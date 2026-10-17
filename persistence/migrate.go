package persistence

import (
	"context"
	"strings"
)

// EnsureTaskSchema creates the initial table and adds the error column used by
// the collector to older day8 tables. Existing task rows are preserved.
func (db *DB) EnsureTaskSchema(ctx context.Context) error {
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var tableExists bool
	if err := tx.QueryRow(ctx, "SELECT to_regclass('tasks') IS NOT NULL").Scan(&tableExists); err != nil {
		return err
	}
	if !tableExists {
		schema := strings.Replace(TasksSchema, "CREATE TABLE tasks", "CREATE TABLE IF NOT EXISTS tasks", 1)
		if _, err := tx.Exec(ctx, schema); err != nil {
			return err
		}
	}
	var errorColumnExists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='tasks'::regclass AND attname='error' AND NOT attisdropped)").Scan(&errorColumnExists); err != nil {
		return err
	}
	if !errorColumnExists {
		if _, err := tx.Exec(ctx, "ALTER TABLE tasks ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
