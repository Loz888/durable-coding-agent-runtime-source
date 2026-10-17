package reliability15

import (
	"context"
	"errors"
	"fmt"
	"runtime-app/internal/testdb"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var schemaSequence atomic.Int64

func newTestStore(
	t *testing.T,
) (*Store, context.Context) {
	t.Helper()

	url := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal(
			"DAY12_TEST_DATABASE_URL is empty; tests must not SKIP",
		)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		20*time.Second,
	)
	t.Cleanup(cancel)

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var dbname string

	err = pool.QueryRow(
		ctx,
		"SELECT current_database()",
	).Scan(&dbname)
	if err != nil {
		t.Fatal(err)
	}

	if dbname != "agent_runtime_day14_test" {
		t.Fatalf(
			"refusing to mutate database %q",
			dbname,
		)
	}

	schema := fmt.Sprintf(
		"d15_%d_%d",
		time.Now().UnixNano(),
		schemaSequence.Add(1),
	)

	_, err = pool.Exec(
		ctx,
		"CREATE SCHEMA "+schema,
	)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel :=
			context.WithTimeout(
				context.Background(),
				5*time.Second,
			)
		defer cleanupCancel()

		_, err := pool.Exec(
			cleanupCtx,
			"DROP SCHEMA IF EXISTS "+
				schema+" CASCADE",
		)
		if err != nil {
			t.Errorf("schema cleanup: %v", err)
		}
	})

	setup := fmt.Sprintf(`
        CREATE TABLE %s.agent_runs (
            run_id text PRIMARY KEY,
            status text NOT NULL DEFAULT 'pending',
            queue_enabled boolean NOT NULL DEFAULT true,
            priority integer NOT NULL DEFAULT 0,
            available_at timestamptz NOT NULL
                DEFAULT clock_timestamp(),
            lease_owner text,
            lease_epoch bigint NOT NULL DEFAULT 0,
            lease_until timestamptz,
            revision bigint NOT NULL DEFAULT 0,
            retry_count bigint NOT NULL DEFAULT 0,
            max_attempts bigint NOT NULL DEFAULT 3,
            failure_kind text,
            failure_reason text,
            first_claimed_at timestamptz,
            finished_at timestamptz
        );

        CREATE TABLE %s.run_events (
            event_id bigserial PRIMARY KEY,
            run_id text NOT NULL,
            lease_epoch bigint NOT NULL,
            revision bigint NOT NULL,
            event_type text NOT NULL,
            detail text NOT NULL DEFAULT '',
            occurred_at timestamptz NOT NULL
                DEFAULT clock_timestamp(),
            UNIQUE (
                run_id,lease_epoch,revision,event_type
            )
        );
    `, schema, schema)

	_, err = pool.Exec(
		ctx,
		setup,
		pgx.QueryExecModeSimpleProtocol,
	)
	if err != nil {
		t.Fatal(err)
	}

	store, err := NewStore(pool, schema)
	if err != nil {
		t.Fatal(err)
	}

	return store, ctx
}

func putRun(
	t *testing.T,
	s *Store,
	ctx context.Context,
	id string,
	max int64,
) {
	t.Helper()

	sql := fmt.Sprintf(
		"INSERT INTO %s(run_id,max_attempts) VALUES($1,$2)",
		s.table("agent_runs"),
	)

	if _, err := s.Pool.Exec(
		ctx, sql, id, max,
	); err != nil {
		t.Fatal(err)
	}
}

// Test-only atomic claim helper.
// Merge its budget predicate into original Day14 ClaimBatch.
func claimForTest(
	t *testing.T,
	s *Store,
	ctx context.Context,
	id string,
	owner string,
) (int64, bool) {
	t.Helper()

	sql := fmt.Sprintf(`
        WITH candidate AS (
            SELECT run_id
            FROM %s
            WHERE run_id=$1
              AND queue_enabled=TRUE
              AND lease_epoch < max_attempts
              AND (
                  (
                      status='pending'
                      AND available_at <= clock_timestamp()
                  )
                  OR (
                      status='running'
                      AND lease_until <= clock_timestamp()
                  )
              )
            FOR UPDATE SKIP LOCKED
            LIMIT 1
        )
        UPDATE %s AS a
        SET
            status='running',
            lease_owner=$2,
            lease_epoch=a.lease_epoch+1,
            lease_until=clock_timestamp()
                + INTERVAL '10 seconds',
            first_claimed_at=COALESCE(
                a.first_claimed_at,
                clock_timestamp()
            )
        FROM candidate
        WHERE a.run_id=candidate.run_id
        RETURNING a.lease_epoch
    `,
		s.table("agent_runs"),
		s.table("agent_runs"),
	)

	var epoch int64

	err := s.Pool.QueryRow(
		ctx, sql, id, owner,
	).Scan(&epoch)

	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}

	return epoch, true
}

