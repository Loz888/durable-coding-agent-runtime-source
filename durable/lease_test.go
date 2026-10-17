package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime-app/internal/testdb"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"runtime-app/repodiag"
)

// 连接真实 PostgreSQL。
// 没有连接参数时会 SKIP，不能算数据库验收通过。
func day13DB(
	t *testing.T,
) (*pgxpool.Pool, context.Context) {
	t.Helper()

	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")

	if dsn == "" {
		t.Skip(
			"DAY12_TEST_DATABASE_URL is not set: real PostgreSQL test NOT executed",
		)
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}

	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatal(err)
	}

	var tableName *string

	err = pool.QueryRow(
		ctx,
		`SELECT to_regclass('day12.agent_runs')::text`,
	).Scan(&tableName)

	if err != nil || tableName == nil {
		pool.Close()
		t.Fatal(
			"run migrations 001 and 002 first",
			err,
		)
	}

	t.Cleanup(pool.Close)
	var database string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	if database != "agent_runtime_day14_test" {
		t.Fatalf("refusing non-test database %q", database)
	}
	testdb.LockQueue(t, pool)
	return pool, ctx
}

// 创建独立测试 Run。
// 只清理本测试创建的数据。
func day13Initial(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	repo string,
) Snapshot {
	t.Helper()

	s := Snapshot{
		RunID: fmt.Sprintf(
			"day13-test-%d-%d",
			os.Getpid(),
			time.Now().UnixNano(),
		),

		Repo:     repo,
		Provider: "demo",
		Goal:     "diagnose",
		Status:   Running,

		History: []json.RawMessage{
			json.RawMessage(
				`{"role":"user","content":"diagnose"}`,
			),
		},
	}

	store := PostgresStore{Pool: pool}

	if err := store.Create(ctx, s); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		c, cancel := context.WithTimeout(
			context.Background(),
			4*time.Second,
		)
		defer cancel()

		_, err := pool.Exec(
			c,
			`DELETE FROM day12.agent_runs WHERE run_id=$1`,
			s.RunID,
		)

		if err != nil {
			t.Errorf(
				"cleanup failed: %v", err,
			)
		}
	})

	return s
}

// Test A：Claim / Renew / Fencing / CAS。
func TestDay13LeaseClaimRenewFencing(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	s := day13Initial(
		t, ctx, pool, "/tmp/day13-test",
	)

	m := LeaseManager{Pool: pool}

	// Worker A 首次领取。
	a, err := m.Claim(
		ctx,
		s.RunID,
		"session-a",
		3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	if a.Epoch != 1 {
		t.Fatalf(
			"epoch=%d, want 1",
			a.Epoch,
		)
	}

	// Lease 有效时，B 不能接管。
	_, err = m.Claim(
		ctx,
		s.RunID,
		"session-b",
		3*time.Second,
	)

	if !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf(
			"concurrent claim=%v, want ErrLeaseBusy",
			err,
		)
	}

	if err := m.Check(ctx, a); err != nil {
		t.Fatal(err)
	}

	// A 可以续租。
	if _, err := m.Renew(
		ctx, a, 3*time.Second,
	); err != nil {
		t.Fatal(err)
	}

	// A 在有效执行权下提交 Revision 1。
	next := s
	next.Revision = 1

	fencedA := FencedStore{
		Pool:  pool,
		Lease: a,
	}

	if err := fencedA.Save(
		ctx, next, 0,
	); err != nil {
		t.Fatal(err)
	}

	// A 主动释放。
	if err := m.Release(ctx, a); err != nil {
		t.Fatal(err)
	}

	// B 重新领取，Epoch 必须增加。
	b, err := m.Claim(
		ctx,
		s.RunID,
		"session-b",
		3*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	if b.Epoch != a.Epoch+1 {
		t.Fatalf(
			"epochs a=%d b=%d",
			a.Epoch, b.Epoch,
		)
	}

	// 旧 Worker 的 Check 必须失败。
	if err := m.Check(ctx, a); !errors.Is(
		err, ErrLeaseLost,
	) {
		t.Fatalf(
			"stale check=%v, want ErrLeaseLost",
			err,
		)
	}

	// 旧 Worker 不能继续 Renew。
	_, err = m.Renew(
		ctx, a, 3*time.Second,
	)

	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf(
			"stale renew=%v, want ErrLeaseLost",
			err,
		)
	}

	// 两个 Worker 都尝试提交 Revision 2。
	newer := next
	newer.Revision = 2

	// A 虽然持有旧 Snapshot，
	// 但 Epoch 1 已经失效。
	err = fencedA.Save(
		ctx, newer, 1,
	)

	if !errors.Is(err, ErrConflict) {
		t.Fatalf(
			"stale writer=%v, want ErrConflict",
			err,
		)
	}

	// B 使用 Epoch 2 可以提交。
	fencedB := FencedStore{
		Pool:  pool,
		Lease: b,
	}

	if err := fencedB.Save(
		ctx, newer, 1,
	); err != nil {
		t.Fatalf(
			"new writer failed: %v",
			err,
		)
	}

	got, err := (PostgresStore{
		Pool: pool,
	}).Load(ctx, s.RunID)

	if err != nil || got.Revision != 2 {
		t.Fatalf(
			"loaded=%+v err=%v",
			got, err,
		)
	}

	t.Log(
		"Claim, Renew, Epoch fencing and Revision CAS verified",
	)
}

