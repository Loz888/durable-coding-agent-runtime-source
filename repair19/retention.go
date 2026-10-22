package repair19

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime-app/repair17"
	"time"
)

type Retirement struct {
	RunID    string `json:"run_id"`
	Eligible bool   `json:"eligible"`
	Removed  bool   `json:"removed"`
	Reason   string `json:"reason"`
}

// RetireWorktree is opt-in, dry-run friendly and never deletes model/Run audit rows.
// Immutable artifact blobs are retained, including unknown model-call evidence.
func (s *Store) RetireWorktree(ctx context.Context, id, base string, retention time.Duration, dry bool) (out Retirement, err error) {
	out.RunID = id
	if retention < time.Hour || retention > 365*24*time.Hour || !filepath.IsAbs(base) {
		return out, errors.New("canonical base and retention 1h..365d required")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	var eligible bool
	e = tx.QueryRow(ctx, fmt.Sprintf(`SELECT status IN ('succeeded','failed') AND finished_at<clock_timestamp()-($2::bigint*interval '1 microsecond') AND (lease_until IS NULL OR lease_until<=clock_timestamp()) FROM %s.agent_runs WHERE run_id=$1 FOR UPDATE`, s.queue), id, retention.Microseconds()).Scan(&eligible)
	if e != nil {
		return out, e
	}
	if !eligible {
		out.Reason = "active, recent, or lease not expired"
		return out, nil
	}
	var path, identity, intent string
	var retired bool
	e = tx.QueryRow(ctx, fmt.Sprintf(`SELECT worktree_path,worktree_identity,worktree_retired_at IS NOT NULL,retirement_identity FROM %s WHERE run_id=$1 FOR UPDATE`, s.tbl("repair_runs")), id).Scan(&path, &identity, &retired, &intent)
	if e != nil {
		return out, e
	}
	if retired {
		out.Reason = "already retired"
		return out, nil
	}
	var shared bool
	if e = tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE worktree_path=$1 AND run_id<>$2 AND worktree_retired_at IS NULL)`, s.tbl("repair_runs")), path, id).Scan(&shared); e != nil {
		return out, e
	}
	if shared {
		return out, ErrConflict
	}
	rel, e := filepath.Rel(base, path)
	canonical, ce := filepath.EvalSymlinks(base)
	if e != nil || ce != nil || canonical != base || rel == "." || !filepath.IsLocal(rel) {
		return out, ErrConflict
	}
	if intent != "" && intent != identity {
		return out, ErrConflict
	}
	// A previously committed retirement intent makes a crash after filesystem
	// removal reconcilable, without treating an arbitrary missing path as ours.
	if _, statErr := os.Lstat(path); os.IsNotExist(statErr) && intent == identity && identity != "" {
		out.Eligible = true
		out.Reason = "reconcile committed retirement intent; path already absent"
		if dry {
			return out, nil
		}
		_, e = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET worktree_retired_at=clock_timestamp() WHERE run_id=$1`, s.tbl("repair_runs")), id)
		if e != nil {
			return out, e
		}
		out.Removed = true
		return out, tx.Commit(ctx)
	}
	w, e := repair17.OpenManaged(ctx, path)
	if e != nil || w.Identity != identity || w.RunID != id {
		return out, ErrConflict
	}
	out.Eligible = true
	out.Reason = "terminal, aged, unleased, uniquely bound managed worktree"
	if dry {
		return out, nil
	}
	if intent == "" {
		_, e = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET retirement_identity=$2 WHERE run_id=$1`, s.tbl("repair_runs")), id, identity)
		if e != nil {
			return out, e
		}
		if e = tx.Commit(ctx); e != nil {
			return out, e
		}
		return s.RetireWorktree(ctx, id, base, retention, false)
	}
	guard := func(c context.Context) error {
		var ok bool
		e := tx.QueryRow(c, fmt.Sprintf(`SELECT status IN ('succeeded','failed') AND (lease_until IS NULL OR lease_until<=clock_timestamp()) FROM %s.agent_runs WHERE run_id=$1`, s.queue), id).Scan(&ok)
		if e != nil {
			return e
		}
		if !ok {
			return ErrLeaseLost
		}
		return nil
	}
	if e = repair17.RemoveManaged(ctx, path, identity, base, guard); e != nil {
		return out, e
	}
	if _, e = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET worktree_retired_at=clock_timestamp() WHERE run_id=$1`, s.tbl("repair_runs")), id); e != nil {
		return out, e
	}
	out.Removed = true
	return out, tx.Commit(ctx)
}
