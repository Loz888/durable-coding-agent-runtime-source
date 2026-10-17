package repair19

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/repair17"
)

func runGit(
	t *testing.T,
	root string,
	args ...string,
) {
	t.Helper()

	argv := append(
		[]string{"-C", root},
		args...,
	)

	cmd := exec.Command("git", argv...)

	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf(
			"git %v: %v %s",
			args, err, b,
		)
	}
}

func makeWorktree(
	t *testing.T,
	runID string,
) (repair17.Workspace, string) {

	t.Helper()

	dir := t.TempDir()
	source := filepath.Join(dir, "source")

	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"go.mod": "module example.com/day19fixture\n\ngo 1.23\n",

		"math.go": "package example\n" +
			"func Divide(a,b int) int { return a * b }\n",

		"math_test.go": "package example\n" +
			"import \"testing\"\n" +
			"func TestDivide(t *testing.T) {" +
			"if Divide(12,3)!=4 {" +
			"t.Fatal(\"bad divide\")}}\n",
	}

	for name, content := range files {
		err := os.WriteFile(
			filepath.Join(source, name),
			[]byte(content),
			0644,
		)

		if err != nil {
			t.Fatal(err)
		}
	}

	runGit(t, source, "init", "-q")
	runGit(t, source, "add", ".")

	runGit(
		t,
		source,
		"-c", "user.name=Day19 Test",
		"-c", "user.email=day19@example.invalid",
		"commit", "-qm", "initial",
	)

	w, err := repair17.Prepare(
		context.Background(),
		source,
		filepath.Join(dir, "worktrees"),
		runID,
	)

	if err != nil {
		t.Fatal(err)
	}

	return w, source
}

// Only run on a dedicated TEST database.
// Each test uses an independent temporary SQL schema.
func newLab(
	t *testing.T,
) (*Store, func()) {

	t.Helper()

	dsn := os.Getenv("DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip(
			"Set DAY12_TEST_DATABASE_URL to a DEDICATED TEST DB",
		)
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	var dbname string

	err = pool.QueryRow(
		ctx,
		"SELECT current_database()",
	).Scan(&dbname)

	if err != nil ||
		dbname != "agent_runtime_day14_test" {

		pool.Close()

		t.Fatalf(
			"refusing non-test DB %q: %v",
			dbname,
			err,
		)
	}

	schema := fmt.Sprintf(
		"day19_t_%d",
		time.Now().UnixNano(),
	)

	if _, err := pool.Exec(
		ctx,
		"CREATE SCHEMA "+schema,
	); err != nil {
		pool.Close()
		t.Fatal(err)
	}

	cleanup := func() {
		_, _ = pool.Exec(
			context.Background(),
			"DROP SCHEMA IF EXISTS "+schema+" CASCADE",
		)
		pool.Close()
	}

	_, err = pool.Exec(
		ctx,
		`CREATE TABLE `+schema+`.agent_runs (
            run_id text PRIMARY KEY,
            lease_owner text,
            lease_epoch bigint NOT NULL,
            status text NOT NULL,
            lease_until timestamptz NOT NULL
        )`,
	)

	if err != nil {
		cleanup()
		t.Fatal(err)
	}

	stmts := []string{

		`CREATE TABLE ` + schema + `.repair_runs (
            run_id text PRIMARY KEY,
            worktree_path text NOT NULL,
            source_commit text NOT NULL,
            worktree_identity text NOT NULL,

            max_rounds integer NOT NULL
                CHECK (max_rounds BETWEEN 1 AND 5),

            rounds_used integer NOT NULL DEFAULT 0
                CHECK (
                    rounds_used >= 0
                    AND rounds_used <= max_rounds
                )
        )`,

		`CREATE TABLE ` + schema + `.patch_ops (
            run_id text NOT NULL
                REFERENCES ` + schema + `.repair_runs(run_id),

            patch_id text NOT NULL,
            file_path text NOT NULL,

            before_sha text NOT NULL,
            after_sha text NOT NULL,

            old_fragment text NOT NULL,
            new_fragment text NOT NULL,

            prepared_epoch bigint NOT NULL,
            round_number integer NOT NULL DEFAULT 0,
            state text NOT NULL DEFAULT 'prepared',

            result_epoch bigint,
            result_owner text,

            diff text NOT NULL DEFAULT '',
            test_output text NOT NULL DEFAULT '',
            test_exit_code integer,

            created_at timestamptz
                DEFAULT clock_timestamp(),

            updated_at timestamptz
                DEFAULT clock_timestamp(),

            PRIMARY KEY(run_id,patch_id)
        )`,
	}

	for _, sql := range stmts {
		if _, err := pool.Exec(
			ctx, sql,
		); err != nil {
			cleanup()
			t.Fatal(err)
		}
	}

	store, err := NewStore(
		pool, schema, schema,
	)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}

	return store, cleanup
}

