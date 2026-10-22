package durable

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/reliability15"
	"runtime-app/repair19"
	"runtime-app/repodiag"
	"syscall"
	"testing"
	"time"
)

func interviewCrash(t *testing.T) (CodingHandler, ClaimedRun) {
	t.Helper()
	pool, ctx := day13DB(t)
	source, base := day20Fixture(t)
	run := day14Run(t, pool, source, 1)
	day20SetProvider(t, ctx, pool, run.RunID, "coding:mock")
	if _, e := pool.Exec(ctx, "UPDATE day12.agent_runs SET max_attempts=1 WHERE run_id=$1", run.RunID); e != nil {
		t.Fatal(e)
	}
	schema := day20Schema(t, ctx, pool)
	ps, e := repair19.NewStore(pool, "day12", schema)
	if e != nil {
		t.Fatal(e)
	}
	a := day20Claim(t, ctx, Queue{Pool: pool}, run.RunID, "day20-crash-a")
	child := exec.Command(os.Args[0], "-test.run=^TestDay20CrashProcess$", "-test.v")
	child.Env = append(os.Environ(), "DAY12_TEST_DATABASE_URL="+pool.Config().ConnString(), "DAY20_CHILD=1", "DAY20_SCHEMA="+schema, "DAY20_RUN="+run.RunID, "DAY20_SOURCE="+source, "DAY20_BASE="+base, "DEEPSEEK_API_KEY=")
	out, e := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(e, &exit) {
		t.Fatalf("expected crash %v %s", e, out)
	}
	ws, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("not SIGKILL %s", out)
	}
	if _, e = pool.Exec(ctx, "UPDATE day12.agent_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", run.RunID); e != nil {
		t.Fatal(e)
	}
	return CodingHandler{Pool: pool, PatchStore: ps, PatchSchema: schema, WorktreeBase: base, File: "math.go", MaxRounds: 2, TestTimeout: 15 * time.Second, Proposer: forbidModel{}}, a
}
func TestInterviewLastClaimSIGKILLFinalizesWithoutExecution(t *testing.T) {
	h, a := interviewCrash(t)
	s := Scheduler{Queue: Queue{Pool: h.Pool}, Owner: "interview-finalizer", Workers: 1, PollEvery: 10 * time.Millisecond, TTL: time.Second, RenewEvery: 100 * time.Millisecond, RunTimeout: time.Second, Models: func(string) (repodiag.Model, error) { return nil, errors.New("diagnosis must not run") }, CodingRun: h.Execute, FinalizeCoding: h.FinalizeExhausted}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if e := s.Run(ctx); e != nil {
		t.Fatal(e)
	}
	snap, e := (PostgresStore{Pool: h.Pool}).Load(context.Background(), a.RunID)
	if e != nil || snap.Status != Succeeded {
		t.Fatalf("last claim repair not finalized: status=%s %v", snap.Status, e)
	}
	interviewAssertFinal(t, h, a, Succeeded)
}

func interviewAssertFinal(t *testing.T, h CodingHandler, a ClaimedRun, want Status) {
	t.Helper()
	ctx := context.Background()
	snap, e := (PostgresStore{Pool: h.Pool}).Load(ctx, a.RunID)
	if e != nil || snap.Status != want {
		t.Fatalf("terminal %+v %v", snap, e)
	}
	var epoch int64
	var retries, max int
	var pending bool
	if e = h.Pool.QueryRow(ctx, "SELECT lease_epoch,retry_count,max_attempts,completion_pending FROM day12.agent_runs WHERE run_id=$1", a.RunID).Scan(&epoch, &retries, &max, &pending); e != nil || epoch != 1 || max != 1 || retries != 0 || pending {
		t.Fatalf("authorization counters %d %d %d %t %v", epoch, retries, max, pending, e)
	}
	used, _, e := h.PatchStore.Summary(ctx, a.RunID)
	if e != nil || used != 1 {
		t.Fatalf("repair budget %d %v", used, e)
	}
	if _, e = h.PatchStore.Verify(ctx, repair19.Ref{ID: a.RunID, Owner: a.Lease.Owner, Epoch: a.Lease.Epoch}, "repair-001", h.TestTimeout); !errors.Is(e, repair19.ErrLeaseLost) {
		t.Fatalf("old owner could verify %v", e)
	}
	old := snap
	old.Status = Succeeded
	old.Revision = snap.Revision + 1
	if e = (FencedStore{Pool: h.Pool, Lease: a.Lease}).Save(ctx, old, snap.Revision); !errors.Is(e, ErrConflict) {
		t.Fatalf("old owner saved %v", e)
	}
	if jobs, e := (Queue{Pool: h.Pool}).ClaimBatch(ctx, "must-not-claim", 1, time.Second); e != nil || len(jobs) != 0 {
		t.Fatalf("extra claim %+v %v", jobs, e)
	}
	out, e := exec.Command("git", "-C", a.Repo, "status", "--porcelain").CombinedOutput()
	if e != nil || len(out) != 0 {
		t.Fatalf("original modified %s %v", out, e)
	}
	t.Logf("DEMO phase=evidence_only_terminal status=%s epoch=%d max_attempts=%d repair_rounds=%d extra_model_calls=0", snap.Status, epoch, max, used)
}

