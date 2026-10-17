package reliability15

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store extends the existing scheduler.
// It does not claim or poll Agent Runs.
type Store struct {
	Pool         *pgxpool.Pool
	schema       string
	legacyEvents bool
}

var schemaPattern = regexp.MustCompile(
	`^[a-z][a-z0-9_]*$`,
)

func NewStore(
	pool *pgxpool.Pool,
	schema string,
) (*Store, error) {
	if pool == nil ||
		!schemaPattern.MatchString(schema) {
		return nil, fmt.Errorf(
			"invalid pool or SQL schema",
		)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var legacy bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_attribute
 WHERE attrelid=to_regclass($1) AND attname='detail' AND NOT attisdropped)`,
		schema+".run_events").Scan(&legacy)
	if err != nil {
		return nil, err
	}
	return &Store{Pool: pool, schema: schema, legacyEvents: legacy}, nil
}

func (s *Store) table(name string) string {
	return s.schema + "." + name
}

type RunRef struct {
	ID    string
	Owner string
	Epoch int64
}

// Failure transition guarded by the CURRENT live lease.
func (s *Store) FinishFailure(
	ctx context.Context,
	ref RunRef,
	cause error,
	reason string,
	base time.Duration,
	maxDelay time.Duration,
) (Decision, error) {
	if ref.ID == "" ||
		ref.Owner == "" ||
		ref.Epoch < 1 {
		return Decision{},
			fmt.Errorf("invalid run reference")
	}

	if base <= 0 || maxDelay < base {
		return Decision{},
			fmt.Errorf("invalid backoff")
	}

	tx, err := s.Pool.BeginTx(
		ctx, pgx.TxOptions{},
	)
	if err != nil {
		return Decision{}, err
	}
	defer tx.Rollback(ctx)

	var epoch, maxAttempts, revision int64

	sqlSelect := fmt.Sprintf(`
        SELECT lease_epoch, max_attempts, revision
        FROM %s
        WHERE run_id=$1
          AND lease_owner=$2
          AND lease_epoch=$3
          AND status='running'
          AND lease_until > clock_timestamp()
        FOR UPDATE
    `, s.table("agent_runs"))

	err = tx.QueryRow(
		ctx,
		sqlSelect,
		ref.ID,
		ref.Owner,
		ref.Epoch,
	).Scan(
		&epoch,
		&maxAttempts,
		&revision,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return Decision{}, ErrLeaseLost
	}
	if err != nil {
		return Decision{}, err
	}

	decision, err := DecideFailure(
		cause,
		epoch,
		maxAttempts,
	)
	if err != nil {
		return Decision{}, err
	}

	if decision.Action == ActionIgnore {
		return decision, ErrNoAction
	}

	kind := string(decision.Kind)
	status := "failed"
	event := "terminal_failed"

	reason = SafeReason(reason)

	delay := time.Duration(0)

	if decision.Action == ActionRetry {
		status = "pending"
		if !s.legacyEvents {
			status = "running"
		}
		event = "retry_scheduled"
		delay = Backoff(
			epoch,
			base,
			maxDelay,
		)
	}

	sqlUpdate := fmt.Sprintf(`
        UPDATE %s
        SET
            status=$4,
            failure_kind=$5,
            failure_reason=$6,

            retry_count = retry_count +
                CASE WHEN $4 IN ('pending','running')
                     THEN 1 ELSE 0 END,

            available_at =
                CASE WHEN $4 IN ('pending','running')
                     THEN clock_timestamp()+$7::interval
                     ELSE available_at END,

            finished_at =
                CASE WHEN $4='failed'
                     THEN clock_timestamp()
                     ELSE NULL END,

            lease_owner=NULL,
            lease_until=NULL

        WHERE run_id=$1
          AND lease_owner=$2
          AND lease_epoch=$3
          AND status='running'
          AND lease_until > clock_timestamp()
    `, s.table("agent_runs"))

	cmd, err := tx.Exec(
		ctx,
		sqlUpdate,
		ref.ID,
		ref.Owner,
		ref.Epoch,
		status,
		kind,
		reason,
		fmt.Sprintf(
			"%d microseconds",
			delay.Microseconds(),
		),
	)
	if err != nil {
		return Decision{}, err
	}

	if cmd.RowsAffected() != 1 {
		return Decision{}, ErrLeaseLost
	}

	if _, err = s.recordEvent(ctx, tx, ref, revision, event, reason); err != nil {
		return Decision{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Decision{}, err
	}

	return decision, nil
}

// Sweep exhausted execution authorizations, including manual claims.
// Runs with a live lease and successful terminal runs are never selected.
// This is called by the EXISTING Day14 scheduler.
func (s *Store) SweepExhausted(
	ctx context.Context,
	batch int,
) (int, error) {
	if batch < 1 || batch > 1000 {
		return 0, fmt.Errorf(
			"batch must be 1..1000",
		)
	}

	tx, err := s.Pool.BeginTx(
		ctx, pgx.TxOptions{},
	)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	selectSQL := fmt.Sprintf(`
        SELECT run_id, lease_epoch, revision
        FROM %s
        WHERE lease_epoch >= max_attempts
          AND (
              status='pending'
              OR (
                  status='running'
                  AND (lease_owner IS NULL OR lease_until IS NULL OR lease_until <= clock_timestamp())
              )
          )
        ORDER BY available_at, run_id
        FOR UPDATE SKIP LOCKED
        LIMIT $1
    `, s.table("agent_runs"))

	rows, err := tx.Query(
		ctx, selectSQL, batch,
	)
	if err != nil {
		return 0, err
	}

	type target struct {
		id    string
		epoch int64
		rev   int64
	}

	var targets []target

	for rows.Next() {
		var x target
		if err := rows.Scan(
			&x.id, &x.epoch, &x.rev,
		); err != nil {
			rows.Close()
			return 0, err
		}
		targets = append(targets, x)
	}

	err = rows.Err()
	rows.Close()

	if err != nil {
		return 0, err
	}

	for _, x := range targets {
		updateSQL := fmt.Sprintf(`
            UPDATE %s
            SET status='failed',
                failure_kind='retry_exhausted',
                failure_reason=
                    'execution authorization budget exhausted',
                finished_at=clock_timestamp(),
                lease_owner=NULL,
                lease_until=NULL
            WHERE run_id=$1
        `, s.table("agent_runs"))

		if _, err = tx.Exec(
			ctx, updateSQL, x.id,
		); err != nil {
			return 0, err
		}

		if _, err = s.recordEvent(ctx, tx, RunRef{ID: x.id, Epoch: x.epoch}, x.rev, "budget_exhausted", "claim budget reached"); err != nil {
			return 0, err
		}

	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}

	return len(targets), nil
}

// This event is written AFTER the real checkpoint commit.
// It is for observability, not durable source-of-truth.
func (s *Store) RecordCheckpoint(
	ctx context.Context,
	runID string,
	epoch int64,
	revision int64,
) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var exists bool
	query := fmt.Sprintf("SELECT EXISTS(SELECT 1 FROM %s WHERE run_id=$1 AND lease_epoch=$2 AND revision=$3)", s.table("agent_runs"))
	if err = tx.QueryRow(ctx, query, runID, epoch, revision).Scan(&exists); err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	added, err := s.recordEvent(ctx, tx, RunRef{ID: runID, Epoch: epoch}, revision, "checkpoint", "committed")
	if err != nil {
		return false, err
	}
	return added, tx.Commit(ctx)
}

type Summary struct {
	Total           int64
	Pending         int64
	Running         int64
	Succeeded       int64
	Failed          int64
	ExplicitRetries int64
	GrantedAttempts int64
	MeanRunSeconds  float64
}

func (s *Store) QueueSummary(
	ctx context.Context,
) (Summary, error) {
	var m Summary

	query := fmt.Sprintf(`
        SELECT
            count(*),

            count(*) FILTER (
                WHERE status='pending'
            ),

            count(*) FILTER (
                WHERE status='running'
            ),

            count(*) FILTER (
                WHERE status='succeeded'
            ),

            count(*) FILTER (
                WHERE status='failed'
            ),

            COALESCE(sum(retry_count),0),
            COALESCE(sum(lease_epoch),0),

            COALESCE(
                avg(
                    EXTRACT(
                        EPOCH FROM (
                            COALESCE(
                                finished_at,
                                clock_timestamp()
                            ) - first_claimed_at
                        )
                    )
                ) FILTER (
                    WHERE first_claimed_at IS NOT NULL
                ),0
            )::float8

        FROM %s
        WHERE queue_enabled=TRUE
    `, s.table("agent_runs"))

	err := s.Pool.QueryRow(
		ctx, query,
	).Scan(
		&m.Total,
		&m.Pending,
		&m.Running,
		&m.Succeeded,
		&m.Failed,
		&m.ExplicitRetries,
		&m.GrantedAttempts,
		&m.MeanRunSeconds,
	)

	return m, err
}

type RunInfo struct {
	ID            string
	Status        string
	Epoch         int64
	MaxAttempts   int64
	Retries       int64
	Revision      int64
	FailureKind   string
	FailureReason string
	FirstClaimed  *time.Time
	Finished      *time.Time
}

func (s *Store) InspectRun(
	ctx context.Context,
	id string,
) (RunInfo, error) {
	var r RunInfo

	query := fmt.Sprintf(`
        SELECT
            run_id,
            status,
            lease_epoch,
            max_attempts,
            retry_count,
            revision,
            COALESCE(failure_kind,''),
            COALESCE(failure_reason,''),
            first_claimed_at,
            finished_at
        FROM %s
        WHERE run_id=$1
    `, s.table("agent_runs"))

	err := s.Pool.QueryRow(
		ctx, query, id,
	).Scan(
		&r.ID,
		&r.Status,
		&r.Epoch,
		&r.MaxAttempts,
		&r.Retries,
		&r.Revision,
		&r.FailureKind,
		&r.FailureReason,
		&r.FirstClaimed,
		&r.Finished,
	)

	return r, err
}

// recordEvent supports the original Day15 fixture and the shipped Day12 event
// schema without changing either public API or the database's unique keys.
func (s *Store) recordEvent(ctx context.Context, tx pgx.Tx, ref RunRef, revision int64, event, detail string) (bool, error) {
	var query string
	var args []any
	if s.legacyEvents {
		query = fmt.Sprintf(`INSERT INTO %s(run_id,lease_epoch,revision,event_type,detail)
   VALUES($1,$2,$3,$4,$5)
   ON CONFLICT(run_id,lease_epoch,revision,event_type) DO NOTHING`, s.table("run_events"))
		args = []any{ref.ID, ref.Epoch, revision, event, detail}
	} else {
		query = fmt.Sprintf(`INSERT INTO %s(run_id,lease_epoch,revision,event_type,turns,calls,status)
   SELECT run_id,lease_epoch,revision,$4,turns,calls,status FROM %s
   WHERE run_id=$1 AND lease_epoch=$2 AND revision=$3
   ON CONFLICT(run_id,revision,event_type) DO NOTHING`, s.table("run_events"), s.table("agent_runs"))
		args = []any{ref.ID, ref.Epoch, revision, event}
	}
	tag, err := tx.Exec(ctx, query, args...)
	return tag.RowsAffected() == 1, err
}
