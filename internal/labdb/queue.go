// Package labdb protects synthetic queue experiments; it is not a production API.
package labdb

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

func Lock(ctx context.Context, pool *pgxpool.Pool) (func(), error) {
	if pool == nil {
		return nil, errors.New("dedicated test pool required")
	}
	var database string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		return nil, errors.New("cannot inspect test database")
	}
	if database != "agent_runtime_day14_test" {
		return nil, errors.New("only agent_runtime_day14_test is allowed")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(20191920)"); err != nil {
		_ = conn.Conn().Close(context.Background())
		conn.Release()
		return nil, err
	}
	release := func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, e := conn.Exec(cleanup, "SELECT pg_advisory_unlock(20191920)"); e != nil {
			_ = conn.Conn().Close(cleanup)
		}
		conn.Release()
	}
	var count int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM day12.agent_runs WHERE queue_enabled AND status='running'").Scan(&count); err != nil {
		release()
		return nil, err
	}
	if count != 0 {
		release()
		return nil, errors.New("test queue must be empty; no existing runs are deleted")
	}
	return release, nil
}