func seed(
	t *testing.T,
	s *Store,
	id string,
) {
	t.Helper()

	_, err := s.Pool.Exec(
		context.Background(),
		fmt.Sprintf(
			`INSERT INTO %s.agent_runs
             (
                 run_id,
                 lease_owner,
                 lease_epoch,
                 status,
                 lease_until
             )
             VALUES(
                 $1,
                 'owner-A',
                 1,
                 'running',
                 clock_timestamp()+interval '2 minutes'
             )`,
			s.queue,
		),
		id,
	)

	if err != nil {
		t.Fatal(err)
	}
}

func transfer(
	t *testing.T,
	s *Store,
	id string,
) {
	t.Helper()

	_, err := s.Pool.Exec(
		context.Background(),
		fmt.Sprintf(
			`UPDATE %s.agent_runs
             SET
                 lease_owner='owner-B',
                 lease_epoch=2,
                 lease_until=clock_timestamp()
                     +interval '2 minutes'
             WHERE run_id=$1`,
			s.queue,
		),
		id,
	)

	if err != nil {
		t.Fatal(err)
	}
}

func patchFor(
	t *testing.T,
	w repair17.Workspace,
) repair17.Patch {

	t.Helper()

	sha, err := repair17.Inspect(
		context.Background(),
		w.Path,
		"math.go",
	)

	if err != nil {
		t.Fatal(err)
	}

	return repair17.Patch{
		Path:           "math.go",
		ExpectedSHA256: sha,
		Old:            "return a * b",
		New:            "return a / b",
	}
}
func TestDay19CrashBetweenRenameAndResult(
	t *testing.T,
) {
	s, cleanup := newLab(t)
	defer cleanup()

	ctx := context.Background()

	w, source := makeWorktree(
		t, "day19-crash",
	)

	seed(t, s, w.RunID)

	a := Ref{
		ID:    w.RunID,
		Owner: "owner-A",
		Epoch: 1,
	}

	if err := s.Register(
		ctx, a, w.Path, 2,
	); err != nil {
		t.Fatal(err)
	}

	p := patchFor(t, w)

	intent, err := s.Prepare(
		ctx, a, "patch-1", p,
	)

	if err != nil {
		t.Fatal(err)
	}

	if intent.State != "prepared" {
		t.Fatal(intent)
	}

	// Inject a failure after the filesystem
	// modification, before DB Commit.
	//
	// Panic here is a deterministic simulation,
	// not a real SIGKILL.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("fault hook not reached")
			}
		}()

		_, _ = s.ApplyOrRecover(
			ctx,
			a,
			"patch-1",
			func() {
				panic(
					"simulate SIGKILL just after Rename",
				)
			},
		)
	}()

	// The DB transaction rolled back.
	var state string

	err = s.Pool.QueryRow(
		ctx,
		fmt.Sprintf(
			`SELECT state
             FROM %s
             WHERE run_id=$1
               AND patch_id='patch-1'`,
			s.tbl("patch_ops"),
		),
		w.RunID,
	).Scan(&state)

	if err != nil || state != "prepared" {
		t.Fatalf(
			"state=%s err=%v",
			state, err,
		)
	}

	// But the file modification survived.
	current, err := repair17.Inspect(
		ctx, w.Path, "math.go",
	)

	if err != nil ||
		current != intent.After {
		t.Fatalf(
			"file after SHA=%s err=%v",
			current, err,
		)
	}

	// Simulate a new owner and new Epoch.
	transfer(t, s, w.RunID)

	// The old owner must lose write authority.
	if _, err := s.ApplyOrRecover(
		ctx,
		a,
		"patch-1",
		nil,
	); !errors.Is(err, ErrLeaseLost) {

		t.Fatalf(
			"stale owner accepted: %v",
			err,
		)
	}

	b := Ref{
		ID:    w.RunID,
		Owner: "owner-B",
		Epoch: 2,
	}

	// Worker B must recognize AfterSHA,
	// without executing the Patch again.
	out, err := s.ApplyOrRecover(
		ctx,
		b,
		"patch-1",
		nil,
	)

	if err != nil ||
		out.Action != RecoverApplied {
		t.Fatalf(
			"recovery=%+v err=%v",
			out, err,
		)
	}

	test, err := s.Verify(
		ctx,
		b,
		"patch-1",
		20*time.Second,
	)

	if err != nil || !test.Passed {
		t.Fatalf(
			"test=%+v err=%v",
			test, err,
		)
	}

	// Crash recovery must not grant
	// another repair round.
	used, max, err := s.Summary(
		ctx, w.RunID,
	)

	if err != nil ||
		used != 1 ||
		max != 2 {
		t.Fatalf(
			"budget %d/%d err=%v",
			used, max, err,
		)
	}

	// Original source must remain unchanged.
	raw, err := os.ReadFile(
		filepath.Join(source, "math.go"),
	)

	if err != nil ||
		!strings.Contains(
			string(raw),
			"return a * b",
		) {
		t.Fatal("source polluted")
	}
}

