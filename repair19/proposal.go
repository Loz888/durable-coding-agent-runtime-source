package repair19

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime-app/repair17"
	"time"

	"github.com/jackc/pgx/v5"
	"runtime-app/repair18"
)

var ErrProposalUnknown error = &protocolError{"model result is unknown; automatic physical retry refused", "model_outcome_unknown"}

type savedProposal struct {
	Suggestion repair18.Suggestion `json:"suggestion"`
	Prompt     int                 `json:"prompt_tokens"`
	Completion int                 `json:"completion_tokens"`
}

func digestJSON(v any) (string, error) {
	raw, e := json.Marshal(v)
	if e != nil {
		return "", e
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Propose commits the call intention before dispatch, then the observed response
// before returning it to Prepare. It cannot recover an unobserved provider result.
func (s *Store) Propose(ctx context.Context, r Ref, in repair18.Request, next repair18.Proposer, timeout time.Duration) (repair18.Suggestion, error) {
	var zero repair18.Suggestion
	if next == nil || len(in.ContextDigest) < 64 || len(in.ContextDigest) > 128 || in.Round < 1 || in.Round > 5 {
		return zero, errors.New("bounded verified logical context required")
	}
	raw, e := json.Marshal(in)
	if e != nil || len(raw) > 50000 {
		return zero, errors.New("bounded model request required")
	}
	requestSum := sha256.Sum256(raw)
	requestDigest := hex.EncodeToString(requestSum[:])
	policy := repair18.ProposalIdentity(next)
	contextDigest, e := digestJSON(struct {
		Context, Policy string
		Round           int
	}{in.ContextDigest, policy, in.Round})
	if e != nil {
		return zero, e
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback(ctx)
	if e = s.lockLease(ctx, tx, r); e != nil {
		return zero, e
	}
	path, used, maximum, e := s.binding(ctx, tx, r.ID)
	if e != nil {
		return zero, e
	}
	actual, e := repair17.VerificationDigest(ctx, path, s.inputPolicy("logical-model-context-v1"), timeout)
	if e != nil {
		return zero, e
	}
	if actual != in.ContextDigest {
		return zero, ErrConflict
	}
	inputs := append([]repair18.SourceFile{{File: in.File, SHA256: in.SourceHash, Source: in.Source}}, in.Files...)
	if len(inputs) > 9 {
		return zero, ErrConflict
	}
	for _, input := range inputs {
		sha, e := repair17.Inspect(ctx, path, input.File)
		if e != nil || sha != input.SHA256 || Hash([]byte(input.Source)) != sha {
			return zero, ErrConflict
		}
	}
	if in.Round != used+1 || used >= maximum {
		return zero, ErrBudget
	}
	var attempt int
	var state, storedContext string
	var response []byte
	e = tx.QueryRow(ctx, fmt.Sprintf(`SELECT attempt_number,state,context_digest,response_json FROM %s WHERE run_id=$1 AND round_number=$2 ORDER BY attempt_number DESC LIMIT 1 FOR UPDATE`, s.tbl("model_calls")), r.ID, in.Round).Scan(&attempt, &state, &storedContext, &response)
	if e == nil {
		if storedContext != contextDigest {
			return zero, ErrConflict
		}
		if state == "completed" {
			var saved savedProposal
			if json.Unmarshal(response, &saved) != nil {
				return zero, ErrConflict
			}
			saved.Suggestion.PromptTokens = saved.Prompt
			saved.Suggestion.CompletionTokens = saved.Completion
			if e = s.lockLease(ctx, tx, r); e != nil {
				return zero, e
			}
			return saved.Suggestion, tx.Commit(ctx)
		}
		if state != "retry_authorized" || attempt >= 3 {
			return zero, ErrProposalUnknown
		}
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return zero, e
	}
	attempt++
	_, e = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s(run_id,round_number,attempt_number,context_digest,request_digest,model_policy,owner_id,owner_epoch,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'inflight')`, s.tbl("model_calls")), r.ID, in.Round, attempt, contextDigest, requestDigest, policy, r.Owner, r.Epoch)
	if e != nil {
		return zero, e
	}
	if e = s.lockLease(ctx, tx, r); e != nil {
		return zero, e
	}
	if e = tx.Commit(ctx); e != nil {
		return zero, e
	}
	if s.ProposalHook != nil {
		s.ProposalHook("intent_committed")
	}
	out, callErr := next.Suggest(ctx, in)
	if s.ProposalHook != nil {
		s.ProposalHook("response_received")
	}
	tx, e = s.Pool.Begin(ctx)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback(ctx)
	if e = s.lockLease(ctx, tx, r); e != nil {
		return zero, e
	}
	actual, e = repair17.VerificationDigest(ctx, path, s.inputPolicy("logical-model-context-v1"), timeout)
	if e != nil {
		return zero, e
	}
	if actual != in.ContextDigest {
		return zero, ErrConflict
	}
	state = "unknown"
	var encoded []byte
	if callErr == nil {
		encoded, e = json.Marshal(savedProposal{Suggestion: out, Prompt: out.PromptTokens, Completion: out.CompletionTokens})
		if e != nil || len(encoded) > 512<<10 {
			return zero, errors.New("bounded observed response required")
		}
		state = "completed"
	}
	tag, e := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET state=$4,response_json=$5,finished_at=clock_timestamp() WHERE run_id=$1 AND round_number=$2 AND attempt_number=$3 AND state='inflight' AND owner_id=$6 AND owner_epoch=$7 AND context_digest=$8`, s.tbl("model_calls")), r.ID, in.Round, attempt, state, encoded, r.Owner, r.Epoch, contextDigest)
	if e != nil {
		return zero, e
	}
	if tag.RowsAffected() != 1 {
		return zero, ErrConflict
	}
	if e = s.lockLease(ctx, tx, r); e != nil {
		return zero, e
	}
	if e = tx.Commit(ctx); e != nil {
		return zero, e
	}
	if s.ProposalHook != nil {
		s.ProposalHook("response_committed")
	}
	if callErr != nil {
		return zero, callErr
	}
	return out, nil
}

// AuthorizeProposalRetry is an explicit operator decision under a fresh live
// lease. It preserves the unknown attempt and never resets the paid-call ledger.
func (s *Store) AuthorizeProposalRetry(ctx context.Context, r Ref, round int) error {
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = s.lockLease(ctx, tx, r); e != nil {
		return e
	}
	var attempt int
	var state string
	e = tx.QueryRow(ctx, fmt.Sprintf(`SELECT attempt_number,state FROM %s WHERE run_id=$1 AND round_number=$2 ORDER BY attempt_number DESC LIMIT 1 FOR UPDATE`, s.tbl("model_calls")), r.ID, round).Scan(&attempt, &state)
	if e != nil {
		return e
	}
	if attempt >= 3 {
		return ErrBudget
	}
	if state != "unknown" && state != "inflight" {
		return ErrConflict
	}
	if _, e = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET state='retry_authorized' WHERE run_id=$1 AND round_number=$2 AND attempt_number=$3`, s.tbl("model_calls")), r.ID, round, attempt); e != nil {
		return e
	}
	if e = s.lockLease(ctx, tx, r); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
