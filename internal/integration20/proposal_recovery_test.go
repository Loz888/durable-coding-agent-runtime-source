package integration20

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/durable"
	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
	"strings"
	"syscall"
	"testing"
	"time"
)

type observedProposer struct {
	Counter string
	Fail    bool
}

func (observedProposer) PolicyIdentity() string { return "observed-frozen-fixture-v1" }
func (p observedProposer) Suggest(context.Context, repair18.Request) (repair18.Suggestion, error) {
	f, e := os.OpenFile(p.Counter, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return repair18.Suggestion{}, e
	}
	_, e = f.WriteString("call\n")
	_ = f.Close()
	if e != nil {
		return repair18.Suggestion{}, e
	}
	if p.Fail {
		return repair18.Suggestion{}, errors.New("synthetic unknown provider result")
	}
	return repair18.Suggestion{Changes: []repair18.Change{{File: "a.go", Old: "return a-b", New: "return a+b"}, {File: "b.go", Old: "return a+b", New: "return a-b"}}, Reason: "frozen synthetic response", UsageKnown: true, PromptTokens: 9, CompletionTokens: 4}, nil
}
func proposalRequest(t *testing.T, w repair17.Workspace) repair18.Request {
	t.Helper()
	ctx := context.Background()
	digest, e := repair17.VerificationDigest(ctx, w.Path, "logical-model-context-v1", 10*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	in := repair18.Request{Round: 1, SourceCommit: w.Commit, ContextDigest: digest, File: "a.go", TestOutput: "synthetic baseline failure"}
	for _, file := range []string{"a.go", "b.go"} {
		sha, e := repair17.Inspect(ctx, w.Path, file)
		if e != nil {
			t.Fatal(e)
		}
		raw, e := os.ReadFile(filepath.Join(w.Path, file))
		if e != nil {
			t.Fatal(e)
		}
		in.Files = append(in.Files, repair18.SourceFile{File: file, SHA256: sha, Source: string(raw)})
	}
	in.SourceHash = in.Files[0].SHA256
	in.Source = in.Files[0].Source
	return in
}
func TestProposalCrashProcess(t *testing.T) {
	stage := os.Getenv("PROPOSAL_STAGE")
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
	s, e := repair19.NewStore(pool, "day12", os.Getenv("PROPOSAL_SCHEMA"))
	if e != nil {
		t.Fatal(e)
	}
	w, e := repair17.OpenManaged(ctx, os.Getenv("PROPOSAL_PATH"))
	if e != nil {
		t.Fatal(e)
	}
	s.ProposalHook = func(point string) {
		if point == stage {
			fmt.Println("PROPOSAL_READY", point)
			select {}
		}
	}
	_, e = s.Propose(ctx, repair19.Ref{ID: w.RunID, Owner: "owner-A", Epoch: 1}, proposalRequest(t, w), observedProposer{Counter: os.Getenv("PROPOSAL_COUNTER")}, 10*time.Second)
	if e != nil {
		t.Fatal(e)
	}
}
func TestProposalRealSIGKILLResponseWindows(t *testing.T) {
	for _, stage := range []string{"intent_committed", "response_received", "response_committed"} {
		t.Run(stage, func(t *testing.T) {
			id := "proposal-crash-" + stage
			s, w, source, job, _ := multiLab(t, id)
			counter := filepath.Join(t.TempDir(), "physical-calls")
			binary, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			child := exec.Command(binary, "-test.run=^TestProposalCrashProcess$", "-test.timeout=30s")
			child.Env = append(os.Environ(), "DEEPSEEK_API_KEY=", "PROPOSAL_STAGE="+stage, "PROPOSAL_PATH="+w.Path, "PROPOSAL_SCHEMA="+s.schema, "PROPOSAL_COUNTER="+counter)
			stdout, e := child.StdoutPipe()
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
				scan := bufio.NewScanner(stdout)
				for scan.Scan() {
					if scan.Text() == "PROPOSAL_READY "+stage {
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
					t.Fatal("missing crash point", stderr.String())
				}
			case <-time.After(20 * time.Second):
				_ = child.Process.Kill()
				_ = child.Wait()
				t.Fatal("crash point timeout", stderr.String())
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
			before, _ := os.ReadFile(counter)
			expectedCalls := 1
			if stage == "intent_committed" {
				expectedCalls = 0
			}
			if strings.Count(string(before), "call\n") != expectedCalls {
				t.Fatal("unexpected physical dispatch count", string(before))
			}
			next := nextPatchOwner(t, s, id)
			if next.Epoch != job.Lease.Epoch+1 {
				t.Fatal("claim not advanced")
			}
			in := proposalRequest(t, w)
			in.TestOutput = "same captured test inputs, different wall-clock log text"
			old := repair19.Ref{ID: id, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch}
			model := observedProposer{Counter: counter}
			if _, e = s.Propose(context.Background(), old, in, model, 10*time.Second); !errors.Is(e, repair19.ErrLeaseLost) {
				t.Fatal("old model owner", e)
			}
			if stage == "response_committed" {
				cached, e := s.Propose(context.Background(), next, in, model, 10*time.Second)
				if e != nil || !cached.UsageKnown || cached.PromptTokens != 9 || cached.CompletionTokens != 4 {
					t.Fatal("cached response/usage lost", cached, e)
				}
				job.Lease.Owner = next.Owner
				job.Lease.Epoch = next.Epoch
				h := durable.CodingHandler{Pool: s.Pool, PatchStore: s.Store, PatchSchema: s.schema, WorktreeBase: filepath.Dir(w.Path), File: "a.go", Files: []string{"a.go", "b.go"}, MaxRounds: 2, TestTimeout: 10 * time.Second, Proposer: model, ResponseCache: true}
				snapshot, e := h.Execute(context.Background(), job)
				if e != nil || snapshot.Status != durable.Succeeded {
					t.Fatal("cached response not consumed by real handler", snapshot, e)
				}
			} else {
				if _, e = s.Propose(context.Background(), next, in, model, 10*time.Second); !errors.Is(e, repair19.ErrProposalUnknown) {
					t.Fatal("unknown result automatically redispatched", e)
				}
			}
			after, _ := os.ReadFile(counter)
			if string(before) != string(after) {
				t.Fatal("recovery called model again")
			}
			raw, e := exec.Command("git", "-C", source, "status", "--porcelain").Output()
			if e != nil || strings.TrimSpace(string(raw)) != "" {
				t.Fatal("original changed", string(raw), e)
			}
		})
	}
}
func TestProposalContextTamperAndExplicitRetryBound(t *testing.T) {
	s, w, _, job, _ := multiLab(t, "proposal-explicit-retry")
	ctx := context.Background()
	ref := repair19.Ref{ID: w.RunID, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch}
	counter := filepath.Join(t.TempDir(), "calls")
	in := proposalRequest(t, w)
	model := observedProposer{Counter: counter, Fail: true}
	for attempt := 1; attempt <= 3; attempt++ {
		if _, e := s.Propose(ctx, ref, in, model, 10*time.Second); e == nil {
			t.Fatal("expected observed provider error")
		}
		if _, e := s.Propose(ctx, ref, in, model, 10*time.Second); !errors.Is(e, repair19.ErrProposalUnknown) {
			t.Fatal("automatic repeat accepted", e)
		}
		e := s.AuthorizeProposalRetry(ctx, ref, 1)
		if attempt < 3 && e != nil {
			t.Fatal(e)
		}
		if attempt == 3 && !errors.Is(e, repair19.ErrBudget) {
			t.Fatal("physical retry bound bypass", e)
		}
	}
	raw, e := os.ReadFile(counter)
	if e != nil || strings.Count(string(raw), "call\n") != 3 {
		t.Fatal("physical count", string(raw), e)
	}
	var rows int
	if e = s.Pool.QueryRow(ctx, "SELECT count(*) FROM "+s.table("model_calls")+" WHERE run_id=$1", w.RunID).Scan(&rows); e != nil || rows != 3 {
		t.Fatal("unknown history discarded", rows, e)
	}
	if used, _, e := s.Summary(ctx, w.RunID); e != nil || used != 0 {
		t.Fatal("model attempt spent patch round", used, e)
	}
	if e = os.WriteFile(filepath.Join(w.Path, "b.go"), []byte("package fixture\nfunc Sub(a,b int)int{return 5}\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Propose(ctx, ref, in, model, 10*time.Second); !errors.Is(e, repair19.ErrConflict) {
		t.Fatal("stale complete-context digest accepted", e)
	}
}
