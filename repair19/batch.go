package repair19

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/jackc/pgx/v5"
	"runtime-app/repair17"
)

const MaxFileChanges = 8

type FileChange struct {
	File   string `json:"file"`
	Before string `json:"before_sha"`
	After  string `json:"after_sha"`
	Old    string `json:"old"`
	New    string `json:"new"`
}

// loadChanges works with legacy single-file schemas as well as migration 013.
func (s *Store) loadChanges(ctx context.Context, tx pgx.Tx, id, patch string) ([]FileChange, string, error) {
	var header FileChange
	var state string
	var raw []byte
	err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT file_path,before_sha,after_sha,old_fragment,new_fragment,state,COALESCE(to_jsonb(p)->'file_changes','[]'::jsonb) FROM %s AS p WHERE run_id=$1 AND patch_id=$2 FOR UPDATE`, s.tbl("patch_ops")), id, patch).Scan(&header.File, &header.Before, &header.After, &header.Old, &header.New, &state, &raw)
	if err != nil {
		return nil, "", err
	}
	var changes []FileChange
	if json.Unmarshal(raw, &changes) != nil {
		return nil, "", ErrConflict
	}
	if len(changes) == 0 {
		return []FileChange{header}, state, nil
	}
	if len(changes) < 2 || len(changes) > MaxFileChanges || changes[0] != header {
		return nil, "", ErrConflict
	}
	for i, c := range changes {
		if c.File == "" || c.Before == "" || c.After == "" || c.Old == "" || c.Old == c.New || (i > 0 && changes[i-1].File >= c.File) {
			return nil, "", ErrConflict
		}
	}
	return changes, state, nil
}
func (s *Store) membersAtAfter(ctx context.Context, tx pgx.Tx, id, patch, path string) ([]FileChange, error) {
	changes, _, err := s.loadChanges(ctx, tx, id, patch)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
		sha, e := repair17.Inspect(ctx, path, c.File)
		if e != nil || sha != c.After {
			return nil, ErrConflict
		}
	}
	return changes, nil
}
func (s *Store) previousAfter(ctx context.Context, tx pgx.Tx, id, file string) (string, bool, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT file_path,after_sha,COALESCE(to_jsonb(p)->'file_changes','[]'::jsonb) FROM %s AS p WHERE run_id=$1 ORDER BY round_number DESC,created_at DESC,patch_id DESC`, s.tbl("patch_ops")), id)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	for rows.Next() {
		var path, after string
		var raw []byte
		if err = rows.Scan(&path, &after, &raw); err != nil {
			return "", false, err
		}
		var changes []FileChange
		if json.Unmarshal(raw, &changes) != nil {
			return "", false, ErrConflict
		}
		if len(changes) == 0 {
			if path == file {
				return after, true, nil
			}
		} else {
			for _, c := range changes {
				if c.File == file {
					return c.After, true, nil
				}
			}
		}
	}
	return "", false, rows.Err()
}

