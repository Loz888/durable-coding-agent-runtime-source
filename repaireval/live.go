package repaireval

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/durable"
	"runtime-app/internal/labdb"
	"runtime-app/modelbudget"
	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
	"sort"
	"strings"
	"syscall"
	"time"
)

const LiveManifestSHA = "b3376e63e8ed6738e185b9bdd5e82c98214214a062770c6ce3826611323df3e4"
const LiveExperiment = "runtime-remaining-20261021-12"

type LiveCase struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Files     map[string]string `json:"files"`
	Visible   string            `json:"visible_test"`
	Oracle    string            `json:"oracle_test"`
	MaxRounds int               `json:"max_rounds"`
	Recovery  bool              `json:"recovery"`
	Reference string            `json:"reference"`
}
type LiveManifest struct {
	Version string     `json:"version"`
	Scope   string     `json:"scope"`
	Cases   []LiveCase `json:"cases"`
}
type LiveObservation struct {
	CaseID             string `json:"case_id"`
	Kind               string `json:"kind"`
	RunID              string `json:"run_id"`
	BaselinePassed     bool   `json:"baseline_passed"`
	RuntimeStatus      string `json:"runtime_status"`
	ErrorCode          string `json:"error_code,omitempty"`
	OraclePassed       bool   `json:"oracle_passed"`
	OriginalClean      bool   `json:"original_clean"`
	Rounds             int    `json:"rounds"`
	PhysicalCalls      int    `json:"physical_calls"`
	KnownCalls         int    `json:"usage_known_calls"`
	InputTokens        int64  `json:"input_tokens"`
	OutputTokens       int64  `json:"output_tokens"`
	EstimatedNano      int64  `json:"peak_cache_miss_estimate_nano_usd"`
	UnknownReservation int64  `json:"unknown_reservation_nano_usd"`
	Model              string `json:"provider_model"`
	RequestID          string `json:"provider_request_id,omitempty"`
	ClaimEpoch         int64  `json:"claim_epoch"`
	RealSIGKILL        bool   `json:"real_sigkill"`
	ResponseReused     bool   `json:"response_reused"`
	StaleRejected      bool   `json:"stale_rejected"`
	InputDigest        string `json:"candidate_digest"`
	OracleSHA          string `json:"oracle_sha256"`
	DurationMS         int64  `json:"duration_ms"`
}
type LiveReport struct {
	ManifestSHA   string            `json:"manifest_sha256"`
	Experiment    string            `json:"experiment"`
	Scope         string            `json:"scope"`
	BudgetNano    int64             `json:"budget_nano_usd"`
	PhysicalCalls int               `json:"physical_calls"`
	InputTokens   int64             `json:"input_tokens"`
	OutputTokens  int64             `json:"output_tokens"`
	EstimateNano  int64             `json:"peak_cache_miss_estimate_nano_usd"`
	ReservedNano  int64             `json:"budget_reserved_nano_usd"`
	Cases         []LiveObservation `json:"cases"`
}
type LiveChildConfig struct {
	Case LiveCase
	Job  durable.ClaimedRun
	Base string
}

