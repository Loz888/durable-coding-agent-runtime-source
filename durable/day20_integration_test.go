package durable

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
	"runtime-app/repodiag"
)

// Uses the existing day13DB / day14Run
// helpers and dedicated Day14 test DB.
func day20Fixture(
	t *testing.T,
) (source, base string) {

	t.Helper()

	root := t.TempDir()
	source = filepath.Join(root, "source")
	base = filepath.Join(root, "worktrees")

	if err := os.MkdirAll(
		source, 0755,
	); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"go.mod": "module example.com/day20-integration\n" +
			"\ngo 1.23\n",

		"math.go": "package example\n" +
			"func Divide(a,b int) int { return a * b }\n",

		"math_test.go": "package example\n" +
			"import \"testing\"\n" +
			"func TestDivide(t *testing.T){" +
			"if Divide(12,3)!=4 {" +
			"t.Fatal(\"bad divide\")}}\n",
	}

	for name, data := range files {
		err := os.WriteFile(
			filepath.Join(source, name),
			[]byte(data),
			0644,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	commands := [][]string{
		{"init", "-q"},
		{"add", "."},
		{
			"-c", "user.name=Day20",
			"-c", "user.email=day20@example.invalid",
			"commit", "-qm", "baseline",
		},
	}

	for _, args := range commands {
		cmd := exec.Command("git", args...)
		cmd.Dir = source

		output, err := cmd.CombinedOutput()

		if err != nil {
			t.Fatalf(
				"git %v: %v %s",
				args, err, output,
			)
		}
	}

	return source, base
}

// Create an isolated Patch schema while
// retaining the actual day12 Queue schema.
func day20Schema(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) string {

	t.Helper()

	schema := fmt.Sprintf(
		"d20_%d",
		time.Now().UnixNano(),
	)

	ddl := []string{
		"CREATE SCHEMA " + schema,

		`CREATE TABLE ` + schema + `.repair_runs(
            run_id text PRIMARY KEY,
            worktree_path text NOT NULL,
            source_commit text NOT NULL,
            worktree_identity text NOT NULL,
            max_rounds integer NOT NULL,
            rounds_used integer NOT NULL DEFAULT 0,
            created_at timestamptz
                DEFAULT clock_timestamp()
        )`,

		`CREATE TABLE ` + schema + `.patch_ops(
            run_id text NOT NULL
                REFERENCES ` + schema + `.repair_runs(run_id),

            patch_id text NOT NULL,
            file_path text NOT NULL,
            before_sha text NOT NULL,
            after_sha text NOT NULL,
            old_fragment text NOT NULL,
            new_fragment text NOT NULL,
            prepared_epoch bigint NOT NULL,
            round_number integer NOT NULL,
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

            PRIMARY KEY(run_id,patch_id),
            UNIQUE(run_id,round_number)
        )`,
	}

	for _, q := range ddl {
		if _, err := pool.Exec(
			ctx, q,
		); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(
			context.Background(),
			"DROP SCHEMA IF EXISTS "+
				schema+" CASCADE",
		)
	})

	return schema
}

// Real atomic ClaimBatch, not a synthetic claim.
func day20Claim(
	t *testing.T,
	ctx context.Context,
	q Queue,
	id,
	owner string,
) ClaimedRun {

	t.Helper()

	jobs, err := q.ClaimBatch(
		ctx,
		owner,
		1,
		60*time.Second,
	)

	if err != nil ||
		len(jobs) != 1 ||
		jobs[0].RunID != id {

		t.Fatalf(
			"claim jobs=%+v err=%v",
			jobs, err,
		)
	}

	return jobs[0]
}

