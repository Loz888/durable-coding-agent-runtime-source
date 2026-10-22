package integration20

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/repair17"
	"runtime-app/repair19"
	"strings"
	"testing"
	"time"
)

func TestInterviewVerificationCacheTracksTestInput(t *testing.T) {
	s, w, _, claim := integratedPatchLab(t, "interview-cache")
	ctx := context.Background()
	ref := repair19.Ref{ID: w.RunID, Owner: claim.Lease.Owner, Epoch: claim.Lease.Epoch}
	if _, err := s.Prepare(ctx, ref, "p1", patchFor(t, w)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyOrRecover(ctx, ref, "p1", nil); err != nil {
		t.Fatal(err)
	}
	result, err := s.Verify(ctx, ref, "p1", 10*time.Second)
	if err != nil || !result.Passed {
		t.Fatalf("initial %+v %v", result, err)
	}
	if err = os.WriteFile(filepath.Join(w.Path, "math_test.go"), []byte(`package example
import "testing"
func TestChanged(t *testing.T){t.Fatal("changed test")}
`), 0600); err != nil {
		t.Fatal(err)
	}
	result, err = s.Verify(ctx, ref, "p1", 10*time.Second)
	if err != nil || result.Passed || result.ExitCode == 0 {
		t.Fatalf("changed input was not actually retested: %+v %v", result, err)
	}
}

func TestInterviewVerificationDigestInvalidations(t *testing.T) {
	for _, kind := range []string{"same", "test", "source", "mod", "policy", "toolchain", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			s, w, _, claim := integratedPatchLab(t, "input-"+kind)
			ctx := context.Background()
			ref := repair19.Ref{ID: w.RunID, Owner: claim.Lease.Owner, Epoch: claim.Lease.Epoch}
			calls := 0
			s.VerificationPolicy = "synthetic-test-verifier-v1"
			s.VerifyGo = func(_ context.Context, path string, _ time.Duration) (repair17.TestResult, error) {
				calls++
				if path == w.Path {
					t.Error("verifier executed mutable original candidate")
				}
				return repair17.TestResult{Passed: true, ExitCode: 0}, nil
			}
			if _, e := s.Prepare(ctx, ref, "p1", patchFor(t, w)); e != nil {
				t.Fatal(e)
			}
			if _, e := s.ApplyOrRecover(ctx, ref, "p1", nil); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Verify(ctx, ref, "p1", time.Second); e != nil {
				t.Fatal(e)
			}
			var old string
			if e := s.Pool.QueryRow(ctx, fmt.Sprintf("SELECT verification_input_digest FROM %s WHERE run_id=$1", s.table("patch_ops")), w.RunID).Scan(&old); e != nil || old == "" {
				t.Fatal(old, e)
			}
			switch kind {
			case "test":
				f := filepath.Join(w.Path, "math_test.go")
				b, _ := os.ReadFile(f)
				if e := os.WriteFile(f, append(b, []byte("\n// changed test\n")...), 0600); e != nil {
					t.Fatal(e)
				}
			case "source":
				if e := os.WriteFile(filepath.Join(w.Path, "other.go"), []byte("package example\nconst Extra = 1\n"), 0600); e != nil {
					t.Fatal(e)
				}
			case "mod":
				f := filepath.Join(w.Path, "go.mod")
				b, _ := os.ReadFile(f)
				if e := os.WriteFile(f, append(b, []byte("\n// changed dependency configuration\n")...), 0600); e != nil {
					t.Fatal(e)
				}
			case "policy":
				s.VerificationPolicy = "synthetic-test-verifier-v2"
			case "toolchain":
				actual, e := exec.LookPath("go")
				if e != nil {
					t.Fatal(e)
				}
				dir := t.TempDir()
				script := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'go version synthetic-toolchain linux/amd64'; else exec " + actual + " \"$@\"; fi\n"
				if e = os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0700); e != nil {
					t.Fatal(e)
				}
				t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
			case "legacy":
				if _, e := s.Pool.Exec(ctx, fmt.Sprintf("UPDATE %s SET verification_input_digest='' WHERE run_id=$1", s.table("patch_ops")), w.RunID); e != nil {
					t.Fatal(e)
				}
			}
			result, e := s.Verify(ctx, ref, "p1", time.Second)
			if e != nil || !result.Passed {
				t.Fatalf("verify %+v %v", result, e)
			}
			want := 2
			if kind == "same" {
				want = 1
			}
			if calls != want {
				t.Fatalf("verifier calls %d want %d", calls, want)
			}
			var now string
			if e = s.Pool.QueryRow(ctx, fmt.Sprintf("SELECT verification_input_digest FROM %s WHERE run_id=$1", s.table("patch_ops")), w.RunID).Scan(&now); e != nil || now == "" {
				t.Fatal(now, e)
			}
			if kind != "same" && kind != "legacy" && old == now {
				t.Fatal("changed verification identity not reflected")
			}
			// The migration is additive/idempotent and does not re-stamp evidence.
			migration, e := os.ReadFile("../../migrations/010_verification_input.sql")
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 2; i++ {
				if _, e = s.Pool.Exec(ctx, strings.ReplaceAll(string(migration), "day19", s.schema)); e != nil {
					t.Fatal(e)
				}
			}
			var after string
			if e = s.Pool.QueryRow(ctx, fmt.Sprintf("SELECT verification_input_digest FROM %s WHERE run_id=$1", s.table("patch_ops")), w.RunID).Scan(&after); e != nil || after != now {
				t.Fatal("migration changed evidence", e)
			}
		})
	}
}

func TestInterviewVerificationChangesDuringExecutionCannotPersistPass(t *testing.T) {
	for _, where := range []string{"candidate", "snapshot"} {
		t.Run(where, func(t *testing.T) {
			s, w, _, claim := integratedPatchLab(t, "during-"+where)
			ctx := context.Background()
			ref := repair19.Ref{ID: w.RunID, Owner: claim.Lease.Owner, Epoch: claim.Lease.Epoch}
			s.VerificationPolicy = "synthetic-mutation-verifier-v1"
			s.VerifyGo = func(_ context.Context, path string, _ time.Duration) (repair17.TestResult, error) {
				target := path
				if where == "candidate" {
					target = w.Path
				}
				e := os.WriteFile(filepath.Join(target, "other.go"), []byte("package example\nconst Other=1\n"), 0600)
				return repair17.TestResult{Passed: true, ExitCode: 0}, e
			}
			if _, e := s.Prepare(ctx, ref, "p1", patchFor(t, w)); e != nil {
				t.Fatal(e)
			}
			if _, e := s.ApplyOrRecover(ctx, ref, "p1", nil); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Verify(ctx, ref, "p1", time.Second); e == nil {
				t.Fatal("input mutation accepted")
			}
			var state, digest string
			if e := s.Pool.QueryRow(ctx, fmt.Sprintf("SELECT state,verification_input_digest FROM %s WHERE run_id=$1", s.table("patch_ops")), w.RunID).Scan(&state, &digest); e != nil || state != "applied" || digest != "" {
				t.Fatalf("published changed inputs %s %s %v", state, digest, e)
			}
			bad := ref
			bad.Epoch++
			if _, e := s.Verify(ctx, bad, "p1", time.Second); !errors.Is(e, repair19.ErrLeaseLost) {
				t.Fatalf("wrong Epoch accepted %v", e)
			}
		})
	}
}
