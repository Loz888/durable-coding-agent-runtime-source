package repair19

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/repair17"
)

var sqlName = regexp.MustCompile(
	`^[a-z][a-z0-9_]*$`,
)

var patchName = regexp.MustCompile(
	`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`,
)

// Exact claimed execution identity.
type Ref struct {
	ID    string
	Owner string
	Epoch int64
}

type Store struct {
	VerifyGo func(context.Context, string, time.Duration) (repair17.TestResult, error)
	Pool     *pgxpool.Pool
	queue    string
	patch    string
}

type Intent struct {
	ID     string `json:"patch_id"`
	File   string `json:"file"`
	Before string `json:"before_sha"`
	After  string `json:"after_sha"`
	State  string `json:"state"`
}

type Outcome struct {
	Intent
	Action Decision `json:"action"`
	Diff   string   `json:"diff"`
}

func NewStore(
	pool *pgxpool.Pool,
	queueSchema string,
	patchSchema string,
) (*Store, error) {

	if pool == nil ||
		!sqlName.MatchString(queueSchema) ||
		!sqlName.MatchString(patchSchema) {

		return nil, errors.New(
			"invalid pool/schema",
		)
	}

	return &Store{
		Pool:  pool,
		queue: queueSchema,
		patch: patchSchema,
	}, nil
}

func (s *Store) tbl(name string) string {
	return s.patch + "." + name
}

