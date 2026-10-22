package repair19

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
	"runtime-app/repair17"
	"time"
)

// PublishPortable snapshots a stable live binding and publishes its hash only
// after rechecking the same whole input. Upload never holds the Run row lock.
func (s *Store) PublishPortable(ctx context.Context, r Ref) (string, error) {
	if s.Artifacts == nil {
		return "", errors.New("portable artifact store not configured")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = s.lockLease(ctx, tx, r); e != nil {
		return "", e
	}
	path, _, _, e := s.binding(ctx, tx, r.ID)
	if e != nil {
		return "", e
	}
	var count int
	if e = tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE run_id=$1`, s.tbl("patch_ops")), r.ID).Scan(&count); e != nil {
		return "", e
	}
	if count == 0 {
		return "", errors.New("portable publication requires durable patch intent")
	}
	before, e := repair17.VerificationDigest(ctx, path, s.inputPolicy("portable-v1"), time.Minute)
	if e != nil {
		return "", e
	}
	if e = tx.Commit(ctx); e != nil {
		return "", e
	}
	raw, e := repair17.MakePortable(ctx, path)
	if e != nil {
		return "", e
	}
	key, e := s.Artifacts.Put(ctx, raw)
	if e != nil {
		return "", e
	}
	tx, e = s.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = s.lockLease(ctx, tx, r); e != nil {
		return "", e
	}
	current, _, _, e := s.binding(ctx, tx, r.ID)
	if e != nil {
		return "", e
	}
	after, e := repair17.VerificationDigest(ctx, current, s.inputPolicy("portable-v1"), time.Minute)
	if e != nil || path != current || before != after {
		return "", ErrConflict
	}
	_, e = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET artifact_sha=$2,artifact_generation=binding_generation,artifact_epoch=$3 WHERE run_id=$1`, s.tbl("repair_runs")), r.ID, key, r.Epoch)
	if e != nil {
		return "", e
	}
	return key, tx.Commit(ctx)
}

