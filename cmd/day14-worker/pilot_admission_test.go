package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"runtime-app/durable"
	"runtime-app/internal/testdb"
	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
)

type blockedPilotModel struct{ calls int }

func (m *blockedPilotModel) Suggest(context.Context, repair18.Request) (repair18.Suggestion, error) {
	m.calls++
	return repair18.Suggestion{}, errors.New("unexpected model call")
}

func TestPilotAdmissionRejectsDisabledPaidExecution(t *testing.T) {
	for _, budget := range []int64{-1, 0, 2000000001} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			var h durable.CodingHandler
			configurePilotAdmission(&h, nil, budget)
			m := &blockedPilotModel{}
			if _, e := h.WrapProposer(context.Background(), durable.ClaimedRun{Provider: "coding:deepseek"}, m); e == nil {
				t.Fatal("paid execution admitted without a valid budget")
			}
			if got, e := h.WrapProposer(context.Background(), durable.ClaimedRun{Provider: "coding:mock"}, m); e != nil || got != m {
				t.Fatal("mock unnecessarily requires paid admission", e)
			}
			if _, e := h.ExpectedCommit(context.Background(), durable.ClaimedRun{Provider: "coding:other"}); e == nil {
				t.Fatal("unsupported identity accepted")
			}
			if m.calls != 0 {
				t.Fatal("admission executed model")
			}
		})
	}
}