// Serializes with an existing ClaimBatch that
// locks the SAME agent_runs row.
func (s *Store) lockLease(
	ctx context.Context,
	tx pgx.Tx,
	r Ref,
) error {

	if r.ID == "" ||
		r.Owner == "" ||
		r.Epoch < 1 {
		return ErrLeaseLost
	}

	query := fmt.Sprintf(
		`SELECT run_id
         FROM %s.agent_runs
         WHERE run_id=$1
           AND lease_owner=$2
           AND lease_epoch=$3
           AND status='running'
           AND lease_until > clock_timestamp()
         FOR UPDATE`,
		s.queue,
	)

	var id string

	err := tx.QueryRow(
		ctx,
		query,
		r.ID,
		r.Owner,
		r.Epoch,
	).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		return ErrLeaseLost
	}

	if err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s.agent_runs
 WHERE run_id=$1 AND lease_owner=$2 AND lease_epoch=$3
 AND status='running' AND lease_until>clock_timestamp())`, s.queue), r.ID, r.Owner, r.Epoch).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return ErrLeaseLost
	}
	return nil
}

// Bind one logical RunID to one managed Worktree.
func (s *Store) Register(
	ctx context.Context,
	r Ref,
	worktree string,
	maxRounds int,
) error {

	if maxRounds < 1 || maxRounds > 5 {
		return errors.New(
			"maxRounds must be 1..5",
		)
	}

	w, err := repair17.OpenManaged(
		ctx,
		worktree,
	)
	if err != nil {
		return err
	}

	if w.RunID != r.ID {
		return fmt.Errorf(
			"%w: RunID/worktree mismatch",
			ErrConflict,
		)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if err = s.lockLease(ctx, tx, r); err != nil {
		return err
	}

	_, err = tx.Exec(
		ctx,
		fmt.Sprintf(
			`INSERT INTO %s
             (run_id,worktree_path,source_commit,max_rounds,worktree_identity)
             VALUES($1,$2,$3,$4,$5)
             ON CONFLICT (run_id) DO NOTHING`,
			s.tbl("repair_runs"),
		),
		r.ID,
		w.Path,
		w.Commit,
		maxRounds,
		w.Identity,
	)
	if err != nil {
		return err
	}

	var storedPath, commit, identity string
	var budget int

	err = tx.QueryRow(
		ctx,
		fmt.Sprintf(
			`SELECT worktree_path,source_commit,max_rounds,worktree_identity
             FROM %s
             WHERE run_id=$1
             FOR UPDATE`,
			s.tbl("repair_runs"),
		),
		r.ID,
	).Scan(
		&storedPath,
		&commit,
		&budget,
		&identity,
	)
	if err != nil {
		return err
	}

	if storedPath != w.Path ||
		commit != w.Commit ||
		budget != maxRounds || identity == "" || identity != w.Identity {

		return fmt.Errorf(
			"%w: binding/budget cannot change",
			ErrConflict,
		)
	}

	if err = repair17.MarkDurable(ctx, w.Path); err != nil {
		return err
	}
	if err = s.lockLease(ctx, tx, r); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) binding(
	ctx context.Context,
	tx pgx.Tx,
	runID string,
) (string, int, int, error) {

	var path, commit, identity string
	var rounds, max int

	err := tx.QueryRow(
		ctx,
		fmt.Sprintf(
			`SELECT worktree_path,rounds_used,max_rounds,source_commit,worktree_identity
             FROM %s
             WHERE run_id=$1 FOR UPDATE`,
			s.tbl("repair_runs"),
		),
		runID,
	).Scan(
		&path,
		&rounds,
		&max,
		&commit, &identity,
	)

	if err != nil {
		return path, rounds, max, err
	}
	w, err := repair17.OpenManaged(ctx, path)
	if err != nil {
		return "", 0, 0, fmt.Errorf("%w: invalid bound worktree: %v", ErrConflict, err)
	}
	if w.RunID != runID || w.Commit != commit || identity == "" || w.Identity != identity {
		return "", 0, 0, fmt.Errorf("%w: worktree binding identity/commit mismatch; legacy bindings require audit", ErrConflict)
	}
	return path, rounds, max, nil
}

// Persist Patch Intent BEFORE file modification.
// Repeating an identical patch_id does not
// consume another repair round.
func (s *Store) Prepare(
	ctx context.Context,
	r Ref,
	patchID string,
	p repair17.Patch,
) (Intent, error) {

	var out Intent

	if !patchName.MatchString(patchID) {
		return out, errors.New(
			"invalid patch_id",
		)
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)

	if err = s.lockLease(ctx, tx, r); err != nil {
		return out, err
	}

	path, used, max, err := s.binding(
		ctx, tx, r.ID,
	)
	if err != nil {
		return out, err
	}

	w, err := repair17.OpenManaged(
		ctx, path,
	)
	if err != nil {
		return out, err
	}

	if w.RunID != r.ID {
		return out, ErrConflict
	}

	var old, newStr string

	query := fmt.Sprintf(
		`SELECT file_path,before_sha,after_sha,
                state,old_fragment,new_fragment
         FROM %s
         WHERE run_id=$1 AND patch_id=$2
         FOR UPDATE`,
		s.tbl("patch_ops"),
	)

	err = tx.QueryRow(
		ctx, query, r.ID, patchID,
	).Scan(
		&out.File,
		&out.Before,
		&out.After,
		&out.State,
		&old,
		&newStr,
	)

	// The same logical Patch already exists.
	if err == nil {

		if out.File != p.Path ||
			out.Before != p.ExpectedSHA256 ||
			old != p.Old ||
			newStr != p.New {

			return Intent{}, ErrConflict
		}

		out.ID = patchID

		return out, tx.Commit(ctx)
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}

	// This is a NEW Patch.
	if used >= max {
		return out, ErrBudget
	}

	var blocked bool
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE run_id=$1
 AND state IN ('prepared','applied','verified_pass'))`, s.tbl("patch_ops")), r.ID).Scan(&blocked); err != nil {
		return out, err
	}
	if blocked {
		return out, fmt.Errorf("%w: recover the existing patch before preparing another", ErrConflict)
	}
	// A failed verdict permits a new intent only on the known post-patch bytes.
	var previousAfter string
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT after_sha FROM %s WHERE run_id=$1 AND file_path=$2
 ORDER BY round_number DESC,created_at DESC,patch_id DESC LIMIT 1`, s.tbl("patch_ops")), r.ID, p.Path).Scan(&previousAfter)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if err == nil && previousAfter != p.ExpectedSHA256 {
		return out, ErrConflict
	}
	sha, err := repair17.Inspect(
		ctx, path, p.Path,
	)
	if err != nil {
		return out, err
	}

	if sha != p.ExpectedSHA256 {
		return out, ErrConflict
	}

	source, err := os.ReadFile(
		filepath.Join(path, p.Path),
	)
	if err != nil {
		return out, err
	}

	after, err := Plan(
		source,
		sha,
		p.Old,
		p.New,
	)
	if err != nil {
		return out, err
	}

	_, err = tx.Exec(
		ctx,
		fmt.Sprintf(
			`INSERT INTO %s
             (run_id,patch_id,file_path,
              before_sha,after_sha,
              old_fragment,new_fragment,prepared_epoch,round_number)
             VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			s.tbl("patch_ops"),
		),
		r.ID,
		patchID,
		p.Path,
		sha,
		after,
		p.Old,
		p.New,
		r.Epoch,
		used+1,
	)
	if err != nil {
		return out, err
	}

	_, err = tx.Exec(
		ctx,
		fmt.Sprintf(
			`UPDATE %s
             SET rounds_used=rounds_used+1
             WHERE run_id=$1`,
			s.tbl("repair_runs"),
		),
		r.ID,
	)
	if err != nil {
		return out, err
	}

	if err = tx.Commit(ctx); err != nil {
		return out, err
	}

	return Intent{
		ID:     patchID,
		File:   p.Path,
		Before: sha,
		After:  after,
		State:  "prepared",
	}, nil
}