// day13ClaimAfterExpiry lets PostgreSQL decide expiry, rather than assuming
// a fixed client Sleep means clock_timestamp() has passed lease_until.
// Only ErrLeaseBusy is retried; other errors and a bounded timeout fail the test.
func day13ClaimAfterExpiry(
	t *testing.T,
	ctx context.Context,
	manager LeaseManager,
	runID, owner string,
	ttl time.Duration,
) Lease {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

	for {
		lease, err := manager.Claim(waitCtx, runID, owner, ttl)
		if err == nil {
			return lease
		}
		if !errors.Is(err, ErrLeaseBusy) {
			t.Fatalf("claim expired lease for %s: %v", runID, err)
		}
		select {
		case <-waitCtx.Done():
			t.Fatalf("lease for %s was still busy after waiting: %v", runID, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// Test B：不续租，等待过期后接管。
func TestDay13ExpiredLeaseReclaim(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	s := day13Initial(
		t, ctx, pool, "/tmp/day13-expiry",
	)

	m := LeaseManager{Pool: pool}

	a, err := m.Claim(
		ctx,
		s.RunID,
		"session-a",
		250*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}

	// 不续租，由 Claim 的数据库时间条件决定什么时候可以接管。
	b := day13ClaimAfterExpiry(t, ctx, m, s.RunID, "session-b", 2*time.Second)

	if b.Epoch != a.Epoch+1 {
		t.Fatalf(
			"a=%d b=%d",
			a.Epoch, b.Epoch,
		)
	}

	if err := m.Check(ctx, a); !errors.Is(
		err, ErrLeaseLost,
	) {
		t.Fatal(
			"old lease should be lost:", err,
		)
	}

	t.Log(
		"expired lease reclaimed with higher epoch",
	)
}

// 创建供 Agent 使用的可信临时 Go 仓库。
func day13Workspace(
	t *testing.T,
) (string, *repodiag.Workspace) {
	t.Helper()

	dir := t.TempDir()

	files := map[string]string{
		"go.mod": "module example.com/day13fixture\n\ngo 1.22\n",

		"math.go": "package math\n" +
			"func Add(a,b int) int{return a+b}\n",

		"math_test.go": "package math\n" +
			"import \"testing\"\n" +
			"func TestAdd(t *testing.T){" +
			"if Add(1,2)!=3{t.Fatal(\"bad\")}}\n",
	}

	for name, content := range files {
		if err := os.WriteFile(
			filepath.Join(dir, name),
			[]byte(content),
			0600,
		); err != nil {
			t.Fatal(err)
		}
	}

	w, err := repodiag.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}

	return dir, w
}

// Test C：A 在 ToolCall 提交后崩溃，B 接管并恢复。
func TestDay13RunnerReclaimAfterCrash(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	repo, w := day13Workspace(t)
	s := day13Initial(t, ctx, pool, repo)

	m := LeaseManager{Pool: pool}

	a, err := m.Claim(
		ctx, s.RunID,
		"session-a",
		350*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New(
		"simulate process crash after committed tool intent",
	)

	r1 := Runner{
		Store: FencedStore{
			Pool:  pool,
			Lease: a,
		},

		Model:     repodiag.DemoModel{},
		Workspace: w,
		Provider:  "demo",

		Guard: func(c context.Context) error {
			return m.Check(c, a)
		},

		AfterSave: func(s Snapshot) error {
			if s.Revision == 1 {
				return injected
			}
			return nil
		},
	}

	_, err = r1.Run(
		ctx,
		s.RunID,
		repo,
		s.Goal,
	)

	if !errors.Is(err, injected) {
		t.Fatalf(
			"want injected stop; got %v",
			err,
		)
	}

	saved, err := (PostgresStore{
		Pool: pool,
	}).Load(ctx, s.RunID)

	if err != nil || saved.Revision != 1 {
		t.Fatalf(
			"saved=%+v err=%v",
			saved, err,
		)
	}

	// 原进程不再续租，等待数据库实际允许重新领取同一个 RunID。
	b := day13ClaimAfterExpiry(t, ctx, m, s.RunID, "session-b", 15*time.Second)

	if b.Epoch != a.Epoch+1 {
		t.Fatalf(
			"epochs a=%d b=%d",
			a.Epoch, b.Epoch,
		)
	}

	r2 := Runner{
		Store: FencedStore{
			Pool:  pool,
			Lease: b,
		},

		Model:     repodiag.DemoModel{},
		Workspace: w,
		Provider:  "demo",

		Guard: func(c context.Context) error {
			return m.Check(c, b)
		},
	}

	final, err := r2.Run(
		ctx,
		s.RunID,
		repo,
		s.Goal,
	)
	if err != nil {
		t.Fatal(err)
	}

	if final.Status != Succeeded ||
		final.Revision != 7 ||
		final.Turns != 4 ||
		final.Calls != 3 {
		t.Fatalf(
			"final=%+v",
			final,
		)
	}

	if err := m.Check(ctx, a); !errors.Is(
		err, ErrLeaseLost,
	) {
		t.Fatalf(
			"old owner alive: %v",
			err,
		)
	}

	t.Log(
		"old process lost lease; new owner resumed persisted ToolCall",
	)
}

// Test D：旧 Worker 不能释放新 Worker 的 Lease。
func TestDay13StaleReleaseCannotAffectNewOwner(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	// 创建一个专用于此测试的 Agent Run。
	s := day13Initial(
		t,
		ctx,
		pool,
		"/tmp/day13-release",
	)

	manager := LeaseManager{
		Pool: pool,
	}

	// Step 1：Worker A 首次领取。
	a, err := manager.Claim(
		ctx,
		s.RunID,
		"session-a",
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	if a.Epoch != 1 {
		t.Fatalf(
			"A epoch=%d, want 1",
			a.Epoch,
		)
	}

	// Step 2：A 主动释放。
	if err := manager.Release(
		ctx,
		a,
	); err != nil {
		t.Fatal(err)
	}

	// Step 3：B 领取同一个 Run。
	b, err := manager.Claim(
		ctx,
		s.RunID,
		"session-b",
		5*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	// B 应获得新的 Epoch。
	if b.Epoch != a.Epoch+1 {
		t.Fatalf(
			"epochs: A=%d B=%d",
			a.Epoch,
			b.Epoch,
		)
	}

	// Step 4：旧 A 再次尝试 Release。
	if err := manager.Release(
		ctx,
		a,
	); err != nil {
		t.Fatal(err)
	}

	// Step 5：检查 B 仍然拥有执行权。
	if err := manager.Check(
		ctx,
		b,
	); err != nil {
		t.Fatalf(
			"stale release affected B: %v",
			err,
		)
	}

	// Step 6：读取数据库中的真实 Owner 和 Epoch。
	var owner string
	var epoch int64

	err = pool.QueryRow(
		ctx,
		`SELECT lease_owner, lease_epoch
		 FROM day12.agent_runs
		 WHERE run_id = $1`,
		s.RunID,
	).Scan(
		&owner,
		&epoch,
	)

	if err != nil {
		t.Fatal(err)
	}

	if owner != b.Owner || epoch != b.Epoch {
		t.Fatalf(
			"lease changed unexpectedly: owner=%s epoch=%d",
			owner,
			epoch,
		)
	}

	t.Log(
		"PASS: stale Worker A cannot release Worker B lease",
	)
}