func LoadLive(path string) (LiveManifest, error) {
	var m LiveManifest
	raw, e := os.ReadFile(path)
	if e != nil {
		return m, e
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != LiveManifestSHA {
		return m, errors.New("live manifest differs from pre-authorized frozen SHA")
	}
	if json.Unmarshal(raw, &m) != nil || m.Version != "governed-live-multifile-v1" || len(m.Cases) != 12 {
		return m, errors.New("invalid live manifest")
	}
	return m, nil
}
func liveHandler(pool *pgxpool.Pool, c LiveChildConfig) (durable.CodingHandler, *repair19.Store, error) {
	store, e := repair19.NewStore(pool, "day12", "day19")
	if e != nil {
		return durable.CodingHandler{}, nil, e
	}
	files := make([]string, 0, len(c.Case.Files))
	for f := range c.Case.Files {
		files = append(files, f)
	}
	sort.Strings(files)
	model := modelbudget.Proposer{Pool: pool, Next: repair18.DeepSeek{APIKey: os.Getenv("DEEPSEEK_API_KEY")}, RunID: c.Job.RunID, Owner: c.Job.Lease.Owner, Epoch: c.Job.Lease.Epoch, Policy: modelbudget.Policy{Experiment: LiveExperiment, Team: "synthetic-live", BudgetNano: 2000000000, GlobalLimit: 1, TeamLimit: 1, MaxRunCalls: 1, MaxProjectCalls: 11, Timeout: 75 * time.Second, UncertainFor: 2 * time.Minute}}
	h := durable.CodingHandler{ResponseCache: true, Pool: pool, PatchStore: store, PatchSchema: "day19", WorktreeBase: c.Base, File: files[0], Files: files, MaxRounds: 1, TestTimeout: 20 * time.Second, Proposer: model}
	return h, store, nil
}
func liveExecute(ctx context.Context, pool *pgxpool.Pool, c LiveChildConfig, crash bool) error {
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-call.Done():
				return
			case <-ticker.C:
				if _, e := (durable.LeaseManager{Pool: pool}).Renew(call, c.Job.Lease, 3*time.Second); e != nil {
					cancel()
					return
				}
			}
		}
	}()
	h, store, e := liveHandler(pool, c)
	if e != nil {
		return e
	}
	if crash {
		store.ProposalHook = func(stage string) {
			if stage == "response_committed" {
				fmt.Println("LIVE_RESPONSE_COMMITTED")
				select {}
			}
		}
	}
	_, e = h.Execute(call, c.Job)
	cancel()
	<-heartbeatDone
	return e
}
func LiveChild(ctx context.Context, pool *pgxpool.Pool, path string) error {
	var db string
	if e := pool.QueryRow(ctx, "SELECT current_database()").Scan(&db); e != nil || db != "agent_runtime_day14_test" {
		return errors.New("dedicated live test DB required")
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var c LiveChildConfig
	if json.Unmarshal(raw, &c) != nil || !c.Case.Recovery {
		return errors.New("invalid crash child configuration")
	}
	return liveExecute(ctx, pool, c, true)
}
func liveCrash(ctx context.Context, c LiveChildConfig, dir string) error {
	raw, e := json.Marshal(c)
	if e != nil {
		return e
	}
	path := filepath.Join(dir, "child.json")
	if e = os.WriteFile(path, raw, 0600); e != nil {
		return e
	}
	binary, e := os.Executable()
	if e != nil {
		return e
	}
	child := exec.CommandContext(ctx, binary, "-child", path)
	pipe, e := child.StdoutPipe()
	if e != nil {
		return e
	}
	var stderr bytes.Buffer
	child.Stderr = &stderr
	if e = child.Start(); e != nil {
		return e
	}
	waited := false
	defer func() {
		if !waited {
			child.Process.Kill()
			child.Wait()
		}
	}()
	ready := make(chan bool, 1)
	go func() {
		scan := bufio.NewScanner(pipe)
		for scan.Scan() {
			if scan.Text() == "LIVE_RESPONSE_COMMITTED" {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			return errors.New("live child failed before committed response; no paid retry")
		}
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Second):
		return errors.New("live response boundary timeout; no paid retry")
	}
	if e = child.Process.Kill(); e != nil {
		return e
	}
	e = child.Wait()
	waited = true
	var exit *exec.ExitError
	if !errors.As(e, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		return errors.New("child was not SIGKILL")
	}
	return nil
}
func RunLive(ctx context.Context, pool *pgxpool.Pool, m LiveManifest, observe func(LiveObservation) error) (out LiveReport, err error) {
	if os.Getenv("DEEPSEEK_API_KEY") == "" {
		return out, errors.New("DEEPSEEK_API_KEY missing; no paid request made")
	}
	unlock, e := labdb.Lock(ctx, pool)
	if e != nil {
		return out, e
	}
	defer unlock()
	var prior int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM pilot.model_attempts WHERE experiment=$1", LiveExperiment).Scan(&prior); e != nil {
		return out, e
	}
	if prior > 0 {
		return out, errors.New("experiment already has calls; inspect retained evidence, automatic paid rerun refused")
	}
	out = LiveReport{ManifestSHA: LiveManifestSHA, Experiment: LiveExperiment, Scope: m.Scope, BudgetNano: 2000000000}
	for _, c := range m.Cases {
		o, e := liveCase(ctx, pool, c)
		out.Cases = append(out.Cases, o)
		if observe != nil {
			if observeErr := observe(o); observeErr != nil {
				return out, observeErr
			}
		}
		if e != nil {
			return out, fmt.Errorf("live infrastructure stopped at %s; partial evidence retained, no automatic retry", c.ID)
		}
	}
	if e = pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(charged_nano),0) FROM pilot.model_attempts WHERE experiment=$1`, LiveExperiment).Scan(&out.PhysicalCalls, &out.InputTokens, &out.OutputTokens, &out.EstimateNano); e != nil {
		return out, e
	}
	if e = pool.QueryRow(ctx, "SELECT reserved_nano FROM pilot.model_budgets WHERE experiment=$1", LiveExperiment).Scan(&out.ReservedNano); e != nil {
		return out, e
	}
	return out, nil
}
func liveCase(ctx context.Context, pool *pgxpool.Pool, c LiveCase) (o LiveObservation, err error) {
	start := time.Now()
	o.CaseID = c.ID
	o.Kind = c.Kind
	defer func() { o.DurationMS = time.Since(start).Milliseconds() }()
	root, e := os.MkdirTemp("", "governed-live-")
	if e != nil {
		return o, e
	}
	defer os.RemoveAll(root)
	source := filepath.Join(root, "source")
	base := filepath.Join(root, "worktrees")
	if e = os.Mkdir(source, 0700); e != nil {
		return o, e
	}
	for f, data := range c.Files {
		if e = os.WriteFile(filepath.Join(source, f), []byte(data), 0600); e != nil {
			return o, e
		}
	}
	for f, data := range map[string]string{"go.mod": "module example.invalid/livefixture\n\ngo 1.23\n", "visible_test.go": c.Visible} {
		if e = os.WriteFile(filepath.Join(source, f), []byte(data), 0600); e != nil {
			return o, e
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "frozen synthetic live case"}} {
		if _, e = git(ctx, source, args...); e != nil {
			return o, e
		}
	}
	o.RunID = "live20-" + c.ID
	w, e := repair17.Prepare(ctx, source, base, o.RunID)
	if e != nil {
		return o, e
	}
	baseline, e := repair17.VerifyGo(ctx, w.Path, 20*time.Second)
	if e != nil {
		return o, e
	}
	o.BaselinePassed = baseline.Passed
	q := durable.Queue{Pool: pool}
	if e = q.Submit(ctx, durable.Snapshot{RunID: o.RunID, Repo: source, Provider: "coding:deepseek", Goal: "frozen synthetic defect " + c.ID, Status: durable.Running, History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"frozen synthetic evaluation"}`)}}, 0); e != nil {
		return o, e
	}
	jobs, e := q.ClaimBatch(ctx, "live-A-"+c.ID, 1, 3*time.Second)
	if e != nil || len(jobs) != 1 || jobs[0].RunID != o.RunID {
		return o, errors.New("real ClaimBatch failed")
	}
	job := jobs[0]
	o.ClaimEpoch = job.Lease.Epoch
	// Preserve terminal Run, budget and attempt audit rows. On infrastructure errors,
	// terminate only this owned Run, never erase a potentially charged attempt.
	defer func() {
		if err != nil {
			_, _ = pool.Exec(context.Background(), `UPDATE day12.agent_runs SET queue_enabled=false,status='failed',finished_at=clock_timestamp(),last_error_code='evaluation_stopped' WHERE run_id=$1 AND status='running'`, o.RunID)
		}
		// Retain all audit/FK rows but remove owned terminal experiments from
		// active queue statistics. Never delete model attempts to clean a test.
		_, _ = pool.Exec(context.Background(), `UPDATE day12.agent_runs SET queue_enabled=false WHERE run_id=$1 AND status IN ('succeeded','failed')`, o.RunID)
	}()
	config := LiveChildConfig{c, job, base}
	if c.Recovery {
		if e = liveCrash(ctx, config, root); e != nil {
			return o, e
		}
		o.RealSIGKILL = true
		deadline := time.Now().Add(12 * time.Second)
		for {
			jobs, e = q.ClaimBatch(ctx, "live-B-"+c.ID, 1, 3*time.Second)
			if e != nil {
				return o, e
			}
			if len(jobs) > 0 {
				break
			}
			if time.Now().After(deadline) {
				return o, errors.New("takeover after natural expiry timed out")
			}
			select {
			case <-ctx.Done():
				return o, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
		if len(jobs) != 1 || jobs[0].RunID != o.RunID || jobs[0].Lease.Epoch != job.Lease.Epoch+1 {
			return o, errors.New("wrong real claim epoch")
		}
		o.StaleRejected = errors.Is((durable.LeaseManager{Pool: pool}).Check(ctx, job.Lease), durable.ErrLeaseLost)
		config.Job = jobs[0]
		o.ClaimEpoch = config.Job.Lease.Epoch
	}
	// A terminal model/patch failure is an observed result, not an instruction to retry.
	executionError := liveExecute(ctx, pool, config, false)
	if executionError != nil {
		o.ErrorCode = "model_or_patch_rejected"
		var code interface{ FailureCode() string }
		if errors.As(executionError, &code) {
			o.ErrorCode = code.FailureCode()
		}
		f := durable.FencedStore{Pool: pool, Lease: config.Job.Lease}
		snap, e := f.Load(ctx, o.RunID)
		if e == nil {
			before := snap.Revision
			snap.Revision++
			snap.Status = durable.Failed
			snap.Final = "frozen live evaluation stopped after one authorized attempt"
			_ = f.Save(ctx, snap, before)
		}
	}
	snapshot, e := (durable.PostgresStore{Pool: pool}).Load(ctx, o.RunID)
	if e != nil {
		return o, e
	}
	o.RuntimeStatus = string(snapshot.Status)
	if e = pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE usage_known),COALESCE(sum(input_tokens),0),COALESCE(sum(output_tokens),0),COALESCE(sum(charged_nano),0),COALESCE(sum(reservation_nano) FILTER(WHERE NOT usage_known),0) FROM pilot.model_attempts WHERE run_id=$1`, o.RunID).Scan(&o.PhysicalCalls, &o.KnownCalls, &o.InputTokens, &o.OutputTokens, &o.EstimatedNano, &o.UnknownReservation); e != nil {
		return o, e
	}
	if e = pool.QueryRow(ctx, "SELECT COALESCE(max(rounds_used),0) FROM day19.repair_runs WHERE run_id=$1", o.RunID).Scan(&o.Rounds); e != nil {
		return o, e
	}
	var response []byte
	e = pool.QueryRow(ctx, "SELECT response_json FROM day19.model_calls WHERE run_id=$1 AND state='completed' ORDER BY attempt_number DESC LIMIT 1", o.RunID).Scan(&response)
	if e == nil {
		var saved struct {
			Suggestion repair18.Suggestion `json:"suggestion"`
		}
		if json.Unmarshal(response, &saved) == nil {
			o.Model = saved.Suggestion.ProviderModel
			o.RequestID = saved.Suggestion.ProviderRequestID
		}
	}
	o.ResponseReused = c.Recovery && o.PhysicalCalls == 1 && o.Rounds == 1 && o.RuntimeStatus == "succeeded"
	input, e := repair17.CaptureVerification(ctx, w.Path, "live-oracle-v1", 20*time.Second)
	if e != nil {
		return o, e
	}
	defer input.Close()
	o.InputDigest = input.Digest
	h := sha256.Sum256([]byte(c.Oracle))
	o.OracleSHA = hex.EncodeToString(h[:])
	if e = os.WriteFile(filepath.Join(input.Path, "oracle_test.go"), []byte(c.Oracle), 0600); e != nil {
		return o, e
	}
	oracle, e := input.Verify(ctx, 20*time.Second)
	if e != nil {
		return o, e
	}
	o.OraclePassed = oracle.Passed
	dirty, e := git(ctx, source, "status", "--porcelain")
	if e != nil {
		return o, e
	}
	o.OriginalClean = strings.TrimSpace(dirty) == ""
	if !o.OriginalClean || o.PhysicalCalls > 1 || o.Rounds > 1 {
		return o, errors.New("live governance invariant failed")
	}
	return o, nil
}
