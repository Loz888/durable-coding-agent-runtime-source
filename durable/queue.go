package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ClaimedRun 包含 Agent 元数据和当前执行权。
type ClaimedRun struct {
	RunID      string
	Repo       string
	Provider   string
	Goal       string
	RetryCount int
	Lease      Lease
}

type Queue struct {
	Pool *pgxpool.Pool
}

// Submit 原子创建 Agent Run 和 Revision 0。
// 只有通过这个方法提交的 Run 才自动入队。
func (q Queue) Submit(
	ctx context.Context,
	s Snapshot,
	priority int,
) error {
	if q.Pool == nil {
		return errors.New("nil postgres pool")
	}

	if err := s.Validate(); err != nil {
		return err
	}

	if s.Status != Running ||
		s.Revision != 0 ||
		s.Turns != 0 ||
		s.Calls != 0 ||
		s.Final != "" {
		return errors.New(
			"new queued run must start at revision 0, running, with no calls",
		)
	}

	if priority < -100 || priority > 100 {
		return errors.New(
			"priority must be between -100 and 100",
		)
	}

	history, err := json.Marshal(s.History)
	if err != nil {
		return err
	}

	tx, err := q.Pool.BeginTx(
		ctx,
		pgx.TxOptions{},
	)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(
		ctx,
		`INSERT INTO day12.agent_runs
		(
			run_id,
			repo,
			provider,
			goal,
			status,
			revision,
			turns,
			calls,
			final_text,
			queue_enabled,
			priority,
			available_at
		)
		VALUES (
			$1, $2, $3, $4,
			'running', 0, 0, 0, '',
			TRUE, $5, clock_timestamp()
		)
		ON CONFLICT (run_id) DO NOTHING`,
		s.RunID,
		s.Repo,
		s.Provider,
		s.Goal,
		priority,
	)
	if err != nil {
		return fmt.Errorf(
			"submit run: %w",
			err,
		)
	}

	if tag.RowsAffected() != 1 {
		return ErrExists
	}

	_, err = tx.Exec(
		ctx,
		`INSERT INTO day12.checkpoints
			(run_id, revision, history)
		VALUES ($1, 0, $2::jsonb)`,
		s.RunID,
		string(history),
	)
	if err != nil {
		return fmt.Errorf(
			"submit initial checkpoint: %w",
			err,
		)
	}

	return tx.Commit(ctx)
}

// ClaimBatch 原子领取最多 n 个任务。
//
// 并发 Worker 使用 SKIP LOCKED 跳过
// 已被其他事务锁定的候选任务。
func (q Queue) ClaimBatch(
	ctx context.Context,
	owner string,
	n int,
	ttl time.Duration,
) ([]ClaimedRun, error) {
	if q.Pool == nil {
		return nil, errors.New(
			"nil postgres pool",
		)
	}

	if strings.TrimSpace(owner) == "" {
		return nil, errors.New(
			"owner session is required",
		)
	}

	if n < 1 || n > 64 {
		return nil, errors.New(
			"batch size must be 1..64",
		)
	}

	if err := validateLeaseInput(
		"queue",
		owner,
		ttl,
	); err != nil {
		return nil, err
	}

	rows, err := q.Pool.Query(
		ctx,
		`WITH candidates AS (
			SELECT run_id
			FROM day12.agent_runs
			WHERE queue_enabled = TRUE
			  AND status = 'running'
			  AND lease_epoch < max_attempts
			  AND available_at <= clock_timestamp()
			  AND (
				lease_owner IS NULL
				OR lease_until <= clock_timestamp()
			  )
			ORDER BY
				priority DESC,
				available_at ASC,
				created_at ASC,
				run_id ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE day12.agent_runs AS r
		SET
			lease_owner = $2,
			lease_epoch = r.lease_epoch + 1,
            first_claimed_at = COALESCE(r.first_claimed_at, clock_timestamp()),
			lease_until = clock_timestamp()
				+ ($3::bigint * interval '1 microsecond'),
			updated_at = clock_timestamp()
		FROM candidates AS c
		WHERE r.run_id = c.run_id
		RETURNING
			r.run_id,
			r.repo,
			r.provider,
			r.goal,
			r.retry_count,
			r.lease_owner,
			r.lease_epoch,
			r.lease_until`,
		n,
		owner,
		ttl.Microseconds(),
	)
	if err != nil {
		return nil, fmt.Errorf(
			"claim batch: %w",
			err,
		)
	}
	defer rows.Close()

	result := make(
		[]ClaimedRun,
		0,
		n,
	)

	for rows.Next() {
		var job ClaimedRun

		err := rows.Scan(
			&job.RunID,
			&job.Repo,
			&job.Provider,
			&job.Goal,
			&job.RetryCount,
			&job.Lease.Owner,
			&job.Lease.Epoch,
			&job.Lease.Until,
		)
		if err != nil {
			return nil, err
		}

		job.Lease.RunID = job.RunID

		result = append(
			result,
			job,
		)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"claim rows: %w",
			err,
		)
	}

	return result, nil
}

// RetryLater increments only explicit reschedules; Claim alone consumes an attempt.
// RetryLater 将执行失败的 Run 延期重新入队。
// 必须仍然持有有效 Lease 才能成功。
func (q Queue) RetryLater(
	ctx context.Context,
	lease Lease,
	delay time.Duration,
) error {
	if q.Pool == nil {
		return errors.New(
			"nil postgres pool",
		)
	}

	if delay < 0 || delay > time.Hour {
		return errors.New(
			"retry delay out of range",
		)
	}

	tag, err := q.Pool.Exec(
		ctx,
		`UPDATE day12.agent_runs
		SET
			retry_count = retry_count + 1,
			available_at = clock_timestamp()
				+ ($4::bigint * interval '1 microsecond'),
			lease_owner = NULL,
			lease_until = NULL,
			updated_at = clock_timestamp()
		WHERE run_id = $1
		  AND lease_owner = $2
		  AND lease_epoch = $3
		  AND lease_until > clock_timestamp()
		  AND queue_enabled = TRUE
		  AND status = 'running'`,
		lease.RunID,
		lease.Owner,
		lease.Epoch,
		delay.Microseconds(),
	)
	if err != nil {
		return fmt.Errorf(
			"retry later: %w",
			err,
		)
	}

	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}

	return nil
}
