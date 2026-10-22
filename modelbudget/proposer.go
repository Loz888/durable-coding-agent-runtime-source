// Package modelbudget governs the existing proposer. It does not schedule runs or repairs.
package modelbudget

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"regexp"
	"runtime-app/repair18"
	"time"
)

type failure struct{ code, message string }

func (e *failure) Error() string       { return e.message }
func (e *failure) FailureCode() string { return e.code }

var ErrBudget = &failure{"model_budget_exhausted", "model budget exhausted"}
var ErrCapacity = &failure{"transient", "global model admission busy"}
var ErrOwner = &failure{"lease_lost", "model caller lost execution authority"}

type Policy struct {
	Experiment, Team                                     string
	BudgetNano                                           int64
	GlobalLimit, TeamLimit, MaxRunCalls, MaxProjectCalls int
	Timeout, UncertainFor                                time.Duration
}
type Proposer struct {
	Pool         *pgxpool.Pool
	Next         repair18.Proposer
	RunID, Owner string
	Epoch        int64
	Policy       Policy
}

var safe = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
var requestID = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

func (p Proposer) validate() error {
	v := p.Policy
	if p.Pool == nil || p.Next == nil || p.RunID == "" || p.Owner == "" || p.Epoch < 1 || !safe.MatchString(v.Experiment) || !safe.MatchString(v.Team) || v.BudgetNano <= 0 || v.BudgetNano > 2000000000 || v.GlobalLimit < 1 || v.GlobalLimit > 16 || v.TeamLimit < 1 || v.TeamLimit > v.GlobalLimit || v.MaxRunCalls < 1 || v.MaxRunCalls > 8 || v.MaxProjectCalls < 1 || v.MaxProjectCalls > 200 || v.Timeout <= 0 || v.Timeout > 75*time.Second || v.UncertainFor < time.Minute || v.UncertainFor > 10*time.Minute {
		return errors.New("invalid model governance policy")
	}
	return nil
}
func (p Proposer) reserve(ctx context.Context, amount int64, input repair18.Request, inputDigest string) (string, error) {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	// A short transaction checks the SAME authoritative run/lease before admission.
	var owned bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM day12.agent_runs WHERE run_id=$1 AND lease_owner=$2 AND lease_epoch=$3 AND lease_until>clock_timestamp() AND status='running')`, p.RunID, p.Owner, p.Epoch).Scan(&owned); err != nil {
		return "", err
	}
	if !owned {
		return "", ErrOwner
	}
	const model = "deepseek:deepseek-flash"
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, model); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO pilot.model_limits(provider_model,global_limit,team_limit) VALUES($1,$2,$3) ON CONFLICT(provider_model) DO UPDATE SET global_limit=LEAST(pilot.model_limits.global_limit,EXCLUDED.global_limit),team_limit=LEAST(pilot.model_limits.team_limit,EXCLUDED.team_limit)`, model, p.Policy.GlobalLimit, p.Policy.TeamLimit)
	if err != nil {
		return "", err
	}
	var global, team int
	if err = tx.QueryRow(ctx, `SELECT global_limit,team_limit FROM pilot.model_limits WHERE provider_model=$1`, model).Scan(&global, &team); err != nil {
		return "", err
	}
	var active, own int
	if err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE team=$2) FROM pilot.model_attempts WHERE provider_model=$1 AND held_until>clock_timestamp()`, model, p.Policy.Team).Scan(&active, &own); err != nil {
		return "", err
	}
	if active >= global || own >= team {
		return "", ErrCapacity
	}
	var runCalls int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM pilot.model_attempts WHERE run_id=$1`, p.RunID).Scan(&runCalls); err != nil {
		return "", err
	}
	if runCalls >= p.Policy.MaxRunCalls {
		return "", ErrBudget
	}
	_, err = tx.Exec(ctx, `INSERT INTO pilot.model_budgets(experiment,budget_nano) VALUES($1,$2) ON CONFLICT(experiment) DO UPDATE SET budget_nano=LEAST(pilot.model_budgets.budget_nano,EXCLUDED.budget_nano)`, p.Policy.Experiment, p.Policy.BudgetNano)
	if err != nil {
		return "", err
	}
	tag, err := tx.Exec(ctx, `UPDATE pilot.model_budgets SET reserved_nano=reserved_nano+$2,calls=calls+1 WHERE experiment=$1 AND reserved_nano+$2<=budget_nano AND calls<$3`, p.Policy.Experiment, amount, p.Policy.MaxProjectCalls)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", ErrBudget
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(random[:])
	_, err = tx.Exec(ctx, `INSERT INTO pilot.model_attempts(attempt_id,run_id,experiment,provider_model,team,reservation_nano,held_until,source_file,source_commit,source_sha256,input_digest) VALUES($1,$2,$3,$4,$5,$6,clock_timestamp()+($7::bigint*interval '1 microsecond'),$8,$9,$10,$11)`, id, p.RunID, p.Policy.Experiment, model, p.Policy.Team, amount, (p.Policy.Timeout + p.Policy.UncertainFor).Microseconds(), input.File, input.SourceCommit, input.SourceHash, inputDigest)
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}
func (p Proposer) Settle(ctx context.Context, id string, out repair18.Suggestion, known bool) error {
	tx, err := p.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var reservation int64
	var already bool
	var experiment string
	if err = tx.QueryRow(ctx, `SELECT reservation_nano,usage_known,experiment FROM pilot.model_attempts WHERE attempt_id=$1 FOR UPDATE`, id).Scan(&reservation, &already, &experiment); err != nil {
		return err
	}
	if already {
		return nil
	}
	if !known || out.PromptTokens < 0 || out.CompletionTokens < 0 {
		return tx.Commit(ctx)
	}
	if out.PromptTokens > 1000000 || out.CompletionTokens > 8192 {
		if _, err = tx.Exec(ctx, `UPDATE pilot.model_budgets SET reserved_nano=budget_nano WHERE experiment=$1`, experiment); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return errors.New("provider usage exceeds governed bounds; experiment frozen")
	}
	charge := int64(out.PromptTokens)*300 + int64(out.CompletionTokens)*1200
	if charge > reservation {
		_, err = tx.Exec(ctx, `UPDATE pilot.model_budgets SET reserved_nano=budget_nano WHERE experiment=$1`, experiment)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var providerID *string
	if requestID.MatchString(out.ProviderRequestID) {
		providerID = &out.ProviderRequestID
	}
	_, err = tx.Exec(ctx, `UPDATE pilot.model_attempts SET usage_known=true,input_tokens=$2,output_tokens=$3,charged_nano=$4,outcome='confirmed_return',provider_request_id=$5,finished_at=clock_timestamp(),held_until=clock_timestamp() WHERE attempt_id=$1`, id, out.PromptTokens, out.CompletionTokens, charge, providerID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE pilot.model_budgets SET reserved_nano=reserved_nano-$2 WHERE experiment=$1`, experiment, reservation-charge)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (p Proposer) Suggest(ctx context.Context, in repair18.Request) (repair18.Suggestion, error) {
	var empty repair18.Suggestion
	if err := p.validate(); err != nil {
		return empty, err
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > 50000 {
		return empty, errors.New("bounded model input required")
	}
	// UTF-8/escaping upper bound plus fixed protocol overhead; actual usage remains separate.
	inputBound := int64(len(raw)*6 + 8192)
	amount := inputBound*300 + 8192*1200
	sourceDigest := sha256.Sum256([]byte(in.Source))
	if in.SourceHash != "" && in.SourceHash != hex.EncodeToString(sourceDigest[:]) {
		return empty, errors.New("source context hash mismatch")
	}
	in.SourceHash = hex.EncodeToString(sourceDigest[:])
	requestDigest := sha256.Sum256(raw)
	attempt, err := p.reserve(ctx, amount, in, hex.EncodeToString(requestDigest[:]))
	if err != nil {
		return empty, err
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	var owned bool
	if err = p.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM day12.agent_runs WHERE run_id=$1 AND lease_owner=$2 AND lease_epoch=$3 AND status='running' AND lease_until>clock_timestamp())`, p.RunID, p.Owner, p.Epoch).Scan(&owned); err != nil {
		return empty, err
	}
	if !owned {
		return empty, ErrOwner
	}
	call, cancel := context.WithTimeout(ctx, p.Policy.Timeout)
	defer cancel()
	out, callErr := p.Next.Suggest(call, in)
	cleanup, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	settleErr := p.Settle(cleanup, attempt, out, callErr == nil && out.UsageKnown)
	if callErr != nil {
		return empty, callErr
	}
	if settleErr != nil {
		return empty, settleErr
	}
	return out, nil
}

func (p Proposer) PolicyIdentity() string { return repair18.ProposalIdentity(p.Next) }
