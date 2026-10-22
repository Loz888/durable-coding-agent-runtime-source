package integration20

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"runtime-app/artifactstore"
	"runtime-app/durable"
	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
)

func multiLab(t *testing.T, id string) (*patchLab, repair17.Workspace, string, durable.ClaimedRun, string) {
	t.Helper()
	s := newPatchLab(t)
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if e := os.Mkdir(source, 0700); e != nil {
		t.Fatal(e)
	}
	counter := filepath.Join(root, "test-count")
	files := map[string]string{"go.mod": "module example.invalid/multi\n\ngo 1.23\n", "a.go": "package fixture\nfunc Add(a,b int)int{return a-b}\n", "b.go": "package fixture\nfunc Sub(a,b int)int{return a+b}\n", "math_test.go": fmt.Sprintf(`package fixture
import("testing";"os")
func TestBoth(t *testing.T){f,e:=os.OpenFile(%q,os.O_CREATE|os.O_APPEND|os.O_WRONLY,0600);if e!=nil{t.Fatal(e)};f.WriteString("run\n");f.Close();if Add(5,3)!=8||Sub(5,3)!=2{t.Fatal("arithmetic")}}
`, counter)}
	for n, data := range files {
		if e := os.WriteFile(filepath.Join(source, n), []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	runGit(t, source, "init", "-q")
	runGit(t, source, "add", ".")
	runGit(t, source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "frozen multi fixture")
	w, e := repair17.Prepare(ctx, source, filepath.Join(root, "worktrees"), id)
	if e != nil {
		t.Fatal(e)
	}
	q := durable.Queue{Pool: s.Pool}
	if e = q.Submit(ctx, durable.Snapshot{RunID: id, Repo: source, Provider: "coding:mock", Goal: "multi-file repair", Status: durable.Running, History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"repair both"}`)}}, 0); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := s.Pool.Exec(context.Background(), "DELETE FROM day12.agent_runs WHERE run_id=$1", id); e != nil {
			t.Error(e)
		}
	})
	jobs, e := claimPatchRun(t, q, id, "owner-A", 1)
	if e != nil || len(jobs) != 1 || jobs[0].RunID != id {
		t.Fatal("real initial claim", jobs, e)
	}
	if e = s.Register(ctx, repair19.Ref{ID: id, Owner: jobs[0].Lease.Owner, Epoch: jobs[0].Lease.Epoch}, w.Path, 2); e != nil {
		t.Fatal(e)
	}
	return s, w, source, jobs[0], counter
}
func multiPatches(t *testing.T, w repair17.Workspace, fail bool) []repair17.Patch {
	t.Helper()
	var out []repair17.Patch
	for _, f := range []struct{ path, old, new string }{{"a.go", "return a-b", "return a+b"}, {"b.go", "return a+b", "return a-b"}} {
		sha, e := repair17.Inspect(context.Background(), w.Path, f.path)
		if e != nil {
			t.Fatal(e)
		}
		replacement := f.new
		if fail && f.path == "a.go" {
			replacement = "return 0"
		}
		out = append(out, repair17.Patch{Path: f.path, ExpectedSHA256: sha, Old: f.old, New: replacement})
	}
	return out
}
func TestMultiFileCrashProcess(t *testing.T) {
	stage := os.Getenv("MULTI_STAGE")
	if stage == "" {
		return
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, os.Getenv("DAY12_TEST_DATABASE_URL"))
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	var db string
	if e = pool.QueryRow(ctx, "SELECT current_database()").Scan(&db); e != nil || db != "agent_runtime_day14_test" {
		t.Fatal("not dedicated test DB")
	}
	s, e := repair19.NewStore(pool, "day12", os.Getenv("MULTI_SCHEMA"))
	if e != nil {
		t.Fatal(e)
	}
	w, e := repair17.OpenManaged(ctx, os.Getenv("MULTI_PATH"))
	if e != nil {
		t.Fatal(e)
	}
	ref := repair19.Ref{ID: w.RunID, Owner: "owner-A", Epoch: 1}
	if _, e = s.PrepareBatch(ctx, ref, "multi-1", multiPatches(t, w, stage == "verified_fail")); e != nil {
		t.Fatal(e)
	}
	publish := func() {
		if root := os.Getenv("PORTABLE_ROOT"); root != "" {
			store, e := artifactstore.NewDisk(root)
			if e != nil {
				t.Fatal(e)
			}
			s.Artifacts = store
			if _, e = s.PublishPortable(ctx, ref); e != nil {
				t.Fatal(e)
			}
		}
	}
	// Partial-rename crash resumes from the last committed prepared artifact;
	// publication cannot take a second Run lock inside Apply's rename hook.
	if stage == "partial_rename" || stage == "verified_after_prepared_artifact" {
		publish()
	}
	ready := func() {
		if stage != "partial_rename" && stage != "verified_after_prepared_artifact" {
			publish()
		}
		fmt.Println("MULTI_READY", stage)
		select {}
	}
	if stage == "prepared" {
		ready()
	}
	hook := func() {}
	if stage == "partial_rename" {
		hook = ready
	}
	if _, e = s.ApplyOrRecover(ctx, ref, "multi-1", hook); e != nil {
		t.Fatal(e)
	}
	if stage == "applied" {
		ready()
	}
	result, e := s.Verify(ctx, ref, "multi-1", 10*time.Second)
	if e != nil || result.Passed != (stage != "verified_fail") {
		t.Fatal(result, e)
	}
	ready()
}

type noMultiModel struct{}

func (noMultiModel) Suggest(context.Context, repair18.Request) (repair18.Suggestion, error) {
	return repair18.Suggestion{}, errors.New("recovery must not call model")
}
func TestMultiFileRealSIGKILLRecoveryMatrix(t *testing.T) {
	for _, stage := range []string{"prepared", "partial_rename", "applied", "verified_pass", "verified_fail"} {
		t.Run(stage, func(t *testing.T) {
			id := "multi-crash-" + stage
			s, w, source, job, counter := multiLab(t, id)
			ctx := context.Background()
			beforePatches := multiPatches(t, w, stage == "verified_fail")
			binary, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			child := exec.Command(binary, "-test.run=^TestMultiFileCrashProcess$", "-test.timeout=40s")
			child.Env = append(os.Environ(), "DEEPSEEK_API_KEY=", "MULTI_STAGE="+stage, "MULTI_SCHEMA="+s.schema, "MULTI_PATH="+w.Path)
			pipe, e := child.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			var stderr bytes.Buffer
			child.Stderr = &stderr
			if e = child.Start(); e != nil {
				t.Fatal(e)
			}
			ready := make(chan bool, 1)
			go func() {
				scan := bufio.NewScanner(pipe)
				for scan.Scan() {
					if scan.Text() == "MULTI_READY "+stage {
						ready <- true
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					_ = child.Process.Kill()
					_ = child.Wait()
					t.Fatal("child did not reach boundary", stderr.String())
				}
			case <-time.After(30 * time.Second):
				_ = child.Process.Kill()
				_ = child.Wait()
				t.Fatal("boundary timeout", stderr.String())
			}
			if e = child.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			e = child.Wait()
			var exit *exec.ExitError
			if !errors.As(e, &exit) {
				t.Fatal("not killed", e)
			}
			status, ok := exit.Sys().(syscall.WaitStatus)
			if !ok || status.Signal() != syscall.SIGKILL {
				t.Fatal("not SIGKILL", e)
			}
			expectedState := stage
			if stage == "partial_rename" {
				expectedState = "prepared"
				for i, c := range beforePatches {
					sha, e := repair17.Inspect(ctx, w.Path, c.Path)
					if e != nil {
						t.Fatal(e)
					}
					if (i == 0 && sha == c.ExpectedSHA256) || (i == 1 && sha != c.ExpectedSHA256) {
						t.Fatal("partial rename did not leave exact mixed state")
					}
				}
			}
			before, _ := os.ReadFile(counter)
			next := nextPatchOwner(t, s, id)
			if next.Epoch != job.Lease.Epoch+1 {
				t.Fatal("not real epoch takeover")
			}
			old := repair19.Ref{ID: id, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch}
			if _, e = s.ApplyOrRecover(ctx, old, "multi-1", nil); !errors.Is(e, repair19.ErrLeaseLost) {
				t.Fatal("old owner apply", e)
			}
			if _, e = s.Verify(ctx, old, "multi-1", time.Second); !errors.Is(e, repair19.ErrLeaseLost) {
				t.Fatal("old owner verify", e)
			}
			pending, e := s.Recoverable(ctx, next)
			if e != nil || len(pending) != 1 || pending[0].State != expectedState {
				t.Fatal(pending, e)
			}
			result, e := s.ApplyOrRecover(ctx, next, "multi-1", nil)
			if e != nil {
				t.Fatal(e)
			}
			expectedAction := repair19.RecoverApplied
			if expectedState == "prepared" {
				expectedAction = repair19.ApplyNow
			}
			if result.Action != expectedAction || len(result.Files) != 2 {
				t.Fatal(result)
			}
			verdict, e := s.Verify(ctx, next, "multi-1", 10*time.Second)
			if e != nil || verdict.Passed != (stage != "verified_fail") {
				t.Fatal(verdict, e)
			}
			after, _ := os.ReadFile(counter)
			if strings.HasPrefix(stage, "verified_") && string(before) != string(after) {
				t.Fatal("cached batch verdict re-executed tests")
			}
			used, max, e := s.Summary(ctx, id)
			if e != nil || used != 1 || max != 2 {
				t.Fatal("batch consumed multiple rounds", used, max, e)
			}
			if verdict.Passed {
				job.Lease.Owner = next.Owner
				job.Lease.Epoch = next.Epoch
				h := durable.CodingHandler{Pool: s.Pool, PatchStore: s.Store, PatchSchema: s.schema, WorktreeBase: filepath.Dir(w.Path), File: "a.go", Files: []string{"a.go", "b.go"}, MaxRounds: 2, TestTimeout: 10 * time.Second, Proposer: noMultiModel{}}
				snapshot, e := h.Execute(ctx, job)
				if e != nil || snapshot.Status != durable.Succeeded || snapshot.OutcomeVerdict != "tests_passed" {
					t.Fatal(snapshot, e)
				}
			}
			raw, e := exec.Command("git", "-C", source, "status", "--porcelain").Output()
			if e != nil || len(strings.TrimSpace(string(raw))) != 0 {
				t.Fatal("source polluted", string(raw), e)
			}
		})
	}
}
func TestMultiFilePreflightTamperAndBudget(t *testing.T) {
	s, w, _, job, _ := multiLab(t, "multi-preflight")
	ctx := context.Background()
	ref := repair19.Ref{ID: w.RunID, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch}
	patches := multiPatches(t, w, false)
	invalid := append([]repair17.Patch(nil), patches...)
	invalid[1].Old = "absent fragment"
	if _, e := s.PrepareBatch(ctx, ref, "invalid", invalid); !errors.Is(e, repair19.ErrConflict) {
		t.Fatal("invalid second member", e)
	}
	aliases := append([]repair17.Patch(nil), patches...)
	aliases[1].Path = "./a.go"
	if _, e := s.PrepareBatch(ctx, ref, "alias", aliases); e == nil {
		t.Fatal("alias accepted")
	}
	for _, c := range patches {
		sha, e := repair17.Inspect(ctx, w.Path, c.Path)
		if e != nil || sha != c.ExpectedSHA256 {
			t.Fatal("preflight wrote files", e)
		}
	}
	if used, _, _ := s.Summary(ctx, w.RunID); used != 0 {
		t.Fatal("rejected preflight spent round")
	}
	if _, e := s.PrepareBatch(ctx, ref, "good", patches); e != nil {
		t.Fatal(e)
	}
	if _, e := s.PrepareBatch(ctx, ref, "good", patches); e != nil {
		t.Fatal("idempotent manifest", e)
	}
	if _, e := s.ApplyOrRecover(ctx, ref, "good", nil); e != nil {
		t.Fatal(e)
	}
	if v, e := s.Verify(ctx, ref, "good", 10*time.Second); e != nil || !v.Passed {
		t.Fatal(v, e)
	}
	path := filepath.Join(w.Path, "b.go")
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, append(data, []byte("\n// tampered second file\n")...), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Verify(ctx, ref, "good", 10*time.Second); !errors.Is(e, repair19.ErrConflict) {
		t.Fatal("second member tamper accepted", e)
	}
}
