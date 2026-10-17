package persistence

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var ErrTaskNotFound = errors.New("task not found")
var ErrDuplicateTask = errors.New("duplicate task id")

type TaskRecord struct {
	ID                                                      int64
	Name, ToolName, State, ErrorKind, Error, Stdout, Stderr string
	ExitCode, Attempts, Retries                             int
}
type TaskStore interface {
	Create(context.Context, TaskRecord) error
	Update(context.Context, TaskRecord) error
	Get(context.Context, int64) (TaskRecord, error)
	Delete(context.Context, int64) error
}
type PostgresTaskStore struct{ db *DB }

var _ TaskStore = (*PostgresTaskStore)(nil)

func NewPostgresTaskStore(db *DB) *PostgresTaskStore { return &PostgresTaskStore{db: db} }
func (s *PostgresTaskStore) Create(ctx context.Context, t TaskRecord) error {
	_, err := s.db.Pool.Exec(ctx, `
 INSERT INTO tasks (id,name,tool_name,state,error_kind,error,stdout,stderr,exit_code,attempts,retries)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		t.ID, t.Name, t.ToolName, t.State, t.ErrorKind, t.Error, t.Stdout, t.Stderr, t.ExitCode, t.Attempts, t.Retries)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrDuplicateTask
	}
	return err
}
func (s *PostgresTaskStore) Update(ctx context.Context, t TaskRecord) error {
	tag, err := s.db.Pool.Exec(ctx, `
 UPDATE tasks SET name=$2,tool_name=$3,state=$4,error_kind=$5,error=$6,stdout=$7,stderr=$8,exit_code=$9,attempts=$10,retries=$11
 WHERE id=$1`, t.ID, t.Name, t.ToolName, t.State, t.ErrorKind, t.Error, t.Stdout, t.Stderr, t.ExitCode, t.Attempts, t.Retries)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTaskNotFound
	}
	return nil
}
func (s *PostgresTaskStore) Get(ctx context.Context, id int64) (TaskRecord, error) {
	var t TaskRecord
	err := s.db.Pool.QueryRow(ctx, `
 SELECT id,name,tool_name,state,COALESCE(error_kind,''),COALESCE(error,''),COALESCE(stdout,''),COALESCE(stderr,''),COALESCE(exit_code,0),COALESCE(attempts,0),COALESCE(retries,0)
 FROM tasks WHERE id=$1`, id).Scan(&t.ID, &t.Name, &t.ToolName, &t.State, &t.ErrorKind, &t.Error, &t.Stdout, &t.Stderr, &t.ExitCode, &t.Attempts, &t.Retries)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskRecord{}, ErrTaskNotFound
	}
	return t, err
}
func (s *PostgresTaskStore) Delete(ctx context.Context, id int64) error {
	_, err := s.db.Pool.Exec(ctx, "DELETE FROM tasks WHERE id=$1", id)
	return err
}