func day20SetProvider(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	id,
	provider string,
) {
	t.Helper()

	_, err := pool.Exec(
		ctx,
		`UPDATE day12.agent_runs
         SET provider=$2,
             max_attempts=2,
             available_at='-infinity'::timestamptz
         WHERE run_id=$1`,
		id,
		provider,
	)

	if err != nil {
		t.Fatal(err)
	}
}
func TestDay20ExistingSchedulerMockRepair(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	source, base := day20Fixture(t)

	run := day14Run(
		t, pool, source, 2,
	)

	day20SetProvider(
		t,
		ctx,
		pool,
		run.RunID,
		"coding:mock",
	)

	schema := day20Schema(
		t, ctx, pool,
	)

	ps, err := repair19.NewStore(
		pool,
		"day12",
		schema,
	)
	if err != nil {
		t.Fatal(err)
	}

	h := CodingHandler{
		Pool:         pool,
		PatchStore:   ps,
		PatchSchema:  schema,
		WorktreeBase: base,
		File:         "math.go",
		MaxRounds:    2,
		TestTimeout:  10 * time.Second,
	}

	sched := Scheduler{
		Queue:      Queue{Pool: pool},
		Owner:      "day20-scheduler-a",
		TTL:        60 * time.Second,
		RenewEvery: time.Second,
		RunTimeout: 40 * time.Second,

		Models: func(
			string,
		) (repodiag.Model, error) {
			return nil, fmt.Errorf(
				"diagnosis factory must not run for coding",
			)
		},

		CodingRun: h.Execute,
	}

	job := day20Claim(
		t,
		ctx,
		sched.Queue,
		run.RunID,
		sched.Owner,
	)

	// Execute the REAL Scheduler code path.
	sched.execute(ctx, job)

	got, err := (PostgresStore{
		Pool: pool,
	}).Load(
		ctx,
		run.RunID,
	)

	if err != nil ||
		got.Status != Succeeded {
		t.Fatalf(
			"checkpoint=%+v err=%v",
			got, err,
		)
	}

	used, max, err := ps.Summary(
		ctx, run.RunID,
	)

	if err != nil ||
		used != 1 ||
		max != 2 {

		t.Fatalf(
			"budget %d/%d err=%v",
			used, max, err,
		)
	}

	original, err := os.ReadFile(
		filepath.Join(source, "math.go"),
	)

	if err != nil ||
		!strings.Contains(
			string(original),
			"return a * b",
		) {

		t.Fatal("original polluted")
	}

	// Terminal Run must not be claimed again.
	jobs, err := sched.Queue.ClaimBatch(
		ctx,
		"day20-extra",
		1,
		time.Second,
	)

	if err != nil ||
		len(jobs) != 0 {

		t.Fatalf(
			"terminal run claimed: %+v %v",
			jobs, err,
		)
	}
}