func TestInterviewCompletionRacesSweepAndOtherFinalizer(t *testing.T) {
	h, a := interviewCrash(t)
	ctx := context.Background()
	store, e := reliability15.NewStore(h.Pool, "day12")
	if e != nil {
		t.Fatal(e)
	}
	type result struct {
		n int
		e error
	}
	done := make(chan result, 3)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		go func() { <-start; n, e := h.FinalizeExhausted(ctx, Queue{Pool: h.Pool}, 10); done <- result{n, e} }()
	}
	go func() {
		<-start
		n, e := store.SweepExhausted(ctx, 100)
		if n != 0 && e == nil {
			e = fmt.Errorf("Sweep consumed pending evidence: %d", n)
		}
		done <- result{0, e}
	}()
	close(start)
	total := 0
	for i := 0; i < 3; i++ {
		r := <-done
		if r.e != nil {
			t.Fatal(r.e)
		}
		total += r.n
	}
	if total != 1 {
		t.Fatalf("completion count %d", total)
	}
	interviewAssertFinal(t, h, a, Succeeded)
	n, e := h.FinalizeExhausted(ctx, Queue{Pool: h.Pool}, 10)
	if e != nil || n != 0 {
		t.Fatal("completion not idempotent", n, e)
	}
}

func TestInterviewCompletionRejectsUnprovenEvidence(t *testing.T) {
	for _, kind := range []string{"prepared", "applied", "verified_fail", "missing_digest", "test_changed", "module_changed", "identity", "missing_binding"} {
		t.Run(kind, func(t *testing.T) {
			h, a := interviewCrash(t)
			ctx := context.Background()
			schema := h.PatchSchema
			switch kind {
			case "prepared", "applied", "verified_fail":
				_, e := h.Pool.Exec(ctx, "UPDATE "+schema+".patch_ops SET state=$2 WHERE run_id=$1", a.RunID, kind)
				if e != nil {
					t.Fatal(e)
				}
			case "missing_digest":
				if _, e := h.Pool.Exec(ctx, "UPDATE "+schema+".patch_ops SET verification_input_digest='' WHERE run_id=$1", a.RunID); e != nil {
					t.Fatal(e)
				}
			case "test_changed", "module_changed":
				name := "math_test.go"
				if kind == "module_changed" {
					name = "go.mod"
				}
				path := filepath.Join(h.WorktreeBase, a.RunID, name)
				b, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(path, append(b, []byte("\n// input changed\n")...), 0600); e != nil {
					t.Fatal(e)
				}
			case "identity":
				if _, e := h.Pool.Exec(ctx, "UPDATE "+schema+".repair_runs SET worktree_identity='not-the-worktree' WHERE run_id=$1", a.RunID); e != nil {
					t.Fatal(e)
				}
			case "missing_binding":
				if _, e := h.Pool.Exec(ctx, "DELETE FROM "+schema+".patch_ops WHERE run_id=$1", a.RunID); e != nil {
					t.Fatal(e)
				}
				if _, e := h.Pool.Exec(ctx, "DELETE FROM "+schema+".repair_runs WHERE run_id=$1", a.RunID); e != nil {
					t.Fatal(e)
				}
			}
			n, e := h.FinalizeExhausted(ctx, Queue{Pool: h.Pool}, 10)
			if e != nil || n != 1 {
				t.Fatalf("inspection %d %v", n, e)
			}
			if kind == "missing_binding" {
				snap, e := (PostgresStore{Pool: h.Pool}).Load(ctx, a.RunID)
				if e != nil || snap.Status != Failed {
					t.Fatal(snap, e)
				}
			} else {
				interviewAssertFinal(t, h, a, Failed)
			}
		})
	}
}

func TestInterviewCompletionRollbackAndProfileIsolation(t *testing.T) {
	h, a := interviewCrash(t)
	ctx := context.Background()
	if n, e := h.FinalizeExhausted(ctx, Queue{Pool: h.Pool, Pilot: true}, 10); e != nil || n != 0 {
		t.Fatalf("profile boundary %d %v", n, e)
	}
	if _, e := h.Pool.Exec(ctx, `INSERT INTO day12.checkpoints(run_id,revision,history) SELECT run_id,1,history FROM day12.checkpoints WHERE run_id=$1 AND revision=0`, a.RunID); e != nil {
		t.Fatal(e)
	}
	if _, e := h.FinalizeExhausted(ctx, Queue{Pool: h.Pool}, 10); e == nil {
		t.Fatal("injected checkpoint insert conflict did not roll back")
	}
	var owner, status string
	var pending bool
	var revision int64
	if e := h.Pool.QueryRow(ctx, "SELECT COALESCE(lease_owner,''),status,revision,completion_pending FROM day12.agent_runs WHERE run_id=$1", a.RunID).Scan(&owner, &status, &revision, &pending); e != nil || owner != a.Lease.Owner || status != string(Running) || revision != 0 || !pending {
		t.Fatalf("rollback leaked authority %s %s %d %t %v", owner, status, revision, pending, e)
	}
	if _, e := h.Pool.Exec(ctx, "DELETE FROM day12.checkpoints WHERE run_id=$1 AND revision=1", a.RunID); e != nil {
		t.Fatal(e)
	}
	if n, e := h.FinalizeExhausted(ctx, Queue{Pool: h.Pool}, 10); e != nil || n != 1 {
		t.Fatalf("retry %d %v", n, e)
	}
	interviewAssertFinal(t, h, a, Succeeded)
}