// Apply, or recognize an already-applied Patch.
// Keeps the Run row locked during the short
// filesystem modification.
func (s *Store) ApplyOrRecover(
	ctx context.Context,
	r Ref,
	patchID string,
	afterRename func(),
) (Outcome, error) {

	var o Outcome

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return o, err
	}
	defer tx.Rollback(ctx)

	if err = s.lockLease(ctx, tx, r); err != nil {
		return o, err
	}

	worktree, _, _, err := s.binding(
		ctx, tx, r.ID,
	)
	if err != nil {
		return o, err
	}

	var old, newStr, state string

	q := fmt.Sprintf(
		`SELECT file_path,before_sha,after_sha,
                state,old_fragment,new_fragment
         FROM %s
         WHERE run_id=$1 AND patch_id=$2
         FOR UPDATE`,
		s.tbl("patch_ops"),
	)

	err = tx.QueryRow(
		ctx, q, r.ID, patchID,
	).Scan(
		&o.File,
		&o.Before,
		&o.After,
		&state,
		&old,
		&newStr,
	)
	if err != nil {
		return o, err
	}

	w, err := repair17.OpenManaged(
		ctx, worktree,
	)
	if err != nil {
		return o, err
	}

	if w.RunID != r.ID {
		return o, ErrConflict
	}

	current, err := repair17.Inspect(
		ctx,
		worktree,
		o.File,
	)
	if err != nil {
		return o, err
	}

	action := Decide(
		current,
		o.Before,
		o.After,
	)

	if action == Conflict {
		return o, ErrConflict
	}

	if action == ApplyNow {

		if state != "prepared" {
			return o, ErrConflict
		}

		_, err = repair17.ApplyGuarded(
			ctx,
			worktree,
			repair17.Patch{
				Path:           o.File,
				ExpectedSHA256: o.Before,
				Old:            old,
				New:            newStr,
			},
			func(c context.Context) error { return s.lockLease(c, tx, r) },
		)
		if err != nil {
			return o, err
		}

		// Test-only fault injection point:
		// the file is already modified,
		// but DB Result has not been committed.
		if afterRename != nil {
			afterRename()
		}
	}

	// If CurrentSHA == AfterSHA,
	// do not apply the Patch again.
	diff, err := repair17.Diff(
		ctx,
		worktree,
	)
	if err != nil {
		return o, err
	}

	if len(diff) > 128*1024 {
		return o, fmt.Errorf(
			"diff too large",
		)
	}

	if err = s.lockLease(ctx, tx, r); err != nil {
		return o, err
	}
	_, err = tx.Exec(
		ctx,
		fmt.Sprintf(
			`UPDATE %s
             SET
               state=CASE
                 WHEN state='prepared'
                 THEN 'applied'
                 ELSE state
               END,
               result_epoch=$3,
               result_owner=$4,
               diff=$5,
               updated_at=clock_timestamp()
             WHERE run_id=$1 AND patch_id=$2`,
			s.tbl("patch_ops"),
		),
		r.ID,
		patchID,
		r.Epoch,
		r.Owner,
		diff,
	)
	if err != nil {
		return o, err
	}

	if err = tx.Commit(ctx); err != nil {
		return o, err
	}

	o.ID = patchID
	o.State = state

	if state == "prepared" {
		o.State = "applied"
	}

	o.Action = action
	o.Diff = diff

	return o, nil
}

