package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime-app/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 每个测试使用独立 RunID。
// 不删除其他测试或真实 Agent Run 的数据。
func newDay12TestID() string {
	return fmt.Sprintf(
		"day12-test-%d-%d",
		os.Getpid(),
		time.Now().UnixNano(),
	)
}

func newDay12TestSnapshot(id string) Snapshot {
	return Snapshot{
		RunID:    id,
		Repo:     "/tmp/trusted-repository",
		Provider: "demo",
		Goal:     "test checkpoint persistence",
		Status:   Running,
		Revision: 0,

		History: []json.RawMessage{
			json.RawMessage(
				`{"role":"user","content":"test"}`,
			),
		},
	}
}

func openDay12TestPool(
	t *testing.T,
) (*pgxpool.Pool, string) {
	t.Helper()

	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")

	if dsn == "" {
		t.Skip(
			"DAY12_TEST_DATABASE_URL not set",
		)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal("PostgreSQL not reachable: ", err)
	}

	// 明确验证迁移已经存在。
	var tableName *string

	err = pool.QueryRow(
		ctx,
		`SELECT to_regclass('day12.agent_runs')::text`,
	).Scan(&tableName)

	if err != nil || tableName == nil {
		pool.Close()
		t.Fatal(
			"day12 schema missing; execute migration first",
		)
	}

	return pool, dsn
}

func cleanupDay12TestRun(
	t *testing.T,
	pool *pgxpool.Pool,
	id string,
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(
		context.Background(),
		5*time.Second,
	)
	defer cancel()

	// checkpoints 使用 ON DELETE CASCADE。
	_, err := pool.Exec(
		ctx,
		`DELETE FROM day12.agent_runs WHERE run_id=$1`,
		id,
	)

	if err != nil {
		t.Logf(
			"warning: test row cleanup failed: %v",
			err,
		)
	}
}

// Test F：真实数据库 Create / Save / Load / CAS / 重连。
func TestDay12PostgresCheckpoint(t *testing.T) {
	pool, dsn := openDay12TestPool(t)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(
		context.Background(),
		25*time.Second,
	)
	defer cancel()

	id := newDay12TestID()

	// 数据清理在 Pool 关闭前执行。
	defer cleanupDay12TestRun(t, pool, id)

	store := PostgresStore{Pool: pool}
	initial := newDay12TestSnapshot(id)

	// 1. Create
	if err := store.Create(ctx, initial); err != nil {
		t.Fatal("create: ", err)
	}

	// 2. Duplicate Create
	err := store.Create(ctx, initial)

	if !errors.Is(err, ErrExists) {
		t.Fatalf(
			"duplicate create: want ErrExists, got %v",
			err,
		)
	}

	// 3. Load Revision 0
	loaded, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal("load revision 0: ", err)
	}

	if loaded.Revision != 0 ||
		len(loaded.History) != 1 {
		t.Fatalf(
			"unexpected initial state: %+v",
			loaded,
		)
	}

	// 4. Save Revision 1
	next := loaded
	next.Revision = 1
	next.Turns = 1
	next.Calls = 1

	next.History = append(
		append(
			[]json.RawMessage{},
			loaded.History...,
		),
		json.RawMessage(
			`{"type":"function_call",`+
				`"call_id":"call-1",`+
				`"name":"list_files",`+
				`"arguments":"{}"}`,
		),
	)

	if err := store.Save(ctx, next, 0); err != nil {
		t.Fatal("save revision 1: ", err)
	}

	// 5. Stale Revision
	err = store.Save(ctx, next, 0)

	if !errors.Is(err, ErrConflict) {
		t.Fatalf(
			"stale write: want ErrConflict, got %v",
			err,
		)
	}

	// 6. Load Revision 1
	saved, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if saved.Revision != 1 ||
		saved.Turns != 1 ||
		saved.Calls != 1 ||
		len(saved.History) != 2 {
		t.Fatalf(
			"unexpected saved state: %+v",
			saved,
		)
	}

	// 7. 真实断开旧连接，重新创建连接池。
	// 注意这里不关闭 cleanup 使用的原 Pool。
	newPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer newPool.Close()

	if err := newPool.Ping(ctx); err != nil {
		t.Fatal(err)
	}

	reopenedStore := PostgresStore{
		Pool: newPool,
	}

	recovered, err := reopenedStore.Load(ctx, id)
	if err != nil {
		t.Fatal("load after reconnect: ", err)
	}

	if recovered.Revision != 1 ||
		len(recovered.History) != 2 {
		t.Fatalf(
			"reconnect lost checkpoint: %+v",
			recovered,
		)
	}

	t.Log(
		"PASS: real PostgreSQL create, load, CAS, reconnect",
	)
}

// Test G：验证 UPDATE 成功但 INSERT 失败时事务回滚。
func TestDay12PostgresTransactionRollback(
	t *testing.T,
) {
	pool, _ := openDay12TestPool(t)
	defer pool.Close()

	ctx, cancel := context.WithTimeout(
		context.Background(),
		15*time.Second,
	)
	defer cancel()

	id := newDay12TestID()

	defer cleanupDay12TestRun(t, pool, id)

	store := PostgresStore{Pool: pool}
	initial := newDay12TestSnapshot(id)

	if err := store.Create(ctx, initial); err != nil {
		t.Fatal(err)
	}

	// 人为创建一个 Revision 1 Checkpoint，
	// 使之后 Save() 的 INSERT 出现唯一键冲突。
	//
	// 注意：这只是独立测试 RunID 中的故障注入。
	_, err := pool.Exec(
		ctx,
		`INSERT INTO day12.checkpoints(
			run_id, revision, history
		)
		VALUES($1, 1, '[]'::jsonb)`,
		id,
	)
	if err != nil {
		t.Fatal(err)
	}

	next := initial
	next.Revision = 1
	next.Turns = 1

	// UPDATE agent_runs 先执行成功，
	// INSERT checkpoints 随后因主键冲突失败。
	err = store.Save(ctx, next, 0)

	if err == nil {
		t.Fatal(
			"expected INSERT failure and rollback",
		)
	}

	// 因为事务回滚，agent_runs 仍应指向 Revision 0。
	loaded, err := store.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if loaded.Revision != 0 ||
		loaded.Turns != 0 {
		t.Fatalf(
			"transaction was not rolled back: %+v",
			loaded,
		)
	}

	t.Log(
		"PASS: transaction rolled back after checkpoint insert failure",
	)
}
