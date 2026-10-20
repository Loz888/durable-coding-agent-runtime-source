package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FencedStore 用于已经被成功 Claim 的 Run。
// 它在提交 Checkpoint 的同一事务中检查 Lease 和 Revision。
type FencedStore struct {
	Pool  *pgxpool.Pool
	Lease Lease
}

// 新 Run 必须先由 PostgresStore.Create 初始化，
// 然后领取 Lease，再交给 FencedStore。
func (f FencedStore) Create(
	context.Context,
	Snapshot,
) error {
	return errors.New(
		"initialize agent run with PostgresStore before claiming",
	)
}

func (f FencedStore) Load(
	ctx context.Context,
	id string,
) (Snapshot, error) {
	if id != f.Lease.RunID {
		return Snapshot{}, errors.New(
			"run ID does not match claimed lease",
		)
	}

	return (PostgresStore{
		Pool: f.Pool,
	}).Load(ctx, id)
}

// Save 同时检查：
// 1. Checkpoint Revision
// 2. Run 身份
// 3. Lease Owner
// 4. Lease Epoch
// 5. Lease 是否仍然有效
func (f FencedStore) Save(
	ctx context.Context,
	s Snapshot,
	expected int64,
) error {
	if f.Pool == nil {
		return errors.New("nil postgres pool")
	}

	if s.RunID != f.Lease.RunID {
		return errors.New(
			"run ID does not match claimed lease",
		)
	}

	if err := s.Validate(); err != nil {
		return err
	}

	if s.Revision != expected+1 {
		return errors.New(
			"revision must increment by one",
		)
	}

	history, err := json.Marshal(s.History)
	if err != nil {
		return err
	}

	diagnosis, err := json.Marshal(s.Diagnosis)
	if err != nil {
		return err
	}
	verdict := s.OutcomeVerdict
	if verdict == "" {
		verdict = "not_evaluated"
	}

	tx, err := f.Pool.BeginTx(
		ctx,
		pgx.TxOptions{},
	)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		UPDATE day12.agent_runs
		SET
			status = $1,
			revision = $2,
			turns = $3,
			calls = $4,
			final_text = $5,
			finished_at = CASE WHEN $1 IN ('succeeded','failed') THEN COALESCE(finished_at, clock_timestamp()) ELSE finished_at END,
			updated_at = clock_timestamp(),
            outcome_verdict = $13,
            diagnostic_report = $14::jsonb
		WHERE run_id = $6
		  AND revision = $7
		  AND status = 'running'
		  AND repo = $8
		  AND provider = $9
		  AND goal = $10
		  AND lease_owner = $11
		  AND lease_epoch = $12
		  AND lease_until > clock_timestamp()
	`,
		s.Status,
		s.Revision,
		s.Turns,
		s.Calls,
		s.Final,
		s.RunID,
		expected,
		s.Repo,
		s.Provider,
		s.Goal,
		f.Lease.Owner,
		f.Lease.Epoch,
		verdict, string(diagnosis),
	)

	if err != nil {
		return fmt.Errorf(
			"fenced UPDATE: %w", err,
		)
	}

	if tag.RowsAffected() != 1 {
		return ErrConflict
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO day12.checkpoints(
			run_id, revision, history
		)
		VALUES($1, $2, $3::jsonb)
	`,
		s.RunID,
		s.Revision,
		string(history),
	)

	if err != nil {
		return fmt.Errorf(
			"fenced INSERT: %w", err,
		)
	}

	return tx.Commit(ctx)
}
