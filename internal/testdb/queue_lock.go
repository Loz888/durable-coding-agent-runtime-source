package testdb

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
	"time"
)

// LockQueue serializes independent test fixtures sharing the hard-coded day12
// queue. Workers inside a fixture remain concurrent. A crash child borrows its
// parent's lock; it must not acquire a second one.
func LockQueue(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if os.Getenv("PRE20_PARENT_HOLDS_QUEUE_LOCK") == "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(20191920)"); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if _, err := conn.Exec(cleanup, "SELECT pg_advisory_unlock(20191920)"); err != nil {
			t.Errorf("release queue test lock: %v", err)
		}
		conn.Release()
	})
}
