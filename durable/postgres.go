package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	Pool *pgxpool.Pool
}

// Create 原子创建 Run 与 Revision 0。
func (p PostgresStore) Create(
	ctx context.Context,
	s Snapshot,
) error {
	if p.Pool == nil {
		return errors.New("nil postgres pool")
	}

	if err := s.Validate(); err != nil {
		return err
	}

	if s.Revision != 0 || s.Status != Running {
		return errors.New(
			"new run must begin at running/revision 0",
		)
	}

	history, err := json.Marshal(s.History)
	if err != nil {
		return err
	}

	tx, err := p.Pool.BeginTx(
		ctx,
		pgx.TxOptions{},
	)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(
		ctx,
		`INSERT INTO day12.agent_runs(
			run_id, repo, provider, goal,
			status, revision, turns, calls, final_text
		)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)
		ON CONFLICT (run_id) DO NOTHING`,
		s.RunID,
		s.Repo,
		s.Provider,
		s.Goal,
		s.Status,
		s.Revision,
		s.Turns,
		s.Calls,
		s.Final,
	)
	if err != nil {
		return err
	}

	if tag.RowsAffected() == 0 {
		return ErrExists
	}

	_, err = tx.Exec(
		ctx,
		`INSERT INTO day12.checkpoints(
			run_id, revision, history
		)
		VALUES($1,$2,$3::jsonb)`,
		s.RunID,
		s.Revision,
		string(history),
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// Load 读取最新已提交的 Checkpoint。
func (p PostgresStore) Load(
	ctx context.Context,
	runID string,
) (Snapshot, error) {
	if p.Pool == nil {
		return Snapshot{}, errors.New(
			"nil postgres pool",
		)
	}

	return loadSnapshot(ctx, p.Pool, runID)
}

type snapshotQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadSnapshot(ctx context.Context, db snapshotQuery, runID string) (Snapshot, error) {
	var s Snapshot
	var history []byte
	var diagnosis []byte

	err := db.QueryRow(
		ctx,
		`SELECT
			r.run_id,
			r.repo,
			r.provider,
			r.goal,
			r.status,
			r.revision,
			r.turns,
			r.calls,
			r.final_text,
			c.history, r.outcome_verdict, r.diagnostic_report
		FROM day12.agent_runs AS r
		JOIN day12.checkpoints AS c
			ON c.run_id = r.run_id
			AND c.revision = r.revision
		WHERE r.run_id = $1`,
		runID,
	).Scan(
		&s.RunID,
		&s.Repo,
		&s.Provider,
		&s.Goal,
		&s.Status,
		&s.Revision,
		&s.Turns,
		&s.Calls,
		&s.Final,
		&history, &s.OutcomeVerdict, &diagnosis,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return Snapshot{}, ErrNotFound
	}

	if err != nil {
		return Snapshot{}, err
	}

	if err := json.Unmarshal(
		history,
		&s.History,
	); err != nil {
		return Snapshot{}, fmt.Errorf(
			"decode checkpoint: %w",
			err,
		)
	}

	if err := json.Unmarshal(diagnosis, &s.Diagnosis); err != nil {
		return Snapshot{}, fmt.Errorf("decode diagnostic report: %w", err)
	}
	return s, s.Validate()
}

// Save 使用 CAS + Transaction 提交新版本。
func (p PostgresStore) Save(
	ctx context.Context,
	s Snapshot,
	expected int64,
) error {
	if p.Pool == nil {
		return errors.New(
			"nil postgres pool",
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

	tx, err := p.Pool.BeginTx(
		ctx,
		pgx.TxOptions{},
	)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(
		ctx,
		`UPDATE day12.agent_runs
		SET
			status = $1,
			revision = $2,
			turns = $3,
			calls = $4,
			final_text = $5,
			finished_at = CASE WHEN $1 IN ('succeeded','failed') THEN COALESCE(finished_at, clock_timestamp()) ELSE finished_at END,
			updated_at = clock_timestamp(),
            outcome_verdict = $11,
            diagnostic_report = $12::jsonb
WHERE
    run_id = $6
    AND revision = $7
    AND status = 'running'
    AND lease_epoch = 0
    AND queue_enabled = FALSE
    AND repo = $8
    AND provider = $9
    AND goal = $10 `,

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
		verdict, string(diagnosis),
	)
	if err != nil {
		return err
	}

	if tag.RowsAffected() != 1 {
		return ErrConflict
	}

	_, err = tx.Exec(
		ctx,
		`INSERT INTO day12.checkpoints(
			run_id, revision, history
		)
		VALUES($1,$2,$3::jsonb)`,
		s.RunID,
		s.Revision,
		string(history),
	)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}
