package integration20

import (
	"bufio"
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

	"github.com/jackc/pgx/v5/pgxpool"
	"runtime-app/durable"
	"runtime-app/repair17"
	"runtime-app/repair19"
)

func integratedPatchLab(t *testing.T, id string) (*patchLab, repair17.Workspace, string, durable.ClaimedRun) {
	t.Helper()
	s := newPatchLab(t)
	w, source := makeWorktree(t, id)
	snapshot := durable.Snapshot{RunID: id, Repo: source, Provider: "demo", Goal: "repair fixture", Status: durable.Running,
		History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"repair"}`)}}
	q := durable.Queue{Pool: s.Pool}
	if err := q.Submit(context.Background(), snapshot, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := s.Pool.Exec(context.Background(), "DELETE FROM day12.agent_runs WHERE run_id=$1", id); err != nil {
			t.Error(err)
		}
	})
	jobs, err := claimPatchRun(t, q, id, "owner-A", 1)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != id {
		t.Fatalf("real queue claim %+v %v", jobs, err)
	}
	ref := repair19.Ref{ID: id, Owner: jobs[0].Lease.Owner, Epoch: jobs[0].Lease.Epoch}
	if err := s.Register(context.Background(), ref, w.Path, 2); err != nil {
		t.Fatal(err)
	}
	return s, w, source, jobs[0]
}

func nextPatchOwner(t *testing.T, s *patchLab, id string) repair19.Ref {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "UPDATE day12.agent_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", id); err != nil {
		t.Fatal(err)
	}
	jobs, err := claimPatchRun(t, durable.Queue{Pool: s.Pool}, id, "owner-B", 2)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != id {
		var diagnostic string
		diagnosticErr := s.Pool.QueryRow(ctx, `SELECT json_build_object(
   'status',status,'owner',lease_owner,'epoch',lease_epoch,'max',max_attempts,
   'until',lease_until,'now',clock_timestamp(),'available',available_at)::text
   FROM day12.agent_runs WHERE run_id=$1`, id).Scan(&diagnostic)
		t.Fatalf("takeover %+v %v; database=%s diagnostic_error=%v", jobs, err, diagnostic, diagnosticErr)
	}
	return repair19.Ref{ID: id, Owner: jobs[0].Lease.Owner, Epoch: jobs[0].Lease.Epoch}
}

// This helper is killed by its parent only after the requested durable commit.
func TestPre20PatchCrashProcess(t *testing.T) {
	stage := os.Getenv("PRE20_PATCH_STAGE")
	if stage == "" {
		return
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, os.Getenv("DAY12_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var database string
	if err = pool.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil || database != "agent_runtime_day14_test" {
		t.Fatalf("refuse non-test database %q %v", database, err)
	}
	s, err := repair19.NewStore(pool, "day12", os.Getenv("PRE20_PATCH_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	id := os.Getenv("PRE20_PATCH_RUN")
	path := os.Getenv("PRE20_PATCH_PATH")
	ref := repair19.Ref{ID: id, Owner: "owner-A", Epoch: 1}
	sha, err := repair17.Inspect(ctx, path, "math.go")
	if err != nil {
		t.Fatal(err)
	}
	replacement := "return a / b"
	if stage == "verified_fail" {
		replacement = "return a + b"
	}
	if _, err = s.Prepare(ctx, ref, "p1", repair17.Patch{Path: "math.go", ExpectedSHA256: sha, Old: "return a * b", New: replacement}); err != nil {
		t.Fatal(err)
	}
	if stage != "prepared" {
		if _, err = s.ApplyOrRecover(ctx, ref, "p1", nil); err != nil {
			t.Fatal(err)
		}
	}
	if strings.HasPrefix(stage, "verified_") {
		result, err := s.Verify(ctx, ref, "p1", 10*time.Second)
		if err != nil || result.Passed != (stage == "verified_pass") {
			t.Fatalf("verify %+v %v", result, err)
		}
	}
	fmt.Printf("DURABLE %s\n", stage)
	select {}
}

func TestPre20SIGKILLRecoveryMatrix(t *testing.T) {
	for _, stage := range []string{"prepared", "applied", "verified_pass", "verified_fail"} {
		t.Run(stage, func(t *testing.T) {
			id := "pre20-crash-" + stage
			s, w, source, claim := integratedPatchLab(t, id)
			ctx := context.Background()
			// Count verification side effects in the trusted fixture to prove that
			// replay of a persisted verdict does not run Go tests again.
			testSource := `package example
import ("testing";"os")
func TestDivide(t *testing.T){
 f,err:=os.OpenFile("verify-runs.txt",os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600)
 if err!=nil {t.Fatal(err)};defer f.Close();f.WriteString("run\n")
 if Divide(12,3)!=4 {t.Fatal("bad divide")}
}
`
			if err := os.WriteFile(filepath.Join(w.Path, "math_test.go"), []byte(testSource), 0600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			child := exec.Command(binary, "-test.run=^TestPre20PatchCrashProcess$", "-test.timeout=20s")
			child.Env = append(os.Environ(), "PRE20_PATCH_STAGE="+stage, "PRE20_PATCH_RUN="+id,
				"PRE20_PATCH_PATH="+w.Path, "PRE20_PATCH_SCHEMA="+s.schema, "PRE20_PARENT_HOLDS_QUEUE_LOCK=1",
				"DAY12_TEST_DATABASE_URL="+s.dsn)
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err = child.Start(); err != nil {
				t.Fatal(err)
			}
			ready := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if strings.HasPrefix(scanner.Text(), "DURABLE ") {
						ready <- scanner.Text()
						return
					}
				}
				ready <- ""
			}()
			select {
			case got := <-ready:
				if got != "DURABLE "+stage {
					_ = child.Process.Kill()
					_ = child.Wait()
					t.Fatalf("child output %q", got)
				}
			case <-time.After(10 * time.Second):
				_ = child.Process.Kill()
				_ = child.Wait()
				t.Fatal("child did not reach durable crash point")
			}
			if err = child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err = child.Wait(); err == nil {
				t.Fatal("child was not killed")
			}
			before, _ := os.ReadFile(filepath.Join(w.Path, "verify-runs.txt"))
			b := nextPatchOwner(t, s, id)
			if b.Epoch != claim.Lease.Epoch+1 {
				t.Fatal("epoch did not advance")
			}
			pending, err := s.Pending(ctx, b)
			if err != nil || len(pending) != 1 || pending[0].State != stage {
				t.Fatalf("recovery scan %+v %v", pending, err)
			}
			a := repair19.Ref{ID: id, Owner: claim.Lease.Owner, Epoch: claim.Lease.Epoch}
			if _, err = s.ApplyOrRecover(ctx, a, "p1", nil); !errors.Is(err, repair19.ErrLeaseLost) {
				t.Fatalf("old apply accepted: %v", err)
			}
			if _, err = s.Verify(ctx, a, "p1", time.Second); !errors.Is(err, repair19.ErrLeaseLost) {
				t.Fatalf("old verify accepted: %v", err)
			}
			if stage == "prepared" {
				if _, err = s.ApplyOrRecover(ctx, b, "p1", nil); err != nil {
					t.Fatal(err)
				}
			} else {
				outcome, err := s.ApplyOrRecover(ctx, b, "p1", nil)
				if err != nil || outcome.Action != repair19.RecoverApplied {
					t.Fatalf("replayed applied patch %+v %v", outcome, err)
				}
				if strings.HasPrefix(stage, "verified_") && outcome.State != stage {
					t.Fatal("verified state reset")
				}
			}
			result, err := s.Verify(ctx, b, "p1", 10*time.Second)
			if err != nil || result.Passed != (stage != "verified_fail") {
				t.Fatalf("recovered verify %+v %v", result, err)
			}
			after, _ := os.ReadFile(filepath.Join(w.Path, "verify-runs.txt"))
			if strings.HasPrefix(stage, "verified_") && string(before) != string(after) {
				t.Fatal("cached verdict reran verification")
			}
			if result.Passed {
				sha, err := repair17.Inspect(ctx, w.Path, "math.go")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Prepare(ctx, b, "must-not-regenerate", repair17.Patch{Path: "math.go", ExpectedSHA256: sha, Old: "return a / b", New: "return a + b"}); !errors.Is(err, repair19.ErrConflict) {
					t.Fatalf("verified pass allowed another intent: %v", err)
				}
			}
			used, max, err := s.Summary(ctx, id)
			if err != nil || used != 1 || max != 2 {
				t.Fatalf("recovery consumed budget %d/%d %v", used, max, err)
			}
			original, err := os.ReadFile(filepath.Join(source, "math.go"))
			if err != nil || !strings.Contains(string(original), "return a * b") {
				t.Fatal("original repository polluted")
			}
			statusCmd := exec.Command("git", "-C", source, "status", "--porcelain")
			status, err := statusCmd.Output()
			if err != nil || strings.TrimSpace(string(status)) != "" {
				t.Fatalf("source status %q %v", status, err)
			}
		})
	}
}

func TestPre20PatchStateIdempotencyAndSharedBudget(t *testing.T) {
	s, w, _, claim := integratedPatchLab(t, "pre20-two-rounds")
	ctx := context.Background()
	a := repair19.Ref{ID: w.RunID, Owner: claim.Lease.Owner, Epoch: claim.Lease.Epoch}
	p := patchFor(t, w)
	if _, err := repair17.Apply(ctx, w.Path, p); err == nil {
		t.Fatal("standalone writer bypassed durable binding")
	}
	p.New = "return a + b"
	if _, err := s.Prepare(ctx, a, "p1", p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(ctx, a, "p1", p); err != nil {
		t.Fatal("identical intent not idempotent", err)
	}
	changed := p
	changed.New = "return a - b"
	if _, err := s.Prepare(ctx, a, "p1", changed); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("different content allowed: %v", err)
	}
	if _, err := s.Prepare(ctx, a, "overlap", p); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("unfinished patch overlap: %v", err)
	}
	if _, err := s.ApplyOrRecover(ctx, a, "p1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(ctx, a, "overlap", p); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("applied overlap: %v", err)
	}
	if result, err := s.Verify(ctx, a, "p1", 10*time.Second); err != nil || result.Passed {
		t.Fatalf("expected failing evidence %+v %v", result, err)
	}
	b := nextPatchOwner(t, s, w.RunID)
	known, err := os.ReadFile(filepath.Join(w.Path, "math.go"))
	if err != nil {
		t.Fatal(err)
	}
	unknown := []byte("package example\nfunc Divide(a,b int) int { return 42 }\n")
	if err = os.WriteFile(filepath.Join(w.Path, "math.go"), unknown, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Prepare(ctx, b, "unknown-version", repair17.Patch{Path: "math.go", ExpectedSHA256: repair19.Hash(unknown), Old: "return 42", New: "return 0"}); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("unknown bytes accepted by new intent: %v", err)
	}
	if err = os.WriteFile(filepath.Join(w.Path, "math.go"), known, 0600); err != nil {
		t.Fatal(err)
	}
	sha, err := repair17.Inspect(ctx, w.Path, "math.go")
	if err != nil {
		t.Fatal(err)
	}
	second := repair17.Patch{Path: "math.go", ExpectedSHA256: sha, Old: "return a + b", New: "return a - b"}
	if _, err := s.Prepare(ctx, b, "p2", second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyOrRecover(ctx, b, "p2", nil); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Verify(ctx, b, "p2", 10*time.Second); err != nil || result.Passed {
		t.Fatalf("expected failed round2 %+v %v", result, err)
	}
	sha, err = repair17.Inspect(ctx, w.Path, "math.go")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(ctx, b, "p3", repair17.Patch{Path: "math.go", ExpectedSHA256: sha, Old: "return a - b", New: "return a / b"}); !errors.Is(err, repair19.ErrBudget) {
		t.Fatalf("shared budget bypass %v", err)
	}
	// Simulate a wall clock reversal without changing causal repair counters.
	if _, err = s.Pool.Exec(ctx, fmt.Sprintf("UPDATE %s SET created_at=created_at-interval '1 hour' WHERE run_id=$1 AND patch_id='p2'", s.table("patch_ops")), w.RunID); err != nil {
		t.Fatal(err)
	}
	recovery, err := s.Recoverable(ctx, b)
	if err != nil || len(recovery) != 2 || recovery[1].ID != "p2" {
		t.Fatalf("causal repair order lost %+v %v", recovery, err)
	}
	used, max, err := s.Summary(ctx, w.RunID)
	if err != nil || used != 2 || max != 2 {
		t.Fatalf("budget %d/%d %v", used, max, err)
	}
}

func TestPre20WorktreeRebindingReplacementAndCommitMismatch(t *testing.T) {
	s, w, source, claim := integratedPatchLab(t, "pre20-binding")
	ctx := context.Background()
	ref := repair19.Ref{ID: w.RunID, Owner: claim.Lease.Owner, Epoch: claim.Lease.Epoch}
	if err := s.Register(ctx, ref, w.Path, 2); err != nil {
		t.Fatal(err)
	}
	p := patchFor(t, w)
	if _, err := s.Prepare(ctx, ref, "p1", p); err != nil {
		t.Fatal(err)
	}
	alternative, err := repair17.Prepare(ctx, source, filepath.Join(t.TempDir(), "alternative"), w.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Register(ctx, ref, alternative.Path, 2); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("path rebound %v", err)
	}
	if _, err = s.Pool.Exec(ctx, fmt.Sprintf("UPDATE %s SET source_commit='wrong-commit' WHERE run_id=$1", s.table("repair_runs")), w.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ApplyOrRecover(ctx, ref, "p1", nil); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("commit mismatch accepted %v", err)
	}
	if _, err = s.Pool.Exec(ctx, fmt.Sprintf("UPDATE %s SET source_commit=$2 WHERE run_id=$1", s.table("repair_runs")), w.RunID, w.Commit); err != nil {
		t.Fatal(err)
	}
	// Simulate accidental destruction/recreation at the same absolute path.
	runGit(t, source, "worktree", "remove", "--force", w.Path)
	if _, err = s.Pending(ctx, ref); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("missing worktree accepted %v", err)
	}
	replacement, err := repair17.Prepare(ctx, source, filepath.Dir(w.Path), w.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Identity == w.Identity {
		t.Fatal("replacement reused identity")
	}
	if _, err = s.ApplyOrRecover(ctx, ref, "p1", nil); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("replacement accepted %v", err)
	}
	if err = s.Register(ctx, ref, replacement.Path, 2); !errors.Is(err, repair19.ErrConflict) {
		t.Fatalf("replacement re-registered %v", err)
	}
}

func TestPre20LeaseExpiryAfterRenameLeavesRecoverableIntent(t *testing.T) {
	s, w, _, claim := integratedPatchLab(t, "pre20-expired-write")
	ctx := context.Background()
	ref := repair19.Ref{ID: w.RunID, Owner: claim.Lease.Owner, Epoch: claim.Lease.Epoch}
	if err := s.Register(ctx, ref, w.Path, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prepare(ctx, ref, "p1", patchFor(t, w)); err != nil {
		t.Fatal(err)
	}
	var until time.Time
	if err := s.Pool.QueryRow(ctx, fmt.Sprintf("UPDATE %s.agent_runs SET lease_until=clock_timestamp()+interval '300 milliseconds' WHERE run_id=$1 RETURNING lease_until", "day12"), w.RunID).Scan(&until); err != nil {
		t.Fatal(err)
	}
	_, err := s.ApplyOrRecover(ctx, ref, "p1", func() {
		deadline := time.Now().Add(3 * time.Second)
		for {
			var expired bool
			if err := s.Pool.QueryRow(ctx, "SELECT clock_timestamp()>$1", until).Scan(&expired); err != nil {
				panic(err)
			}
			if expired {
				return
			}
			if time.Now().After(deadline) {
				panic("database time did not advance")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	if !errors.Is(err, repair19.ErrLeaseLost) {
		t.Fatalf("late result accepted %v", err)
	}
	var state string
	if err := s.Pool.QueryRow(ctx, fmt.Sprintf("SELECT state FROM %s WHERE run_id=$1", s.table("patch_ops")), w.RunID).Scan(&state); err != nil || state != "prepared" {
		t.Fatalf("uncommitted state=%s %v", state, err)
	}
	next := nextPatchOwner(t, s, w.RunID)
	if next.Epoch != claim.Lease.Epoch+1 {
		t.Fatal("epoch did not advance")
	}
	outcome, err := s.ApplyOrRecover(ctx, next, "p1", nil)
	if err != nil || outcome.Action != repair19.RecoverApplied {
		t.Fatalf("late rename recovery %+v %v", outcome, err)
	}
}

func TestPre20HeartbeatRenewSharesPatchRowLock(t *testing.T) {
	s, w, _, claim := integratedPatchLab(t, "pre20-renew-lock")
	ctx := context.Background()
	ref := repair19.Ref{ID: w.RunID, Owner: claim.Lease.Owner, Epoch: claim.Lease.Epoch}
	if _, err := s.Prepare(ctx, ref, "p1", patchFor(t, w)); err != nil {
		t.Fatal(err)
	}
	renamed := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() { _, err := s.ApplyOrRecover(ctx, ref, "p1", func() { close(renamed); <-release }); done <- err }()
	<-renamed
	renewCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	_, err := (durable.LeaseManager{Pool: s.Pool}).Renew(renewCtx, claim.Lease, time.Second)
	cancel()
	close(release)
	if err == nil {
		t.Error("heartbeat bypassed patch row lock")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := (durable.LeaseManager{Pool: s.Pool}).Renew(ctx, claim.Lease, time.Second); err != nil {
		t.Fatal("renew did not resume after lock release", err)
	}
}

// SKIP LOCKED and wall-clock-based available_at legitimately return no rows.
// Wait for actual eligibility; do not rewrite availability or authorization counts.
func claimPatchRun(t *testing.T, q durable.Queue, id, owner string, wantEpoch int64) ([]durable.ClaimedRun, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var diagnostic string
	for {
		jobs, err := q.ClaimBatch(ctx, owner, 1, 30*time.Second)
		if err != nil {
			return nil, err
		}
		if len(jobs) > 0 {
			if len(jobs) != 1 || jobs[0].RunID != id || jobs[0].Lease.Epoch != wantEpoch {
				t.Fatalf("unexpected authorization %+v", jobs)
			}
			return jobs, nil
		}
		var status string
		var epoch, max int64
		err = q.Pool.QueryRow(ctx, `SELECT status,lease_epoch,max_attempts,
   json_build_object('owner',lease_owner,'epoch',lease_epoch,'until',lease_until,
   'now',clock_timestamp(),'available',available_at)::text
   FROM day12.agent_runs WHERE run_id=$1`, id).Scan(&status, &epoch, &max, &diagnostic)
		if err != nil {
			return nil, err
		}
		if status != "running" || epoch != wantEpoch-1 || epoch >= max {
			t.Fatalf("claim precondition changed: %s", diagnostic)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("eligible claim timed out: %s: %v", diagnostic, ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
}