// Real PostgreSQL, real ClaimBatch and real saved Go-test proof; the DeepSeek
// provider tag exercises Worker wiring only. No provider request or container is used.
func TestPilotCompletionWithPaidCallsDisabled(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated test database required; no SKIP")
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal("open dedicated database", e)
	}
	t.Cleanup(pool.Close)
	var database string
	if e = pool.QueryRow(ctx, "SELECT current_database()").Scan(&database); e != nil || database != "agent_runtime_day14_test" {
		t.Fatal("not dedicated test database", e)
	}
	testdb.LockQueue(t, pool)
	id := fmt.Sprintf("worker-close-%d", time.Now().UnixNano())
	schema := strings.ReplaceAll(id, "-", "_")
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, e := pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Error(e)
		}
		if _, e := pool.Exec(cleanup, "DELETE FROM day12.agent_runs WHERE run_id=$1", id); e != nil {
			t.Error(e)
		}
		if _, e := pool.Exec(cleanup, "DELETE FROM pilot.teams WHERE team=$1", id); e != nil {
			t.Error(e)
		}
	})
	for _, name := range []string{"005_durable_patch.sql", "006_repair_binding.sql", "010_verification_input.sql"} {
		raw, e := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, strings.ReplaceAll(string(raw), "day19", schema)); e != nil {
			t.Fatal(e)
		}
	}
	root := t.TempDir()
	source, base := filepath.Join(root, "source"), filepath.Join(root, "worktrees")
	if e = os.Mkdir(source, 0755); e != nil {
		t.Fatal(e)
	}
	for name, data := range map[string]string{
		"go.mod":       "module example.invalid/workerfixture\n\ngo 1.23\n",
		"math.go":      "package example\nfunc Divide(a,b int) int { return a * b }\n",
		"math_test.go": "package example\nimport \"testing\"\nfunc TestDivide(t *testing.T) { if Divide(12,3)!=4 { t.Fatal(\"bad divide\") } }\n",
	} {
		if e = os.WriteFile(filepath.Join(source, name), []byte(data), 0644); e != nil {
			t.Fatal(e)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "fixture"}} {
		if out, e := exec.Command("git", append([]string{"-C", source}, args...)...).CombinedOutput(); e != nil {
			t.Fatalf("git %s %v", out, e)
		}
	}
	w, e := repair17.Prepare(ctx, source, base, id)
	if e != nil {
		t.Fatal(e)
	}
	q := durable.Queue{Pool: pool, Pilot: true}
	if e = q.Submit(ctx, durable.Snapshot{RunID: id, Repo: source, Provider: "coding:deepseek", Goal: "synthetic completion", Status: durable.Running, History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"synthetic completion"}`)}}, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "UPDATE day12.agent_runs SET max_attempts=1 WHERE run_id=$1", id); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO pilot.teams(team,max_pending,per_minute) VALUES($1,1,1)", id); e != nil {
		t.Fatal(e)
	}
	if _, e = pool.Exec(ctx, "INSERT INTO pilot.runs(run_id,principal,team,repository_id,source_commit,idempotency_key,request_hash) VALUES($1,'fixture',$1,'fixture',$2,$1,'synthetic')", id, w.Commit); e != nil {
		t.Fatal(e)
	}
	jobs, e := q.ClaimBatch(ctx, "worker-admission-fixture", 1, time.Minute)
	if e != nil || len(jobs) != 1 || jobs[0].RunID != id {
		t.Fatal("real claim", e, len(jobs))
	}
	job := jobs[0]
	store, e := repair19.NewStore(pool, "day12", schema)
	if e != nil {
		t.Fatal(e)
	}
	ref := repair19.Ref{ID: id, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch}
	if e = store.Register(ctx, ref, w.Path, 2); e != nil {
		t.Fatal(e)
	}
	sha, e := repair17.Inspect(ctx, w.Path, "math.go")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Prepare(ctx, ref, "repair-001", repair17.Patch{Path: "math.go", ExpectedSHA256: sha, Old: "return a * b", New: "return a / b"}); e != nil {
		t.Fatal(e)
	}
	if _, e = store.ApplyOrRecover(ctx, ref, "repair-001", nil); e != nil {
		t.Fatal(e)
	}
	if result, e := store.Verify(ctx, ref, "repair-001", 15*time.Second); e != nil || !result.Passed {
		t.Fatal("real proof", result, e)
	}
	calls := 0
	forbiddenTest := func(context.Context, string, time.Duration) (repair17.TestResult, error) {
		calls++
		return repair17.TestResult{}, errors.New("unexpected test rerun")
	}
	// Same stable verifier contract; any re-execution now fails the test.
	store.VerificationPolicy = "trusted-go-snapshot-v1"
	store.VerifyGo = forbiddenTest
	model := &blockedPilotModel{}
	h := durable.CodingHandler{Pool: pool, PatchStore: store, PatchSchema: schema, WorktreeBase: base, File: "math.go", MaxRounds: 2, TestTimeout: 15 * time.Second, Proposer: model, VerifyGo: forbiddenTest}
	configurePilotAdmission(&h, pool, 0)
	if _, e = h.Execute(ctx, job); e == nil || !strings.Contains(e.Error(), "paid providers disabled") {
		t.Fatal("normal execution bypassed disabled paid admission", e)
	}
	if model.calls != 0 || calls != 0 {
		t.Fatal("disabled execution performed work")
	}
	if _, e = pool.Exec(ctx, "UPDATE day12.agent_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", id); e != nil {
		t.Fatal(e)
	}
	// Reproduce the former combined identity/paid gate on the actual finalizer.
	identity := h.ExpectedCommit
	h.ExpectedCommit = func(context.Context, durable.ClaimedRun) (string, error) {
		return "", errors.New("pilot paid providers disabled until durable governance acceptance")
	}
	if n, e := h.FinalizeExhausted(ctx, q, 1); e == nil || n != 0 {
		t.Fatal("legacy combined gate did not reproduce blocked completion", n, e)
	}
	var blockedStatus string
	var blockedPending bool
	if e = pool.QueryRow(ctx, "SELECT status,completion_pending FROM day12.agent_runs WHERE run_id=$1", id).Scan(&blockedStatus, &blockedPending); e != nil || blockedStatus != "running" || !blockedPending {
		t.Fatal("blocked completion changed persisted state", blockedStatus, blockedPending, e)
	}
	t.Log("legacy combined paid/identity gate reproduced: running + completion_pending, completion blocked")
	h.ExpectedCommit = identity
	if n, e := h.FinalizeExhausted(ctx, q, 1); e != nil || n != 1 {
		t.Fatal("free completion blocked by paid admission", n, e)
	}
	snap, e := (durable.PostgresStore{Pool: pool}).Load(ctx, id)
	if e != nil || snap.Status != durable.Succeeded || snap.OutcomeVerdict != "tests_passed" || snap.Revision != 1 {
		t.Fatal("not completed", snap, e)
	}
	var epoch int64
	var pending bool
	if e = pool.QueryRow(ctx, "SELECT lease_epoch,completion_pending FROM day12.agent_runs WHERE run_id=$1", id).Scan(&epoch, &pending); e != nil || epoch != 1 || pending {
		t.Fatal("authority changed", epoch, pending, e)
	}
	if model.calls != 0 || calls != 0 {
		t.Fatal("completion executed provider or tests", model.calls, calls)
	}
	if n, e := h.FinalizeExhausted(ctx, q, 1); e != nil || n != 0 {
		t.Fatal("completion not idempotent", n, e)
	}
	t.Log("paid budget=0; normal Execute refused; persisted proof completed; extra model/test calls=0; epoch=1")
}
