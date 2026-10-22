// Package repaireval exercises existing durable repairs on trusted, frozen,
// synthetic fixtures. The scripted proposer is not a language model evaluation.
package repaireval

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime-app/durable"
	"runtime-app/internal/labdb"
	"runtime-app/observability"
	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
	"strings"
	"time"
)

type Case struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Old       string `json:"old"`
	New       string `json:"new"`
	Visible   string `json:"visible_test"`
	Oracle    string `json:"oracle_test"`
	Reference string `json:"reference"`
}
type Manifest struct {
	Version string `json:"version"`
	Mode    string `json:"mode"`
	Scope   string `json:"scope"`
	Cases   []Case `json:"cases"`
}

func Load(path string) (Manifest, string, error) {
	var m Manifest
	raw, e := os.ReadFile(path)
	if e != nil {
		return m, "", e
	}
	if len(raw) > 1<<20 {
		return m, "", errors.New("bounded manifest required")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(&m); e != nil {
		return m, "", e
	}
	if d.Decode(new(any)) != io.EOF {
		return m, "", errors.New("manifest must contain exactly one JSON object")
	}
	h := sha256.Sum256(raw)
	return m, hex.EncodeToString(h[:]), validate(m)
}
func validate(m Manifest) error {
	if m.Version != "controlled-single-file-v1" || m.Mode != "scripted-only" || len(m.Cases) < 1 || len(m.Cases) > 100 {
		return errors.New("unsupported frozen manifest")
	}
	ids := map[string]bool{}
	safe := regexp.MustCompile(`^[a-z][a-z0-9-]{0,49}$`)
	for _, c := range m.Cases {
		if !safe.MatchString(c.ID) || ids[c.ID] || len(c.Source) > 16000 || len(c.Visible) > 16000 || len(c.Oracle) > 16000 || !strings.HasPrefix(c.Source, "package fixture\n") || c.Reference == "" || c.Visible == "" || c.Oracle == "" || c.Old == c.New {
			return errors.New("invalid or duplicate fixture")
		}
		ids[c.ID] = true
		switch c.Kind {
		case "repair", "already_passed", "exhausted", "invalid_patch", "oracle_rejects":
		default:
			return errors.New("unknown fixture expectation")
		}
	}
	return nil
}

type Observation struct {
	CandidateSHA      string `json:"candidate_sha256"`
	OracleInputDigest string `json:"oracle_input_digest"`
	CaseID            string `json:"case_id"`
	Kind              string `json:"kind"`
	RuntimeStatus     string `json:"runtime_status"`
	RuntimeVerdict    string `json:"runtime_verdict"`
	ErrorCode         string `json:"error_code,omitempty"`
	BaselinePassed    bool   `json:"baseline_passed"`
	OraclePassed      bool   `json:"oracle_passed"`
	OriginalClean     bool   `json:"original_clean"`
	ExpectedBehavior  bool   `json:"expected_behavior"`
	ClaimEpoch        int64  `json:"claim_epoch"`
	Rounds            int    `json:"repair_rounds"`
	ProposerCalls     int    `json:"scripted_proposer_calls"`
	DurationMS        int64  `json:"duration_ms"`
	SourceSHA         string `json:"source_sha256"`
	OracleSHA         string `json:"oracle_sha256"`
}
type Report struct {
	Mode                   string        `json:"mode"`
	Scope                  string        `json:"scope"`
	ManifestSHA            string        `json:"manifest_sha256"`
	GoVersion              string        `json:"go_version"`
	Cases                  []Observation `json:"cases"`
	Expected               int           `json:"expected_behaviors"`
	Total                  int           `json:"total"`
	VisiblePositive        int           `json:"visible_test_positive"`
	OraclePositive         int           `json:"oracle_positive"`
	OracleRejectedPositive int           `json:"visible_positive_oracle_negative"`
	PaidCalls              int           `json:"paid_calls"`
	Cleanup                bool          `json:"owned_resources_cleaned"`
}
type programmed struct {
	old, new string
	calls    int
}

func (p *programmed) Suggest(_ context.Context, in repair18.Request) (repair18.Suggestion, error) {
	p.calls++
	if in.Round != 1 || p.calls != 1 {
		return repair18.Suggestion{}, errors.New("unexpected extra scripted call")
	}
	return repair18.Suggestion{Old: p.old, New: p.new, Reason: "frozen synthetic proposal; not a model response"}, nil
}
func git(ctx context.Context, path string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...)
	data, e := cmd.CombinedOutput()
	if e != nil {
		return "", errors.New("trusted fixture git operation failed")
	}
	return strings.TrimSpace(string(data)), nil
}
func evaluateCase(ctx context.Context, pool *pgxpool.Pool, c Case) (o Observation, err error) {
	o.CaseID = c.ID
	o.Kind = c.Kind
	started := time.Now()
	defer func() { o.DurationMS = time.Since(started).Milliseconds() }()
	h := sha256.Sum256([]byte(c.Source))
	o.SourceSHA = hex.EncodeToString(h[:])
	h = sha256.Sum256([]byte(c.Oracle))
	o.OracleSHA = hex.EncodeToString(h[:])
	root, e := os.MkdirTemp("", "runtime-repair-eval-")
	if e != nil {
		return o, e
	}
	defer os.RemoveAll(root)
	source, base := filepath.Join(root, "source"), filepath.Join(root, "worktrees")
	if e = os.Mkdir(source, 0700); e != nil {
		return o, e
	}
	for name, data := range map[string]string{"go.mod": "module example.invalid/repaireval\n\ngo 1.23\n", "solution.go": c.Source, "visible_test.go": c.Visible} {
		if e = os.WriteFile(filepath.Join(source, name), []byte(data), 0600); e != nil {
			return o, e
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "frozen synthetic defect"}} {
		if _, e = git(ctx, source, args...); e != nil {
			return o, e
		}
	}
	var nonce [12]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return o, e
	}
	id := "eval-" + hex.EncodeToString(nonce[:])
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		for _, table := range []string{"day19.patch_ops", "day19.repair_runs", "day12.agent_runs"} {
			if _, e := pool.Exec(cleanup, "DELETE FROM "+table+" WHERE run_id=$1", id); err == nil && e != nil {
				err = errors.New("owned evaluation row cleanup failed")
			}
		}
	}()
	q := durable.Queue{Pool: pool}
	if e = q.Submit(ctx, durable.Snapshot{RunID: id, Repo: source, Provider: "coding:mock", Goal: "trusted synthetic fixture " + c.ID, Status: durable.Running, History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"trusted frozen fixture"}`)}}, 0); e != nil {
		return o, e
	}
	jobs, e := q.ClaimBatch(ctx, id, 1, 2*time.Minute)
	if e != nil || len(jobs) != 1 || jobs[0].RunID != id {
		return o, errors.New("real evaluation ClaimBatch failed")
	}
	job := jobs[0]
	o.ClaimEpoch = job.Lease.Epoch
	w, e := repair17.Prepare(ctx, source, base, id)
	if e != nil {
		return o, e
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_, _ = git(cleanup, source, "worktree", "remove", "--force", w.Path)
	}()
	baseline, e := repair17.VerifyGo(ctx, w.Path, 15*time.Second)
	if e != nil {
		return o, e
	}
	o.BaselinePassed = baseline.Passed
	if o.BaselinePassed != (c.Kind == "already_passed") {
		return o, errors.New("frozen visible baseline does not match its declared expectation")
	}
	store, e := repair19.NewStore(pool, "day12", "day19")
	if e != nil {
		return o, e
	}
	proposer := &programmed{old: c.Old, new: c.New}
	handler := durable.CodingHandler{Pool: pool, PatchStore: store, PatchSchema: "day19", WorktreeBase: base, File: "solution.go", MaxRounds: 1, TestTimeout: 15 * time.Second, Proposer: proposer}
	_, executionError := handler.Execute(observability.RestoreTrace(ctx, job.TraceParent, job.TraceState), job)
	o.ProposerCalls = proposer.calls
	if executionError != nil {
		if c.Kind != "invalid_patch" || !errors.Is(executionError, repair19.ErrConflict) {
			return o, executionError
		}
		o.ErrorCode = "patch_conflict"
	}
	snapshot, e := (durable.PostgresStore{Pool: pool}).Load(ctx, id)
	if e != nil {
		return o, e
	}
	o.RuntimeStatus = string(snapshot.Status)
	o.RuntimeVerdict = snapshot.OutcomeVerdict
	used, maximum, e := store.Summary(ctx, id)
	if e != nil {
		return o, e
	}
	o.Rounds = used
	if maximum != 1 {
		return o, errors.New("repair budget unexpectedly changed")
	}
	input, e := repair17.CaptureVerification(ctx, w.Path, "independent-oracle-v1", 15*time.Second)
	if e != nil {
		return o, e
	}
	defer input.Close()
	candidate, e := os.ReadFile(filepath.Join(w.Path, "solution.go"))
	if e != nil {
		return o, e
	}
	candidateHash := sha256.Sum256(candidate)
	o.CandidateSHA = hex.EncodeToString(candidateHash[:])
	oracleHash := sha256.Sum256([]byte("oracle-v1:" + input.Digest + ":" + o.OracleSHA))
	o.OracleInputDigest = "oracle-v1:" + hex.EncodeToString(oracleHash[:])
	if e = os.WriteFile(filepath.Join(input.Path, "oracle_test.go"), []byte(c.Oracle), 0600); e != nil {
		return o, e
	}
	oracle, e := input.Verify(ctx, 15*time.Second)
	if e != nil {
		return o, e
	}
	o.OraclePassed = oracle.Passed
	dirty, e := git(ctx, source, "status", "--porcelain")
	if e != nil {
		return o, e
	}
	o.OriginalClean = dirty == ""
	o.ExpectedBehavior = expected(o)
	return o, nil
}
func expected(o Observation) bool {
	if !o.OriginalClean || o.ClaimEpoch != 1 {
		return false
	}
	switch o.Kind {
	case "repair":
		return !o.BaselinePassed && o.RuntimeStatus == "succeeded" && o.RuntimeVerdict == "tests_passed" && o.OraclePassed && o.Rounds == 1 && o.ProposerCalls == 1
	case "already_passed":
		return o.BaselinePassed && o.RuntimeStatus == "succeeded" && o.OraclePassed && o.Rounds == 0 && o.ProposerCalls == 0
	case "exhausted":
		return !o.BaselinePassed && o.RuntimeStatus == "failed" && !o.OraclePassed && o.Rounds == 1 && o.ProposerCalls == 1
	case "invalid_patch":
		return o.ErrorCode == "patch_conflict" && o.RuntimeStatus == "running" && !o.OraclePassed && o.Rounds == 0 && o.ProposerCalls == 1
	case "oracle_rejects":
		return !o.BaselinePassed && o.RuntimeStatus == "succeeded" && !o.OraclePassed && o.Rounds == 1 && o.ProposerCalls == 1
	}
	return false
}
func Run(ctx context.Context, pool *pgxpool.Pool, m Manifest, manifestSHA string, observe func(Observation) error) (out Report, err error) {
	if err = validate(m); err != nil {
		return out, err
	}
	unlock, err := labdb.Lock(ctx, pool)
	if err != nil {
		return out, err
	}
	defer unlock()
	out = Report{Mode: "scripted-only", Scope: "trusted synthetic single-file fixtures; visible tests plus independently specified holdout oracle; no LLM quality or production repair rate claim", ManifestSHA: manifestSHA, GoVersion: runtime.Version(), Total: len(m.Cases), Cleanup: true}
	for _, c := range m.Cases {
		o, e := evaluateCase(ctx, pool, c)
		out.Cases = append(out.Cases, o)
		if e != nil {
			out.Cleanup = false
			return out, fmt.Errorf("evaluation infrastructure failed for %s: %w", c.ID, e)
		}
		if o.ExpectedBehavior {
			out.Expected++
		}
		if o.RuntimeStatus == "succeeded" {
			out.VisiblePositive++
			if !o.OraclePassed {
				out.OracleRejectedPositive++
			}
		}
		if o.OraclePassed {
			out.OraclePositive++
		}
		if observe != nil {
			if e = observe(o); e != nil {
				return out, e
			}
		}
	}
	if out.Expected != out.Total {
		return out, errors.New("one or more frozen fixture expectations failed")
	}
	return out, nil
}
