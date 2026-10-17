package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/repodiag"
)

// day14Run 创建一个真正入队的 Agent Run。
// 每个测试使用独立 RunID，并在结束后删除。
func day14Run(
	t *testing.T,
	pool *pgxpool.Pool,
	repo string,
	suffix int,
) Snapshot {
	t.Helper()

	ctx := context.Background()

	s := Snapshot{
		RunID: fmt.Sprintf(
			"day14-%d-%d-%d",
			os.Getpid(),
			time.Now().UnixNano(),
			suffix,
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

	queue := Queue{Pool: pool}

	if err := queue.Submit(ctx, s, 0); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		c, cancel := context.WithTimeout(
			context.Background(),
			5*time.Second,
		)
		defer cancel()

		_, err := pool.Exec(
			c,
			`DELETE FROM day12.agent_runs
			 WHERE run_id=$1`,
			s.RunID,
		)
		if err != nil {
			t.Errorf("cleanup failed: %v", err)
		}
	})

	return s
}

// Test A：两个并发领取者不能领取相同的 Run。
func TestDay14SubmitClaimNoDuplicates(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	queue := Queue{Pool: pool}

	// 测试数据库必须独立，
	// 不存在其他正在等待领取的 Run。
	for i := 0; i < 6; i++ {
		day14Run(
			t,
			pool,
			"/tmp/trusted-day14",
			i,
		)
	}

	type outcome struct {
		jobs []ClaimedRun
		err  error
	}

	results := make(chan outcome, 2)

	var wg sync.WaitGroup

	owners := []string{
		"day14-session-a",
		"day14-session-b",
	}

	for _, owner := range owners {
		wg.Add(1)

		go func(owner string) {
			defer wg.Done()

			jobs, err := queue.ClaimBatch(
				ctx,
				owner,
				6,
				15*time.Second,
			)

			results <- outcome{
				jobs: jobs,
				err:  err,
			}
		}(owner)
	}

	wg.Wait()
	close(results)

	seen := make(map[string]bool)

	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}

		for _, job := range result.jobs {
			if seen[job.RunID] {
				t.Fatalf(
					"duplicate claim: %s",
					job.RunID,
				)
			}

			seen[job.RunID] = true

			if job.Lease.Epoch != 1 {
				t.Fatalf(
					"first lease epoch=%d, want 1",
					job.Lease.Epoch,
				)
			}
		}
	}

	if len(seen) != 6 {
		t.Fatalf(
			"claimed %d runs, want 6",
			len(seen),
		)
	}

	// 所有任务都已拥有有效 Lease，
	// 第三个执行者不应再领取到它们。
	more, err := queue.ClaimBatch(
		ctx,
		"day14-session-c",
		6,
		15*time.Second,
	)

	if err != nil {
		t.Fatal(err)
	}

	if len(more) != 0 {
		t.Fatalf(
			"expected 0 available runs, got %d",
			len(more),
		)
	}
}

// Test B：RetryLater 设置的等待时间必须生效。
func TestDay14BackoffAndReclaim(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	queue := Queue{Pool: pool}

	s := day14Run(
		t,
		pool,
		"/tmp/trusted-day14",
		1,
	)

	first, err := queue.ClaimBatch(
		ctx,
		"day14-backoff-a",
		1,
		15*time.Second,
	)

	if err != nil || len(first) != 1 {
		t.Fatalf(
			"claim A: jobs=%d err=%v",
			len(first),
			err,
		)
	}

	err = queue.RetryLater(
		ctx,
		first[0].Lease,
		30*time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}

	// 任务的 available_at 还没到，
	// B 此时不能立即领取。
	now, err := queue.ClaimBatch(
		ctx,
		"day14-backoff-b",
		1,
		15*time.Second,
	)

	if err != nil || len(now) != 0 {
		t.Fatalf(
			"backoff ignored: jobs=%d err=%v",
			len(now),
			err,
		)
	}

	// 仅修改本测试创建的任务。
	// 模拟退避时间已经结束，不依赖 Sleep。
	tag, err := pool.Exec(
		ctx,
		`UPDATE day12.agent_runs
		 SET available_at =
		     clock_timestamp() - interval '1 second'
		 WHERE run_id=$1`,
		s.RunID,
	)
	if err != nil {
		t.Fatal(err)
	}

	if tag.RowsAffected() != 1 {
		t.Fatal("expected one updated test run")
	}

	again, err := queue.ClaimBatch(
		ctx,
		"day14-backoff-b",
		1,
		15*time.Second,
	)

	if err != nil || len(again) != 1 {
		t.Fatalf(
			"claim B: jobs=%d err=%v",
			len(again),
			err,
		)
	}

	if again[0].RetryCount != 1 {
		t.Fatalf(
			"retry count=%d, want 1",
			again[0].RetryCount,
		)
	}

	if again[0].Lease.Epoch !=
		first[0].Lease.Epoch+1 {
		t.Fatalf(
			"epoch not incremented",
		)
	}

	// A 已不再拥有 Lease。
	// 旧 Owner 不能安排新的重试。
	err = queue.RetryLater(
		ctx,
		first[0].Lease,
		time.Second,
	)

	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf(
			"stale owner changed retry state: %v",
			err,
		)
	}
}