// PrepareBatch commits one immutable manifest and consumes exactly one repair round.
// Existing single-file callers retain their original API and storage representation.
func (s *Store) PrepareBatch(ctx context.Context, r Ref, id string, patches []repair17.Patch) (Intent, error) {
	if len(patches) == 1 {
		return s.Prepare(ctx, r, id, patches[0])
	}
	if len(patches) < 2 || len(patches) > MaxFileChanges || !patchName.MatchString(id) {
		return Intent{}, ErrConflict
	}
	patches = append([]repair17.Patch(nil), patches...)
	sort.Slice(patches, func(i, j int) bool { return patches[i].Path < patches[j].Path })
	for i, c := range patches {
		if !filepath.IsLocal(c.Path) || filepath.Clean(c.Path) != c.Path || c.Old == "" || c.Old == c.New || len(c.Old) > 16<<10 || len(c.New) > 16<<10 || (i > 0 && patches[i-1].Path == c.Path) {
			return Intent{}, ErrConflict
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Intent{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.lockLease(ctx, tx, r); err != nil {
		return Intent{}, err
	}
	path, used, maximum, err := s.binding(ctx, tx, r.ID)
	if err != nil {
		return Intent{}, err
	}
	old, state, err := s.loadChanges(ctx, tx, r.ID, id)
	if err == nil {
		if len(old) != len(patches) {
			return Intent{}, ErrConflict
		}
		for i, c := range patches {
			if old[i].File != c.Path || old[i].Before != c.ExpectedSHA256 || old[i].Old != c.Old || old[i].New != c.New {
				return Intent{}, ErrConflict
			}
		}
		if err = s.lockLease(ctx, tx, r); err != nil {
			return Intent{}, err
		}
		return Intent{ID: id, File: old[0].File, Before: old[0].Before, After: old[0].After, State: state, Files: old}, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Intent{}, err
	}
	if used >= maximum {
		return Intent{}, ErrBudget
	}
	var blocked bool
	if err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE run_id=$1 AND state IN ('prepared','applied','verified_pass'))`, s.tbl("patch_ops")), r.ID).Scan(&blocked); err != nil {
		return Intent{}, err
	}
	if blocked {
		return Intent{}, ErrConflict
	}
	changes := make([]FileChange, 0, len(patches))
	for _, c := range patches {
		prior, exists, e := s.previousAfter(ctx, tx, r.ID, c.Path)
		if e != nil {
			return Intent{}, e
		}
		if exists && prior != c.ExpectedSHA256 {
			return Intent{}, ErrConflict
		}
		sha, e := repair17.Inspect(ctx, path, c.Path)
		if e != nil {
			return Intent{}, e
		}
		if sha != c.ExpectedSHA256 {
			return Intent{}, ErrConflict
		}
		raw, e := os.ReadFile(filepath.Join(path, c.Path))
		if e != nil {
			return Intent{}, e
		}
		after, e := Plan(raw, sha, c.Old, c.New)
		if e != nil {
			return Intent{}, e
		}
		changes = append(changes, FileChange{File: c.Path, Before: sha, After: after, Old: c.Old, New: c.New})
	}
	raw, err := json.Marshal(changes)
	if err != nil {
		return Intent{}, err
	}
	first := changes[0]
	if err = s.lockLease(ctx, tx, r); err != nil {
		return Intent{}, err
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s(run_id,patch_id,file_path,before_sha,after_sha,old_fragment,new_fragment,prepared_epoch,round_number,file_changes) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, s.tbl("patch_ops")), r.ID, id, first.File, first.Before, first.After, first.Old, first.New, r.Epoch, used+1, raw)
	if err != nil {
		return Intent{}, err
	}
	if _, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET rounds_used=rounds_used+1 WHERE run_id=$1`, s.tbl("repair_runs")), r.ID); err != nil {
		return Intent{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Intent{}, err
	}
	return Intent{ID: id, File: first.File, Before: first.Before, After: first.After, State: "prepared", Files: changes}, nil
}

// Every member is validated before the first write. SQL rollback cannot undo
// completed Renames: a new valid owner finishes before/after mixtures by hashes.
func (s *Store) applyBatch(ctx context.Context, r Ref, id string, afterRename func()) (Outcome, error) {
	var out Outcome
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = s.lockLease(ctx, tx, r); err != nil {
		return out, err
	}
	path, _, _, err := s.binding(ctx, tx, r.ID)
	if err != nil {
		return out, err
	}
	changes, state, err := s.loadChanges(ctx, tx, r.ID, id)
	if err != nil {
		return out, err
	}
	decisions := make([]Decision, len(changes))
	anyApply := false
	for i, c := range changes {
		sha, e := repair17.Inspect(ctx, path, c.File)
		if e != nil {
			return out, e
		}
		decisions[i] = Decide(sha, c.Before, c.After)
		if decisions[i] == Conflict || (decisions[i] == ApplyNow && state != "prepared") {
			return out, ErrConflict
		}
		anyApply = anyApply || decisions[i] == ApplyNow
	}
	for i, c := range changes {
		if decisions[i] != ApplyNow {
			continue
		}
		_, err = repair17.ApplyGuarded(ctx, path, repair17.Patch{Path: c.File, ExpectedSHA256: c.Before, Old: c.Old, New: c.New}, func(c context.Context) error { return s.lockLease(c, tx, r) })
		if err != nil {
			return out, err
		}
		if afterRename != nil {
			afterRename()
		}
	}
	if _, err = s.membersAtAfter(ctx, tx, r.ID, id, path); err != nil {
		return out, err
	}
	diff, err := repair17.Diff(ctx, path)
	if err != nil {
		return out, err
	}
	if len(diff) > 128<<10 {
		return out, errors.New("diff too large")
	}
	if err = s.lockLease(ctx, tx, r); err != nil {
		return out, err
	}
	_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE %s SET state=CASE WHEN state='prepared' THEN 'applied' ELSE state END,result_epoch=$3,result_owner=$4,diff=$5,updated_at=clock_timestamp() WHERE run_id=$1 AND patch_id=$2`, s.tbl("patch_ops")), r.ID, id, r.Epoch, r.Owner, diff)
	if err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	first := changes[0]
	if state == "prepared" {
		state = "applied"
	}
	action := RecoverApplied
	if anyApply {
		action = ApplyNow
	}
	out = Outcome{Intent: Intent{ID: id, File: first.File, Before: first.Before, After: first.After, State: state}, Action: action, Diff: diff}
	if len(changes) > 1 {
		out.Files = changes
	}
	return out, nil
}