// RestorePublished is the only permitted physical rebind. It needs a newer real
// claim, an immutable artifact pointer, and a clean same-commit local clone.
// Cached test verdicts are invalidated: the new physical identity must retest.
func (s *Store) RestorePublished(ctx context.Context, r Ref, source, base string, max int) (repair17.Workspace, error) {
	var zero repair17.Workspace
	if s.Artifacts == nil {
		return zero, errors.New("portable artifact store not configured")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback(ctx)
	if e = s.lockLease(ctx, tx, r); e != nil {
		return zero, e
	}
	var oldPath, commit, key string
	var generation, artifactGeneration, epoch int64
	var budget int
	e = tx.QueryRow(ctx, fmt.Sprintf(`SELECT worktree_path,source_commit,max_rounds,binding_generation,artifact_sha,artifact_generation,artifact_epoch FROM %s WHERE run_id=$1 FOR UPDATE`, s.tbl("repair_runs")), r.ID).Scan(&oldPath, &commit, &budget, &generation, &key, &artifactGeneration, &epoch)
	if e != nil {
		return zero, e
	}
	if key == "" || artifactGeneration > generation || r.Epoch <= epoch || budget != max {
		return zero, ErrConflict
	}
	if e = tx.Commit(ctx); e != nil {
		return zero, e
	}
	raw, e := s.Artifacts.Get(ctx, key)
	if e != nil {
		return zero, e
	}
	m, e := repair17.ParsePortable(raw)
	if e != nil || m.RunID != r.ID || m.Commit != commit {
		return zero, ErrConflict
	}
	base, e = filepath.Abs(base)
	if e != nil {
		return zero, e
	}
	source, e = filepath.Abs(source)
	if e != nil {
		return zero, e
	}
	target := filepath.Join(base, r.ID)
	if target == oldPath {
		return zero, errors.New("portable restore requires a distinct physical path")
	}
	var w repair17.Workspace
	if _, e = os.Lstat(target); os.IsNotExist(e) {
		w, e = repair17.Prepare(ctx, source, base, r.ID)
	} else if e == nil {
		w, e = repair17.OpenManaged(ctx, target)
	}
	if e != nil {
		return zero, e
	}
	if w.RunID != r.ID || w.Source != source || w.Commit != commit {
		return zero, ErrConflict
	}
	tx, e = s.Pool.Begin(ctx)
	if e != nil {
		return zero, e
	}
	defer tx.Rollback(ctx)
	if e = s.lockLease(ctx, tx, r); e != nil {
		return zero, e
	}
	var unchanged bool
	e = tx.QueryRow(ctx, fmt.Sprintf(`SELECT worktree_path=$2 AND binding_generation=$3 AND artifact_sha=$4 AND artifact_epoch=$5 FROM %s WHERE run_id=$1 FOR UPDATE`, s.tbl("repair_runs")), r.ID, oldPath, generation, key, epoch).Scan(&unchanged)
	if e != nil {
		return zero, e
	}
	if !unchanged {
		return zero, ErrConflict
	}
	guard := func(c context.Context) error { return s.lockLease(c, tx, r) }
	m, e = s.reconcilePortable(ctx, tx, r, m)
	if e != nil {
		return zero, e
	}
	if e = repair17.RestorePortable(ctx, w.Path, m, guard); e != nil {
		return zero, e
	}
	if e = repair17.MarkDurable(ctx, w.Path); e != nil {
		return zero, e
	}
	_, e = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET worktree_path=$2,worktree_identity=$3,binding_generation=binding_generation+1 WHERE run_id=$1`, s.tbl("repair_runs")), r.ID, w.Path, w.Identity)
	if e != nil {
		return zero, e
	}
	_, e = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET verification_input_digest='' WHERE run_id=$1`, s.tbl("patch_ops")), r.ID)
	if e != nil {
		return zero, e
	}
	if e = s.markCompletion(ctx, tx, r, false); e != nil {
		return zero, e
	}
	if e = guard(ctx); e != nil {
		return zero, e
	}
	return w, tx.Commit(ctx)
}

// A published artifact can precede a later committed patch/result. Reconstruct
// only the persisted before/after chain; never invent a patch or spend a round.
func (s *Store) reconcilePortable(ctx context.Context, tx pgx.Tx, r Ref, m repair17.Portable) (repair17.Portable, error) {
	rows, e := tx.Query(ctx, fmt.Sprintf(`SELECT patch_id FROM %s WHERE run_id=$1 ORDER BY round_number,created_at,patch_id`, s.tbl("patch_ops")), r.ID)
	if e != nil {
		return m, e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return m, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return m, e
	}
	chains := map[string][]FileChange{}
	for _, id := range ids {
		changes, state, e := s.loadChanges(ctx, tx, r.ID, id)
		if e != nil {
			return m, e
		}
		if state != "prepared" && state != "applied" && state != "verified_pass" && state != "verified_fail" {
			return m, ErrConflict
		}
		for _, c := range changes {
			chains[c.File] = append(chains[c.File], c)
		}
	}
	members := map[string]int{}
	for i, f := range m.Files {
		members[f.Path] = i
	}
	for file, chain := range chains {
		index, ok := members[file]
		if !ok {
			return m, ErrConflict
		}
		f := &m.Files[index]
		start := -1
		// Choose the latest matching known state. Byte-identical cycles need no replay.
		for i, c := range chain {
			if f.SHA == c.Before {
				start = i
			}
			if f.SHA == c.After {
				start = i + 1
			}
			if i > 0 && chain[i-1].After != c.Before {
				return m, ErrConflict
			}
		}
		if start < 0 {
			return m, ErrConflict
		}
		for _, c := range chain[start:] {
			after, e := Plan(f.Data, c.Before, c.Old, c.New)
			if e != nil || after != c.After {
				return m, ErrConflict
			}
			f.Data = bytes.Replace(f.Data, []byte(c.Old), []byte(c.New), 1)
			f.SHA = c.After
		}
	}
	return m, nil
}