// Test C：新的领取者恢复已有 ToolCall Intent。
func TestDay14ReclaimExistingCheckpoint(
	t *testing.T,
) {
	pool, ctx := day13DB(t)

	queue := Queue{Pool: pool}

	repo, workspace := day13Workspace(t)

	s := day14Run(
		t,
		pool,
		repo,
		1,
	)

	a, err := queue.ClaimBatch(
		ctx,
		"day14-recovery-a",
		1,
		15*time.Second,
	)

	if err != nil || len(a) != 1 {
		t.Fatalf(
			"claim A: jobs=%d err=%v",
			len(a),
			err,
		)
	}

	injected := errors.New(
		"stop after committed ToolCall",
	)

	runnerA := Runner{
		Store: FencedStore{
			Pool:  pool,
			Lease: a[0].Lease,
		},
		Model:     repodiag.DemoModel{},
		Workspace: workspace,
		Provider:  "demo",

		AfterSave: func(ss Snapshot) error {
			if ss.Revision == 1 {
				return injected
			}
			return nil
		},
	}

	_, err = runnerA.Run(
		ctx,
		s.RunID,
		repo,
		s.Goal,
	)

	if !errors.Is(err, injected) {
		t.Fatalf(
			"expected injected failure, got %v",
			err,
		)
	}

	before, err := (PostgresStore{
		Pool: pool,
	}).Load(ctx, s.RunID)

	if err != nil || before.Revision != 1 {
		t.Fatalf(
			"before recovery: rev=%d err=%v",
			before.Revision,
			err,
		)
	}

	// 确定性模拟 A Lease 过期。
	tag, err := pool.Exec(
		ctx,
		`UPDATE day12.agent_runs
		 SET lease_until =
		     clock_timestamp() - interval '1 second'
		 WHERE run_id=$1
		   AND lease_owner=$2
		   AND lease_epoch=$3`,
		s.RunID,
		a[0].Lease.Owner,
		a[0].Lease.Epoch,
	)

	if err != nil {
		t.Fatal(err)
	}

	if tag.RowsAffected() != 1 {
		t.Fatal("failed to expire A's lease")
	}

	b, err := queue.ClaimBatch(
		ctx,
		"day14-recovery-b",
		1,
		15*time.Second,
	)

	if err != nil || len(b) != 1 {
		t.Fatalf(
			"claim B: jobs=%d err=%v",
			len(b),
			err,
		)
	}

	if b[0].Lease.Epoch !=
		a[0].Lease.Epoch+1 {
		t.Fatal("new owner epoch did not advance")
	}

	manager := LeaseManager{Pool: pool}

	runnerB := Runner{
		Store: FencedStore{
			Pool:  pool,
			Lease: b[0].Lease,
		},
		Model:     repodiag.DemoModel{},
		Workspace: workspace,
		Provider:  "demo",

		Guard: func(c context.Context) error {
			return manager.Check(
				c,
				b[0].Lease,
			)
		},
	}

	final, err := runnerB.Run(
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
			"unexpected final state: "+
				"status=%s revision=%d turns=%d calls=%d",
			final.Status,
			final.Revision,
			final.Turns,
			final.Calls,
		)
	}

	t.Log(
		"new owner resumed committed ToolCall Intent",
	)
}

// Test D：Scheduler 自动完成已入队任务。
func TestDay14SchedulerProcessesQueuedRun(
	t *testing.T,
) {
	pool, _ := day13DB(t)

	repo, _ := day13Workspace(t)

	s := day14Run(
		t,
		pool,
		repo,
		1,
	)

	ctx, cancel := context.WithTimeout(
		context.Background(),
		45*time.Second,
	)
	defer cancel()

	scheduler := Scheduler{
		Queue: Queue{Pool: pool},

		Owner:   "day14-test-scheduler",
		Workers: 2,

		PollEvery:  50 * time.Millisecond,
		TTL:        15 * time.Second,
		RenewEvery: time.Second,
		RunTimeout: 30 * time.Second,

		Models: func(provider string) (
			repodiag.Model,
			error,
		) {
			if provider != "demo" {
				return nil, fmt.Errorf(
					"unexpected provider %q",
					provider,
				)
			}

			return repodiag.DemoModel{}, nil
		},
	}

	done := make(chan error, 1)

	go func() {
		done <- scheduler.Run(ctx)
	}()

	store := PostgresStore{Pool: pool}

	ticker := time.NewTicker(
		50 * time.Millisecond,
	)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Fatal(
				"scheduler did not complete the run",
			)

		case <-ticker.C:
			current, err := store.Load(
				ctx,
				s.RunID,
			)
			if err != nil {
				t.Fatal(err)
			}

			if current.Status == Succeeded {
				cancel()

				if err := <-done; err != nil {
					t.Fatal(err)
				}

				if current.Revision != 7 ||
					current.Calls != 3 {
					t.Fatalf(
						"final revision=%d calls=%d",
						current.Revision,
						current.Calls,
					)
				}

				t.Log(
					"scheduler completed queued run",
				)

				return
			}
		}
	}
}
