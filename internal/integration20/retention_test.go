package integration20

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWorktreeRetentionRefusesActiveAndPreservesSource(t *testing.T) {
	s, w, source, _, _ := multiLab(t, "retention-managed")
	ctx := context.Background()
	base := filepath.Dir(w.Path)
	out, e := s.RetireWorktree(ctx, w.RunID, base, time.Hour, false)
	if e != nil || out.Removed || out.Eligible {
		t.Fatal("active deleted", out, e)
	}
	if _, e = s.Pool.Exec(ctx, "UPDATE day12.agent_runs SET status='failed',finished_at=clock_timestamp()-interval '2 hours',lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", w.RunID); e != nil {
		t.Fatal(e)
	}
	out, e = s.RetireWorktree(ctx, w.RunID, base, time.Hour, true)
	if e != nil || !out.Eligible || out.Removed {
		t.Fatal("dry run", out, e)
	}
	if _, e = os.Stat(w.Path); e != nil {
		t.Fatal("dry run removed files")
	}
	if _, e = s.RetireWorktree(ctx, w.RunID, source, time.Hour, false); e == nil {
		t.Fatal("wrong base accepted")
	}
	out, e = s.RetireWorktree(ctx, w.RunID, base, time.Hour, false)
	if e != nil || !out.Removed {
		t.Fatal(out, e)
	}
	if _, e = os.Stat(w.Path); !os.IsNotExist(e) {
		t.Fatal("worktree remains")
	}
	if _, e = os.Stat(filepath.Join(source, "a.go")); e != nil {
		t.Fatal("source removed")
	}
	out, e = s.RetireWorktree(ctx, w.RunID, base, time.Hour, false)
	if e != nil || out.Removed || out.Reason != "already retired" {
		t.Fatal("idempotent retirement", out, e)
	}
}

func TestWorktreeRetirementReconcilesCommittedIntent(t *testing.T) {
	s, w, _, _, _ := multiLab(t, "retention-reconcile")
	ctx := context.Background()
	base := filepath.Dir(w.Path)
	if _, e := s.Pool.Exec(ctx, "UPDATE day12.agent_runs SET status='failed',finished_at=clock_timestamp()-interval '2 hours',lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", w.RunID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Pool.Exec(ctx, "UPDATE "+s.table("repair_runs")+" SET retirement_identity=worktree_identity WHERE run_id=$1", w.RunID); e != nil {
		t.Fatal(e)
	}
	// Inject the filesystem-complete/SQL-incomplete window, never an arbitrary directory.
	runGit(t, w.Source, "worktree", "remove", "--force", w.Path)
	out, e := s.RetireWorktree(ctx, w.RunID, base, time.Hour, false)
	if e != nil || !out.Removed {
		t.Fatal("committed intent not reconciled", out, e)
	}
}