// Run tests outside the database transaction.
// Persist the verdict only with a valid Lease.
func (s *Store) Verify(
	ctx context.Context,
	r Ref,
	patchID string,
	timeout time.Duration,
) (repair17.TestResult, error) {

	var empty repair17.TestResult

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return empty, err
	}

	if err = s.lockLease(ctx, tx, r); err != nil {
		tx.Rollback(ctx)
		return empty, err
	}

	path, _, _, err := s.binding(
		ctx, tx, r.ID,
	)
	if err != nil {
		tx.Rollback(ctx)
		return empty, err
	}

	var file, after, state string

	err = tx.QueryRow(
		ctx,
		fmt.Sprintf(
			`SELECT file_path,after_sha,state,test_output,COALESCE(test_exit_code,-1)
             FROM %s
             WHERE run_id=$1 AND patch_id=$2`,
			s.tbl("patch_ops"),
		),
		r.ID,
		patchID,
	).Scan(
		&file,
		&after,
		&state,
		&empty.Output, &empty.ExitCode,
	)
	if err != nil {
		tx.Rollback(ctx)
		return empty, err
	}

	shaBefore, hashErr := repair17.Inspect(ctx, path, file)
	if hashErr != nil || shaBefore != after {
		tx.Rollback(ctx)
		return empty, ErrConflict
	}
	if state == "verified_pass" || state == "verified_fail" {
		empty.Passed = state == "verified_pass"
		if err = s.lockLease(ctx, tx, r); err != nil {
			tx.Rollback(ctx)
			return empty, err
		}
		return empty, tx.Commit(ctx)
	}
	if err = tx.Commit(ctx); err != nil {
		return empty, err
	}

	if state != "applied" {
		return empty, ErrConflict
	}

	// The DB transaction is no longer held.
	verify := s.VerifyGo
	if verify == nil {
		verify = repair17.VerifyGo
	}
	test, err := verify(
		ctx,
		path,
		timeout,
	)
	if err != nil {
		return test, err
	}

	tx, err = s.Pool.Begin(ctx)
	if err != nil {
		return test, err
	}
	defer tx.Rollback(ctx)

	// Recheck ownership after the test.
	if err = s.lockLease(ctx, tx, r); err != nil {
		return test, err
	}

	if _, _, _, err = s.binding(ctx, tx, r.ID); err != nil {
		return test, err
	}
	sha, err := repair17.Inspect(
		ctx, path, file,
	)
	if err != nil {
		return test, err
	}

	if sha != after {
		return test, ErrConflict
	}

	targetState := "verified_fail"

	if test.Passed {
		targetState = "verified_pass"
	}

	output := test.Output
	if len(output) > 32*1024 {
		output = output[:32*1024]
	}

	if err = s.lockLease(ctx, tx, r); err != nil {
		return test, err
	}
	tag, err := tx.Exec(
		ctx,
		fmt.Sprintf(
			`UPDATE %s
             SET
               state=$3,
               test_output=$4,
               test_exit_code=$5,
               updated_at=clock_timestamp()
             WHERE run_id=$1
               AND patch_id=$2
               AND state='applied'`,
			s.tbl("patch_ops"),
		),
		r.ID,
		patchID,
		targetState,
		output,
		test.ExitCode,
	)
	if err != nil {
		return test, err
	}

	if tag.RowsAffected() != 1 {
		return test, ErrConflict
	}

	return test, tx.Commit(ctx)
}

// Query the durable repair budget.
func (s *Store) Summary(
	ctx context.Context,
	runID string,
) (int, int, error) {

	var used, max int

	err := s.Pool.QueryRow(
		ctx,
		fmt.Sprintf(
			`SELECT rounds_used,max_rounds
             FROM %s WHERE run_id=$1`,
			s.tbl("repair_runs"),
		),
		runID,
	).Scan(
		&used,
		&max,
	)

	return used, max, err
}

// Find incomplete Patch Intents after takeover.
// Call before asking the model for another Patch.
func (s *Store) Pending(ctx context.Context, r Ref) ([]Intent, error) {
	return s.Recoverable(ctx, r)
}

// Recoverable returns persisted intents and verdicts for checkpoint reconciliation.
// Verified rows are evidence, not permission to generate a fresh patch.
func (s *Store) Recoverable(
	ctx context.Context,
	r Ref,
) ([]Intent, error) {

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if err = s.lockLease(ctx, tx, r); err != nil {
		return nil, err
	}

	if _, _, _, err = s.binding(ctx, tx, r.ID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(
		ctx,
		fmt.Sprintf(
			`SELECT patch_id,file_path,before_sha,
                    after_sha,state
             FROM %s
             WHERE run_id=$1
               AND state IN ('prepared','applied','verified_pass','verified_fail')
             ORDER BY round_number,created_at,patch_id`,
			s.tbl("patch_ops"),
		),
		r.ID,
	)
	if err != nil {
		return nil, err
	}

	var result []Intent

	for rows.Next() {
		var x Intent

		if err := rows.Scan(
			&x.ID,
			&x.File,
			&x.Before,
			&x.After,
			&x.State,
		); err != nil {
			rows.Close()
			return nil, err
		}

		result = append(result, x)
	}

	err = rows.Err()
	rows.Close()

	if err != nil {
		return nil, err
	}

	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}

	return result, nil
}

// BoundWorktree returns only the immutable, authorized workspace binding.
// Callers proposing a patch must not read another worktree supplied on the CLI.
func (s *Store) BoundWorktree(ctx context.Context, r Ref) (repair17.Workspace, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return repair17.Workspace{}, err
	}
	defer tx.Rollback(ctx)
	if err = s.lockLease(ctx, tx, r); err != nil {
		return repair17.Workspace{}, err
	}
	path, _, _, err := s.binding(ctx, tx, r.ID)
	if err != nil {
		return repair17.Workspace{}, err
	}
	w, err := repair17.OpenManaged(ctx, path)
	if err != nil {
		return w, err
	}
	if err = s.lockLease(ctx, tx, r); err != nil {
		return w, err
	}
	return w, tx.Commit(ctx)
}