func expireForTest(
	t *testing.T,
	s *Store,
	ctx context.Context,
	id string,
) {
	t.Helper()

	sql := fmt.Sprintf(`
        UPDATE %s
        SET lease_until=clock_timestamp()
            -INTERVAL '1 second'
        WHERE run_id=$1
    `, s.table("agent_runs"))

	_, err := s.Pool.Exec(ctx, sql, id)
	if err != nil {
		t.Fatal(err)
	}
}

func TestDay15CrashLoopExhaustion(t *testing.T) {
	s, ctx := newTestStore(t)

	putRun(t, s, ctx, "crash-loop", 2)

	epoch, ok := claimForTest(
		t, s, ctx, "crash-loop", "worker-a",
	)
	if !ok || epoch != 1 {
		t.Fatalf(
			"claim A: epoch=%d ok=%v",
			epoch, ok,
		)
	}

	expireForTest(t, s, ctx, "crash-loop")

	epoch, ok = claimForTest(
		t, s, ctx, "crash-loop", "worker-b",
	)
	if !ok || epoch != 2 {
		t.Fatalf(
			"claim B: epoch=%d ok=%v",
			epoch, ok,
		)
	}

	expireForTest(t, s, ctx, "crash-loop")

	count, err := s.SweepExhausted(ctx, 10)
	if err != nil || count != 1 {
		t.Fatalf(
			"sweep count=%d err=%v",
			count, err,
		)
	}

	_, ok = claimForTest(
		t, s, ctx, "crash-loop", "worker-c",
	)
	if ok {
		t.Fatal("third claim must be forbidden")
	}

	r, err := s.InspectRun(ctx, "crash-loop")
	if err != nil ||
		r.Status != "failed" ||
		r.FailureKind != "retry_exhausted" ||
		r.Epoch != 2 {
		t.Fatalf(
			"unexpected run: %+v err=%v",
			r, err,
		)
	}
}

func TestDay15StaleOwnerCannotFailNewRun(
	t *testing.T,
) {
	s, ctx := newTestStore(t)

	putRun(t, s, ctx, "fenced", 2)

	claimForTest(
		t, s, ctx, "fenced", "worker-a",
	)

	expireForTest(t, s, ctx, "fenced")

	claimForTest(
		t, s, ctx, "fenced", "worker-b",
	)

	_, err := s.FinishFailure(
		ctx,
		RunRef{
			ID:    "fenced",
			Owner: "worker-a",
			Epoch: 1,
		},
		errors.New("stale failure"),
		"old worker",
		time.Millisecond,
		time.Second,
	)

	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf(
			"stale owner err=%v",
			err,
		)
	}

	r, err := s.InspectRun(ctx, "fenced")
	if err != nil ||
		r.Status != "running" ||
		r.Epoch != 2 {
		t.Fatalf(
			"stale owner changed new claim: %+v, %v",
			r, err,
		)
	}
}

func TestDay15CheckpointEventIdempotent(
	t *testing.T,
) {
	s, ctx := newTestStore(t)

	putRun(t, s, ctx, "checkpoints", 2)

	claimForTest(
		t, s, ctx, "checkpoints", "worker-a",
	)

	// Simulate an already-committed revision CAS.
	sql := fmt.Sprintf(`
        UPDATE %s
        SET revision=1
        WHERE run_id='checkpoints'
    `, s.table("agent_runs"))

	if _, err := s.Pool.Exec(
		ctx, sql,
	); err != nil {
		t.Fatal(err)
	}

	first, err := s.RecordCheckpoint(
		ctx, "checkpoints", 1, 1,
	)
	if err != nil || !first {
		t.Fatalf(
			"first checkpoint: %v %v",
			first, err,
		)
	}

	second, err := s.RecordCheckpoint(
		ctx, "checkpoints", 1, 1,
	)
	if err != nil || second {
		t.Fatalf(
			"duplicate checkpoint: %v %v",
			second, err,
		)
	}

	third, err := s.RecordCheckpoint(
		ctx, "checkpoints", 1, 77,
	)
	if err != nil || third {
		t.Fatalf(
			"uncommitted checkpoint: %v %v",
			third, err,
		)
	}

	var n int

	sql = fmt.Sprintf(`
        SELECT COUNT(*)
        FROM %s
        WHERE run_id='checkpoints'
    `, s.table("run_events"))

	err = s.Pool.QueryRow(
		ctx, sql,
	).Scan(&n)

	if err != nil || n != 1 {
		t.Fatalf(
			"events=%d err=%v",
			n, err,
		)
	}
}