func TestDay20RecoverVerifiedPassNoModelKey(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	source, base := day20Fixture(t)

	run := day14Run(
		t, pool, source, 2,
	)

	day20SetProvider(
		t,
		ctx,
		pool,
		run.RunID,
		"coding:deepseek",
	)

	schema := day20Schema(
		t, ctx, pool,
	)

	ps, err := repair19.NewStore(
		pool,
		"day12",
		schema,
	)
	if err != nil {
		t.Fatal(err)
	}

	q := Queue{Pool: pool}

	a := day20Claim(
		t,
		ctx,
		q,
		run.RunID,
		"day20-owner-a",
	)

	ref := repair19.Ref{
		ID:    run.RunID,
		Owner: a.Lease.Owner,
		Epoch: a.Lease.Epoch,
	}

	w, err := repair17.Prepare(
		ctx,
		source,
		base,
		run.RunID,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err = ps.Register(
		ctx,
		ref,
		w.Path,
		2,
	); err != nil {
		t.Fatal(err)
	}

	sha, err := repair17.Inspect(
		ctx,
		w.Path,
		"math.go",
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ps.Prepare(
		ctx,
		ref,
		"repair-001",
		repair17.Patch{
			Path:           "math.go",
			ExpectedSHA256: sha,
			Old:            "return a * b",
			New:            "return a / b",
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ps.ApplyOrRecover(
		ctx,
		ref,
		"repair-001",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	verdict, err := ps.Verify(
		ctx,
		ref,
		"repair-001",
		10*time.Second,
	)

	if err != nil ||
		!verdict.Passed {

		t.Fatalf(
			"verify %+v %v",
			verdict, err,
		)
	}

	// Simulate lost lease after Patch verification
	// but before Agent Checkpoint commit.
	_, err = pool.Exec(
		ctx,
		`UPDATE day12.agent_runs
         SET
            lease_until=clock_timestamp()
                -interval '1 second',
            available_at='-infinity'::timestamptz
         WHERE run_id=$1`,
		run.RunID,
	)
	if err != nil {
		t.Fatal(err)
	}

	b := day20Claim(
		t,
		ctx,
		q,
		run.RunID,
		"day20-owner-b",
	)

	if b.Lease.Epoch != a.Lease.Epoch+1 {
		t.Fatalf(
			"epoch=%d",
			b.Lease.Epoch,
		)
	}

	// Recovery should not need API credentials.
	t.Setenv("DEEPSEEK_API_KEY", "")

	h := CodingHandler{
		Pool:         pool,
		PatchStore:   ps,
		PatchSchema:  schema,
		WorktreeBase: base,
		File:         "math.go",
		MaxRounds:    2,
		TestTimeout:  10 * time.Second,
	}

	snap, err := h.Execute(ctx, b)

	if err != nil ||
		snap.Status != Succeeded {

		t.Fatalf(
			"resume %+v %v",
			snap, err,
		)
	}

	used, max, err := ps.Summary(
		ctx, run.RunID,
	)

	if err != nil ||
		used != 1 ||
		max != 2 {

		t.Fatalf(
			"budget %d/%d %v",
			used, max, err,
		)
	}
}

type forbidModel struct{}

func (forbidModel) Suggest(
	context.Context,
	repair18.Request,
) (repair18.Suggestion, error) {

	return repair18.Suggestion{},
		fmt.Errorf(
			"MODEL_CALLED_DURING_RECOVERY",
		)
}

// Child-process entrypoint.
// The parent test launches this in a real OS process.
func TestDay20CrashProcess(t *testing.T) {
	if os.Getenv("DAY20_CHILD") != "1" {
		return
	}

	ctx := context.Background()

	pool, err := pgxpool.New(
		ctx,
		os.Getenv("DAY12_TEST_DATABASE_URL"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var db string

	err = pool.QueryRow(
		ctx,
		`SELECT current_database()`,
	).Scan(&db)

	if err != nil ||
		db != "agent_runtime_day14_test" {

		t.Fatal("not test db")
	}

	schema := os.Getenv("DAY20_SCHEMA")

	ps, err := repair19.NewStore(
		pool,
		"day12",
		schema,
	)
	if err != nil {
		t.Fatal(err)
	}

	id := os.Getenv("DAY20_RUN")

	job := ClaimedRun{
		RunID:    id,
		Repo:     os.Getenv("DAY20_SOURCE"),
		Provider: "coding:mock",

		Lease: Lease{
			RunID: id,
			Owner: "day20-crash-a",
			Epoch: 1,
		},
	}

	handler := CodingHandler{
		Pool:         pool,
		PatchStore:   ps,
		PatchSchema:  schema,
		WorktreeBase: os.Getenv("DAY20_BASE"),
		File:         "math.go",
		MaxRounds:    2,
		TestTimeout:  15 * time.Second,

		BeforeCheckpoint: func() {
			_ = syscall.Kill(
				os.Getpid(),
				syscall.SIGKILL,
			)
		},
	}

	_, err = handler.Execute(ctx, job)

	t.Fatalf(
		"expected real SIGKILL, got %v",
		err,
	)
}

func TestDay20RealSIGKILLBeforeCheckpoint(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	source, base := day20Fixture(t)

	run := day14Run(
		t, pool, source, 2,
	)

	day20SetProvider(
		t,
		ctx,
		pool,
		run.RunID,
		"coding:mock",
	)

	schema := day20Schema(
		t, ctx, pool,
	)

	ps, err := repair19.NewStore(
		pool,
		"day12",
		schema,
	)
	if err != nil {
		t.Fatal(err)
	}

	q := Queue{Pool: pool}

	a := day20Claim(
		t,
		ctx,
		q,
		run.RunID,
		"day20-crash-a",
	)

	if a.Lease.Epoch != 1 {
		t.Fatalf(
			"first epoch=%d",
			a.Lease.Epoch,
		)
	}

	// The parent may have resolved its DSN from .env.test. Keep the child
	// independent of an exported variable so this path remains covered in CI.
	t.Setenv("DAY12_TEST_DATABASE_URL", "")

	// Child runs the actual CodingHandler,
	// verifies the Patch, then SIGKILLs itself.
	cmd := exec.Command(
		os.Args[0],
		"-test.run=^TestDay20CrashProcess$",
		"-test.v",
	)

	cmd.Env = append(
		os.Environ(),
		// Use the same test database already validated by day13DB.
		"DAY12_TEST_DATABASE_URL="+pool.Config().ConnString(),
		"DAY20_CHILD=1",
		"DAY20_SCHEMA="+schema,
		"DAY20_RUN="+run.RunID,
		"DAY20_SOURCE="+source,
		"DAY20_BASE="+base,
		"DEEPSEEK_API_KEY=",
	)

	output, err := cmd.CombinedOutput()

	var exit *exec.ExitError

	if !errors.As(err, &exit) {
		t.Fatalf(
			"expected SIGKILL: %v %s",
			err, output,
		)
	}

	ws, ok := exit.Sys().(syscall.WaitStatus)

	if !ok ||
		!ws.Signaled() ||
		ws.Signal() != syscall.SIGKILL {

		t.Fatalf(
			"not SIGKILL: %v %s",
			err, output,
		)
	}

	// Patch verification survived SIGKILL.
	ops, err := ps.Recoverable(
		ctx,
		repair19.Ref{
			ID:    run.RunID,
			Owner: a.Lease.Owner,
			Epoch: a.Lease.Epoch,
		},
	)

	if err != nil ||
		len(ops) != 1 ||
		ops[0].State != "verified_pass" {

		t.Fatalf(
			"after crash ops=%+v err=%v",
			ops, err,
		)
	}

	// But the final Agent Checkpoint did not.
	cp, err := (PostgresStore{
		Pool: pool,
	}).Load(
		ctx,
		run.RunID,
	)

	if err != nil ||
		cp.Status != Running {

		t.Fatalf(
			"checkpoint already finalized: %+v %v",
			cp, err,
		)
	}

	// Force expiry only for this test Run.
	_, err = pool.Exec(
		ctx,
		`UPDATE day12.agent_runs
         SET
            lease_until=clock_timestamp()
                -interval '1 second',
            available_at='-infinity'::timestamptz
         WHERE run_id=$1`,
		run.RunID,
	)
	if err != nil {
		t.Fatal(err)
	}

	// Obtain a REAL new Claim and Epoch.
	b := day20Claim(
		t,
		ctx,
		q,
		run.RunID,
		"day20-crash-b",
	)

	// If recovery calls the model, this will fail.
	h := CodingHandler{
		Pool:         pool,
		PatchStore:   ps,
		PatchSchema:  schema,
		WorktreeBase: base,
		File:         "math.go",
		MaxRounds:    2,
		TestTimeout:  15 * time.Second,
		Proposer:     forbidModel{},
	}

	final, err := h.Execute(ctx, b)

	if err != nil ||
		final.Status != Succeeded {

		t.Fatalf(
			"recovery failed / model called: %+v %v",
			final, err,
		)
	}

	used, _, err := ps.Summary(
		ctx,
		run.RunID,
	)

	if err != nil ||
		used != 1 {

		t.Fatalf(
			"budget used=%d err=%v",
			used, err,
		)
	}
}
