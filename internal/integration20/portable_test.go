package integration20

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/artifactstore"
	"runtime-app/durable"
	"runtime-app/repair17"
	"runtime-app/repair19"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestPortableRealClaimAfterSIGKILL(t *testing.T) {
	for _, stage := range []string{"partial_rename", "verified_pass", "verified_after_prepared_artifact"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			s, w, source, job, _ := multiLab(t, "portable-"+stage)
			storeRoot := filepath.Join(t.TempDir(), "artifact")
			disk, e := artifactstore.NewDisk(storeRoot)
			if e != nil {
				t.Fatal(e)
			}
			s.Artifacts = disk
			binary, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			child := exec.Command(binary, "-test.run=^TestMultiFileCrashProcess$", "-test.timeout=45s")
			child.Env = append(os.Environ(), "DEEPSEEK_API_KEY=", "MULTI_STAGE="+stage, "MULTI_SCHEMA="+s.schema, "MULTI_PATH="+w.Path, "PORTABLE_ROOT="+storeRoot)
			pipe, e := child.StdoutPipe()
			if e != nil {
				t.Fatal(e)
			}
			var logs bytes.Buffer
			child.Stderr = &logs
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
					child.Process.Kill()
					child.Wait()
					t.Fatal("missing boundary", logs.String())
				}
			case <-time.After(35 * time.Second):
				child.Process.Kill()
				child.Wait()
				t.Fatal("timeout", logs.String())
			}
			if e = child.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			e = child.Wait()
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatal("not SIGKILL", e)
			}
			clone := filepath.Join(t.TempDir(), "independent-clone")
			runGit(t, source, "clone", "--no-hardlinks", "--quiet", source, clone)
			// Simulate the old disk being unavailable. B is not allowed to read its worktree.
			if e = os.Rename(w.Path, w.Path+"-unavailable"); e != nil {
				t.Fatal(e)
			}
			next := nextPatchOwner(t, s, w.RunID)
			old := repair19.Ref{ID: w.RunID, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch}
			if _, e = s.RestorePublished(ctx, old, clone, filepath.Join(t.TempDir(), "stale"), 2); !errors.Is(e, repair19.ErrLeaseLost) {
				t.Fatal("old owner restored", e)
			}
			base := filepath.Join(t.TempDir(), "host-B")
			job.Lease.Owner = next.Owner
			job.Lease.Epoch = next.Epoch
			h := durable.CodingHandler{Pool: s.Pool, PatchStore: s.Store, PatchSchema: s.schema, WorktreeBase: base, File: "a.go", Files: []string{"a.go", "b.go"}, MaxRounds: 2, TestTimeout: 10 * time.Second, Proposer: noMultiModel{}, ResolveRepo: func(context.Context, durable.ClaimedRun) (string, error) { return clone, nil }}
			var snap durable.Snapshot
			if stage == "verified_after_prepared_artifact" {
				// Crash the new owner after revalidation/publication but before
				// final checkpoint, then evidence-only finalization uses repo-map.
				h.BeforeCheckpoint = func() { panic("portable checkpoint boundary") }
				func() {
					defer func() {
						if recover() == nil {
							t.Error("missing boundary panic")
						}
					}()
					_, e = h.Execute(ctx, job)
					if e != nil {
						t.Error(e)
					}
				}()
				if _, e = s.Pool.Exec(ctx, "UPDATE day12.agent_runs SET max_attempts=lease_epoch,lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", w.RunID); e != nil {
					t.Fatal(e)
				}
				n, e := h.FinalizeExhausted(ctx, durable.Queue{Pool: s.Pool}, 1)
				if e != nil || n != 1 {
					t.Fatal("mapped finalization", n, e)
				}
				snap, e = (durable.PostgresStore{Pool: s.Pool}).Load(ctx, w.RunID)
			} else {
				snap, e = h.Execute(ctx, job)
			}
			if e != nil || snap.Status != durable.Succeeded {
				t.Fatal(snap, e)
			}
			restored, e := repair17.OpenManaged(ctx, filepath.Join(base, w.RunID))
			if e != nil || restored.Source != clone || restored.Identity == w.Identity || restored.Commit != w.Commit {
				t.Fatal("physical binding not migrated", restored, e)
			}
			var generation, rounds int
			var digest string
			if e = s.Pool.QueryRow(ctx, "SELECT binding_generation,rounds_used FROM "+s.table("repair_runs")+" WHERE run_id=$1", w.RunID).Scan(&generation, &rounds); e != nil || generation != 1 || rounds != 1 {
				t.Fatal(generation, rounds, e)
			}
			if e = s.Pool.QueryRow(ctx, "SELECT verification_input_digest FROM "+s.table("patch_ops")+" WHERE run_id=$1", w.RunID).Scan(&digest); e != nil || digest == "" {
				t.Fatal("new identity not reverified", e)
			}
			for _, repo := range []string{source, clone} {
				out, e := exec.Command("git", "-C", repo, "status", "--porcelain").Output()
				if e != nil || strings.TrimSpace(string(out)) != "" {
					t.Fatal("original/clone modified", e, string(out))
				}
			}
		})
	}
}
func TestPortableRefusesWrongCommitAndCorruptArtifact(t *testing.T) {
	ctx := context.Background()
	s, w, _, job, _ := multiLab(t, "portable-bad")
	artifactRoot := filepath.Join(t.TempDir(), "artifact")
	d, e := artifactstore.NewDisk(artifactRoot)
	if e != nil {
		t.Fatal(e)
	}
	s.Artifacts = d
	ref := repair19.Ref{ID: w.RunID, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch}
	if _, e = s.PrepareBatch(ctx, ref, "repair-1", multiPatches(t, w, false)); e != nil {
		t.Fatal(e)
	}
	key, e := s.PublishPortable(ctx, ref)
	if e != nil {
		t.Fatal(e)
	}
	next := nextPatchOwner(t, s, w.RunID)
	// Corrupt the private file through its known root: Get must refuse before any rebind.
	raw, e := d.Get(ctx, key)
	if e != nil {
		t.Fatal(e)
	}
	m, e := repair17.ParsePortable(raw)
	if e != nil {
		t.Fatal(e)
	}
	m.Files[0].SHA = strings.Repeat("0", 64)
	invalid, _ := json.Marshal(m)
	if _, e = repair17.ParsePortable(invalid); e == nil {
		t.Fatal("manifest hash mismatch accepted")
	}
	original := append([]byte(nil), raw...)
	if e = os.WriteFile(filepath.Join(artifactRoot, key), []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.RestorePublished(ctx, next, w.Source, filepath.Join(t.TempDir(), "corrupt-host"), 2); e == nil {
		t.Fatal("corrupt content-addressed blob accepted")
	}
	if e = os.WriteFile(filepath.Join(artifactRoot, key), original, 0600); e != nil {
		t.Fatal(e)
	}
	clone := filepath.Join(t.TempDir(), "wrong-commit")
	runGit(t, w.Source, "clone", "--no-hardlinks", "--quiet", w.Source, clone)
	if e = os.WriteFile(filepath.Join(clone, "extra.txt"), []byte("different baseline"), 0600); e != nil {
		t.Fatal(e)
	}
	runGit(t, clone, "add", ".")
	runGit(t, clone, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "different baseline")
	if _, e = s.RestorePublished(ctx, next, clone, filepath.Join(t.TempDir(), "wrong-host"), 2); e == nil {
		t.Fatal("wrong baseline accepted")
	}
	raw[0] = '!'
	root := t.TempDir()
	bad, e := artifactstore.NewDisk(filepath.Join(root, "bad"))
	if e != nil {
		t.Fatal(e)
	}
	_, e = bad.Put(ctx, raw)
	if e != nil {
		t.Fatal(e)
	}
	s.Artifacts = bad
	if _, e = s.RestorePublished(ctx, next, w.Source, filepath.Join(root, "B"), 2); e == nil {
		t.Fatal("missing artifact accepted")
	}
	b, e := s.BoundWorktree(ctx, next)
	if e != nil || b.Path != w.Path {
		t.Fatal("failed restore changed binding", e)
	}
}

func TestPortableRepeatTakeoverBeforeRepublish(t *testing.T) {
	ctx := context.Background()
	s, w, source, job, _ := multiLab(t, "portable-repeat")
	disk, e := artifactstore.NewDisk(filepath.Join(t.TempDir(), "artifacts"))
	if e != nil {
		t.Fatal(e)
	}
	s.Artifacts = disk
	ref := repair19.Ref{ID: w.RunID, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch}
	if _, e = s.PrepareBatch(ctx, ref, "repair-1", multiPatches(t, w, false)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.PublishPortable(ctx, ref); e != nil {
		t.Fatal(e)
	}
	b := filepath.Join(t.TempDir(), "clone-b")
	runGit(t, source, "clone", "--no-hardlinks", "--quiet", source, b)
	next := nextPatchOwner(t, s, w.RunID)
	if _, e = s.RestorePublished(ctx, next, b, filepath.Join(t.TempDir(), "host-b"), 2); e != nil {
		t.Fatal(e)
	}
	// B dies before publishing a new artifact: C can still reconstruct from the
	// earlier immutable artifact plus persisted intent, under real Claim epoch 3.
	if _, e = s.Pool.Exec(ctx, "UPDATE day12.agent_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", w.RunID); e != nil {
		t.Fatal(e)
	}
	jobs, e := claimPatchRun(t, durable.Queue{Pool: s.Pool}, w.RunID, "owner-C", 3)
	if e != nil || len(jobs) != 1 {
		t.Fatal(jobs, e)
	}
	c := filepath.Join(t.TempDir(), "clone-c")
	runGit(t, source, "clone", "--no-hardlinks", "--quiet", source, c)
	r := repair19.Ref{ID: w.RunID, Owner: jobs[0].Lease.Owner, Epoch: jobs[0].Lease.Epoch}
	restored, e := s.RestorePublished(ctx, r, c, filepath.Join(t.TempDir(), "host-c"), 2)
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range []string{"a.go", "b.go"} {
		sha, e := repair17.Inspect(ctx, restored.Path, f)
		if e != nil || sha == "" {
			t.Fatal(e)
		}
	}
	used, max, e := s.Summary(ctx, w.RunID)
	if e != nil || used != 1 || max != 2 {
		t.Fatal("repeated takeover reset budget", used, max, e)
	}
}