func TestDay15SchedulerPermanentFailure(
	t *testing.T,
) {
	s, ctx := newTestStore(t)

	putRun(t, s, ctx, "bad-request", 3)

	claimForTest(
		t, s, ctx, "bad-request", "worker-a",
	)

	d, err := s.FinishFailure(
		ctx,
		RunRef{
			ID:    "bad-request",
			Owner: "worker-a",
			Epoch: 1,
		},
		errors.New("invalid tool arguments"),
		"invalid tool arguments",
		time.Millisecond,
		time.Second,
	)

	if err != nil ||
		d.Action != ActionFail ||
		d.Kind != FailurePermanent {
		t.Fatalf(
			"decision=%+v err=%v",
			d, err,
		)
	}

	r, err := s.InspectRun(ctx, "bad-request")

	if err != nil ||
		r.Status != "failed" ||
		r.Retries != 0 ||
		r.Epoch != 1 ||
		r.Finished == nil {
		t.Fatalf(
			"unexpected terminal run: %+v err=%v",
			r, err,
		)
	}

	_, ok := claimForTest(
		t, s, ctx, "bad-request", "worker-b",
	)
	if ok {
		t.Fatal("terminal run was reclaimed")
	}
}

func TestDay15TransientFailureReschedule(
	t *testing.T,
) {
	s, ctx := newTestStore(t)

	putRun(t, s, ctx, "transient", 2)

	claimForTest(
		t, s, ctx, "transient", "worker-a",
	)

	d, err := s.FinishFailure(
		ctx,
		RunRef{
			ID:    "transient",
			Owner: "worker-a",
			Epoch: 1,
		},
		Retryable(
			errors.New("retry-safe provider error"),
		),
		"HTTP 503",
		time.Millisecond,
		time.Second,
	)

	if err != nil || d.Action != ActionRetry {
		t.Fatalf(
			"decision=%+v err=%v",
			d, err,
		)
	}

	r, err := s.InspectRun(ctx, "transient")

	if err != nil ||
		r.Status != "pending" ||
		r.Retries != 1 {
		t.Fatalf(
			"unexpected retry %+v %v",
			r, err,
		)
	}

	sql := fmt.Sprintf(`
        UPDATE %s
        SET available_at=clock_timestamp()
            -INTERVAL '1 second'
        WHERE run_id=$1
    `, s.table("agent_runs"))

	if _, err := s.Pool.Exec(
		ctx, sql, "transient",
	); err != nil {
		t.Fatal(err)
	}

	epoch, ok := claimForTest(
		t, s, ctx, "transient", "worker-b",
	)
	if !ok || epoch != 2 {
		t.Fatalf(
			"second claim epoch=%d ok=%v",
			epoch, ok,
		)
	}

	d, err = s.FinishFailure(
		ctx,
		RunRef{
			ID:    "transient",
			Owner: "worker-b",
			Epoch: 2,
		},
		Retryable(
			errors.New("provider still down"),
		),
		"HTTP 503",
		time.Millisecond,
		time.Second,
	)

	if err != nil ||
		d.Action != ActionFail ||
		d.Kind != FailureExhausted {
		t.Fatalf(
			"budget not enforced: %+v %v",
			d, err,
		)
	}

	m, err := s.QueueSummary(ctx)

	if err != nil ||
		m.Failed != 1 ||
		m.ExplicitRetries != 1 ||
		m.GrantedAttempts != 2 {
		t.Fatalf(
			"wrong summary %+v %v",
			m, err,
		)
	}
}
