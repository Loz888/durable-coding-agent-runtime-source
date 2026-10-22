package repair19

import (
	"context"
	"errors"
	"fmt"
	"go.opentelemetry.io/otel/attribute"
	"os"
	"path/filepath"
	"regexp"
	"runtime-app/observability"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/artifactstore"
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
	InputPolicy string
	Artifacts   artifactstore.Store
	// Fault injection only; production leaves nil. No secrets are passed to hooks.
	ProposalHook func(stage string)

	// Required stable identity for custom verifiers (for example pinned container image/policy).
	VerificationPolicy string
	VerifyGo           func(context.Context, string, time.Duration) (repair17.TestResult, error)
	Pool               *pgxpool.Pool
	queue              string
	patch              string
}

type Intent struct {
	Files  []FileChange `json:"files,omitempty"`
	ID     string       `json:"patch_id"`
	File   string       `json:"file"`
	Before string       `json:"before_sha"`
	After  string       `json:"after_sha"`
	State  string       `json:"state"`
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
) (traced Intent, err error) {
	ctx, span := observability.StartOperation(ctx, "patch.prepare", attribute.String("run.id", r.ID), attribute.Int64("lease.epoch", r.Epoch))
	defer func() {
		span.SetAttributes(attribute.String("patch.state", traced.State))
		observability.FinishOperation(span, err)
	}()

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
	prior, exists, err := s.previousAfter(ctx, tx, r.ID, p.Path)
	if err != nil {
		return out, err
	}
	if exists && prior != p.ExpectedSHA256 {
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
) (traced Outcome, err error) {
	ctx, span := observability.StartOperation(ctx, "patch.apply", attribute.String("run.id", r.ID), attribute.Int64("lease.epoch", r.Epoch))
	defer func() { observability.FinishOperation(span, err) }()

	return s.applyBatch(ctx, r, patchID, afterRename)
}

// Run tests outside the database transaction.
// Persist the verdict only with a valid Lease.
func (s *Store) Verify(
	ctx context.Context,
	r Ref,
	patchID string,
	timeout time.Duration,
) (traced repair17.TestResult, err error) {
	ctx, span := observability.StartOperation(ctx, "patch.verify", attribute.String("run.id", r.ID), attribute.Int64("lease.epoch", r.Epoch))
	defer func() {
		span.SetAttributes(attribute.Bool("verification.passed", traced.Passed), attribute.Int("verification.exit_code", traced.ExitCode))
		observability.FinishOperation(span, err)
	}()

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

	var file, after, state, storedDigest string

	err = tx.QueryRow(
		ctx,
		fmt.Sprintf(
			`SELECT file_path,after_sha,state,test_output,COALESCE(test_exit_code,-1),verification_input_digest
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
		&empty.Output, &empty.ExitCode, &storedDigest,
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
	if _, err = s.membersAtAfter(ctx, tx, r.ID, patchID, path); err != nil {
		tx.Rollback(ctx)
		return empty, err
	}
	policy := s.VerificationPolicy
	if s.VerifyGo != nil && policy == "" {
		tx.Rollback(ctx)
		return empty, errors.New("custom verifier requires a stable VerificationPolicy")
	}
	if policy == "" {
		policy = "trusted-go-snapshot-v1"
	}
	policy = s.inputPolicy(policy)
	input, err := repair17.CaptureVerification(ctx, path, policy, timeout)
	if err != nil {
		tx.Rollback(ctx)
		return empty, err
	}
	defer input.Close()
	verified := state == "verified_pass" || state == "verified_fail"
	if verified && storedDigest != "" && storedDigest == input.Digest {
		empty.Passed = state == "verified_pass"
		if err = s.lockLease(ctx, tx, r); err != nil {
			tx.Rollback(ctx)
			return empty, err
		}
		return empty, tx.Commit(ctx)
	}
	if state != "applied" && !verified {
		tx.Rollback(ctx)
		return empty, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return empty, err
	}
	// A legacy or stale verdict is re-executed under current live authorization.
	// Never backfill a new digest onto an old verdict without running tests.
	var test repair17.TestResult
	if s.VerifyGo == nil {
		test, err = input.Verify(ctx, timeout)
	} else {
		test, err = s.VerifyGo(ctx, input.Path, timeout)
	}
	if err != nil {
		return test, err
	}
	if test.Passed && test.ExitCode != 0 {
		return test, errors.New("inconsistent verifier exit status")
	}
	if err = input.Unchanged(ctx); err != nil {
		return test, err
	}
	currentDigest, err := repair17.VerificationDigest(ctx, path, s.inputPolicyOnce(policy), timeout)
	if err != nil {
		return test, err
	}
	if currentDigest != input.Digest {
		return test, ErrConflict
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

	if _, err = s.membersAtAfter(ctx, tx, r.ID, patchID, path); err != nil {
		return test, err
	}
	currentDigest, err = repair17.VerificationDigest(ctx, path, s.inputPolicyOnce(policy), timeout)
	if err != nil {
		return test, err
	}
	if currentDigest != input.Digest {
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
               verification_input_digest=$6,
               updated_at=clock_timestamp()
             WHERE run_id=$1
               AND patch_id=$2
               AND state=$7 AND verification_input_digest=$8`,
			s.tbl("patch_ops"),
		),
		r.ID,
		patchID,
		targetState,
		output,
		test.ExitCode, input.Digest, state, storedDigest,
	)
	if err != nil {
		return test, err
	}

	if tag.RowsAffected() != 1 {
		return test, ErrConflict
	}

	if err = s.markCompletion(ctx, tx, r, test.Passed); err != nil {
		return test, err
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

// The marker only schedules evidence inspection; it is never sufficient proof.
func (s *Store) markCompletion(ctx context.Context, tx pgx.Tx, r Ref, passed bool) error {
	_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s.agent_runs AS r SET completion_pending=($2 AND COALESCE(to_jsonb(r)->>'provider','') LIKE 'coding:%%') WHERE run_id=$1`, s.queue), r.ID, passed)
	return err
}

type CompletionProof struct {
	Files                                       []string
	PatchID, File, Path, Source, Commit, Digest string
	Rounds, Max                                 int
}

// VerifiedCompletion is read-only proof inspection inside the caller's locked Run
// transaction. It grants neither a lease nor tool/model execution permission.
func (s *Store) VerifiedCompletion(ctx context.Context, tx pgx.Tx, id string, timeout time.Duration) (CompletionProof, error) {
	var proof CompletionProof
	path, used, max, err := s.binding(ctx, tx, id)
	if err != nil {
		return proof, err
	}
	var after, state, digest string
	var exit int
	var round int
	err = tx.QueryRow(ctx, fmt.Sprintf(`SELECT patch_id,file_path,after_sha,state,verification_input_digest,COALESCE(test_exit_code,-1),round_number FROM %s WHERE run_id=$1 ORDER BY round_number DESC LIMIT 1 FOR UPDATE`, s.tbl("patch_ops")), id).Scan(&proof.PatchID, &proof.File, &after, &state, &digest, &exit, &round)
	if err != nil {
		return proof, err
	}
	if state != "verified_pass" || digest == "" || exit != 0 || round != used || used < 1 || used > max {
		return proof, ErrConflict
	}
	sha, err := repair17.Inspect(ctx, path, proof.File)
	if err != nil || sha != after {
		return proof, ErrConflict
	}
	members, err := s.membersAtAfter(ctx, tx, id, proof.PatchID, path)
	if err != nil {
		return proof, err
	}
	for _, member := range members {
		proof.Files = append(proof.Files, member.File)
	}
	policy := s.VerificationPolicy
	if s.VerifyGo != nil && policy == "" {
		return proof, ErrConflict
	}
	if policy == "" {
		policy = "trusted-go-snapshot-v1"
	}
	current, err := repair17.VerificationDigest(ctx, path, s.inputPolicyOnce(policy), timeout)
	if err != nil || current != digest {
		return proof, ErrConflict
	}
	w, err := repair17.OpenManaged(ctx, path)
	if err != nil {
		return proof, ErrConflict
	}
	proof.Path = path
	proof.Source = w.Source
	proof.Commit = w.Commit
	proof.Digest = digest
	proof.Rounds = used
	proof.Max = max
	return proof, nil
}

func (s *Store) inputPolicy(policy string) string {
	if s.InputPolicy != "" {
		return s.InputPolicy + ":" + policy
	}
	return policy
}
func (s *Store) inputPolicyOnce(policy string) string {
	if s.InputPolicy != "" && !strings.HasPrefix(policy, s.InputPolicy+":") {
		return s.inputPolicy(policy)
	}
	return policy
}