func TestDay19BudgetSurvivesTakeover(
	t *testing.T,
) {
	s, cleanup := newLab(t)
	defer cleanup()

	ctx := context.Background()

	w, _ := makeWorktree(
		t, "day19-budget",
	)

	seed(t, s, w.RunID)

	a := Ref{
		ID:    w.RunID,
		Owner: "owner-A",
		Epoch: 1,
	}

	if err := s.Register(
		ctx, a, w.Path, 1,
	); err != nil {
		t.Fatal(err)
	}

	p := patchFor(t, w)

	if _, err := s.Prepare(
		ctx, a, "p1", p,
	); err != nil {
		t.Fatal(err)
	}

	transfer(t, s, w.RunID)

	b := Ref{
		ID:    w.RunID,
		Owner: "owner-B",
		Epoch: 2,
	}

	// Same logical Patch: allowed replay.
	if _, err := s.Prepare(
		ctx, b, "p1", p,
	); err != nil {
		t.Fatal(
			"idempotent prepare:", err,
		)
	}

	// New Patch: budget already exhausted.
	if _, err := s.Prepare(
		ctx, b, "p2", p,
	); !errors.Is(err, ErrBudget) {

		t.Fatalf(
			"budget bypass: %v",
			err,
		)
	}

	// Cannot silently enlarge existing budget.
	if err := s.Register(
		ctx, b, w.Path, 2,
	); !errors.Is(err, ErrConflict) {

		t.Fatalf(
			"changed budget allowed: %v",
			err,
		)
	}
}

func TestDay19UnknownFileStateRequiresStop(
	t *testing.T,
) {
	s, cleanup := newLab(t)
	defer cleanup()

	ctx := context.Background()

	w, _ := makeWorktree(
		t, "day19-conflict",
	)

	seed(t, s, w.RunID)

	a := Ref{
		ID:    w.RunID,
		Owner: "owner-A",
		Epoch: 1,
	}

	if err := s.Register(
		ctx, a, w.Path, 1,
	); err != nil {
		t.Fatal(err)
	}

	p := patchFor(t, w)

	if _, err := s.Prepare(
		ctx, a, "p1", p,
	); err != nil {
		t.Fatal(err)
	}

	// Simulate unexpected file modification.
	err := os.WriteFile(
		filepath.Join(w.Path, "math.go"),
		[]byte(
			"package example\n"+
				"// unexpected content\n",
		),
		0644,
	)

	if err != nil {
		t.Fatal(err)
	}

	_, err = s.ApplyOrRecover(
		ctx, a, "p1", nil,
	)

	if !errors.Is(err, ErrConflict) {
		t.Fatalf(
			"unknown hash accepted: %v",
			err,
		)
	}
}

func TestDay19LeaseRowLockSerializesClaim(
	t *testing.T,
) {
	s, cleanup := newLab(t)
	defer cleanup()

	ctx := context.Background()

	seed(t, s, "day19-lock")

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)

	err = s.lockLease(
		ctx,
		tx,
		Ref{
			ID:    "day19-lock",
			Owner: "owner-A",
			Epoch: 1,
		},
	)

	if err != nil {
		t.Fatal(err)
	}

	// Another transaction cannot immediately
	// acquire the conflicting row update.
	timeout, stop := context.WithTimeout(
		ctx,
		120*time.Millisecond,
	)
	defer stop()

	_, err = s.Pool.Exec(
		timeout,
		fmt.Sprintf(
			`UPDATE %s.agent_runs
             SET lease_owner='owner-B'
             WHERE run_id='day19-lock'`,
			s.queue,
		),
	)

	if err == nil {
		t.Fatal(
			"other claim unexpectedly bypassed row lock",
		)
	}

	// Release the lock.
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	// A later update should now proceed.
	_, err = s.Pool.Exec(
		ctx,
		fmt.Sprintf(
			`UPDATE %s.agent_runs
             SET lease_owner='owner-B'
             WHERE run_id='day19-lock'`,
			s.queue,
		),
	)

	if err != nil {
		t.Fatal(err)
	}
}
