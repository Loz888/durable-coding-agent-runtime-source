package durable

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"runtime-app/reliability15"
	"runtime-app/repair19"
	"runtime-app/repodiag"
)

func TestPre20HeartbeatCoversSlowInitialization(t *testing.T) {
	pool, ctx := day13DB(t)
	repo, _ := day13Workspace(t)
	run := day14Run(t, pool, repo, 1)
	q := Queue{Pool: pool}
	jobs, err := q.ClaimBatch(ctx, "slow-init", 1, 3*time.Second)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim %+v %v", jobs, err)
	}
	scheduler := Scheduler{Logf: t.Logf, Queue: q, Owner: "slow-init", TTL: 3 * time.Second, RenewEvery: 100 * time.Millisecond, RunTimeout: 10 * time.Second,
		ModelsWithContext: func(c context.Context, _ string) (repodiag.Model, error) {
			for {
				var originalExpired bool
				if err := pool.QueryRow(c, "SELECT clock_timestamp()>$1", jobs[0].Lease.Until).Scan(&originalExpired); err != nil {
					return nil, err
				}
				if originalExpired {
					if err := (LeaseManager{Pool: pool}).Check(c, jobs[0].Lease); err != nil {
						return nil, fmt.Errorf("initialization outlived initial lease without renewal: %w", err)
					}
					return repodiag.DemoModel{}, nil
				}
				select {
				case <-time.After(40 * time.Millisecond):
				case <-c.Done():
					return nil, c.Err()
				}
			}
		}}

	scheduler.execute(ctx, jobs[0])
	got, err := (PostgresStore{Pool: pool}).Load(ctx, run.RunID)
	if err != nil || got.Status != Succeeded {
		t.Fatalf("slow initialization lost execution: %+v %v", got, err)
	}
}

func TestPre20InitializationFailureStopsHeartbeat(t *testing.T) {
	pool, ctx := day13DB(t)
	run := day14Run(t, pool, "/tmp/no-init-workspace", 1)
	q := Queue{Pool: pool}
	jobs, err := q.ClaimBatch(ctx, "bad-init", 1, time.Second)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim %+v %v", jobs, err)
	}
	scheduler := Scheduler{Queue: q, Owner: "bad-init", TTL: time.Second, RenewEvery: 30 * time.Millisecond, RunTimeout: time.Second,
		ModelsWithContext: func(context.Context, string) (repodiag.Model, error) {
			return nil, errors.New("invalid provider configuration")
		}}
	done := make(chan struct{})
	go func() { scheduler.execute(ctx, jobs[0]); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("initialization cleanup did not return")
	}
	var status string
	var owner *string
	if err := pool.QueryRow(ctx, "SELECT status,lease_owner FROM day12.agent_runs WHERE run_id=$1", run.RunID).Scan(&status, &owner); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || owner != nil {
		t.Fatalf("failed init status=%s owner=%v", status, owner)
	}
	// execute cannot return until its single heartbeat goroutine has exited.
}

func TestPre20LeaseLossCancelsInitialization(t *testing.T) {
	pool, ctx := day13DB(t)
	q := Queue{Pool: pool}
	run := day14Run(t, pool, "/tmp/no-followup-operations", 1)
	jobs, err := q.ClaimBatch(ctx, "initializing-a", 1, time.Second)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim %+v %v", jobs, err)
	}
	started := make(chan struct{})
	scheduler := Scheduler{Queue: q, Owner: "initializing-a", TTL: time.Second, RenewEvery: 30 * time.Millisecond, RunTimeout: 5 * time.Second,
		ModelsWithContext: func(c context.Context, _ string) (repodiag.Model, error) {
			close(started)
			<-c.Done()
			return nil, c.Err()
		}}
	done := make(chan struct{})
	go func() { scheduler.execute(ctx, jobs[0]); close(done) }()
	<-started
	if _, err := pool.Exec(ctx, "UPDATE day12.agent_runs SET lease_owner='new-owner',lease_epoch=lease_epoch+1 WHERE run_id=$1", run.RunID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("lost lease did not cancel model initialization")
	}
	var owner, status string
	var retries int
	if err := pool.QueryRow(ctx, "SELECT lease_owner,status,retry_count FROM day12.agent_runs WHERE run_id=$1", run.RunID).Scan(&owner, &status, &retries); err != nil {
		t.Fatal(err)
	}
	if owner != "new-owner" || status != "running" || retries != 0 {
		t.Fatalf("old owner mutated new lease: %s %s %d", owner, status, retries)
	}
}

func TestPre20SchedulerFailurePolicy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want reliability15.FailureKind
	}{
		{"rate-limit", &repodiag.APIError{StatusCode: 429}, reliability15.FailureTransient},
		{"upstream", &repodiag.APIError{StatusCode: 503}, reliability15.FailureTransient},
		{"auth", &repodiag.APIError{StatusCode: 401}, reliability15.FailurePermanent},
		{"database", &pgconn.PgError{Code: "08006"}, reliability15.FailureTransient},
		{"invalid-sql", &pgconn.PgError{Code: "42601"}, reliability15.FailurePermanent},
		{"lease", ErrLeaseLost, reliability15.FailureLeaseLost},
		{"patch-lease", repair19.ErrLeaseLost, reliability15.FailureLeaseLost},
		{"revision", ErrConflict, reliability15.FailureConflict},
		{"budget", repair19.ErrBudget, "repair_budget_exhausted"},
		{"patch", repair19.ErrConflict, "patch_conflict"},
		{"invalid-patch", errors.New("invalid patch"), reliability15.FailurePermanent},
		{"shutdown", context.Canceled, reliability15.FailureInterrupted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reliability15.ClassifyFailure(schedulerFailure(c.err)); got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestPre20SchedulerRetryUsesRealSchema(t *testing.T) {
	pool, ctx := day13DB(t)
	q := Queue{Pool: pool}
	run := day14Run(t, pool, "/tmp/transient", 1)
	jobs, err := q.ClaimBatch(ctx, "retry-worker", 1, 10*time.Second)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim %+v %v", jobs, err)
	}
	scheduler := Scheduler{Queue: q}
	scheduler.finishError(jobs[0], &repodiag.APIError{StatusCode: 503})
	var status string
	var retries, epoch int
	if err := pool.QueryRow(ctx, "SELECT status,retry_count,lease_epoch FROM day12.agent_runs WHERE run_id=$1", run.RunID).Scan(&status, &retries, &epoch); err != nil {
		t.Fatal(err)
	}
	if status != "running" || retries != 1 || epoch != 1 {
		t.Fatalf("retry state=%s retries=%d attempts=%d", status, retries, epoch)
	}
	if _, err := pool.Exec(ctx, "UPDATE day12.agent_runs SET available_at=clock_timestamp()-interval '1 second' WHERE run_id=$1", run.RunID); err != nil {
		t.Fatal(err)
	}
	jobs, err = q.ClaimBatch(ctx, "retry-worker-b", 1, 10*time.Second)
	if err != nil || len(jobs) != 1 || jobs[0].Lease.Epoch != 2 {
		t.Fatalf("retry was not claimable %+v %v", jobs, err)
	}
	scheduler.finishError(jobs[0], repair19.ErrBudget)
	var kind string
	if err := pool.QueryRow(ctx, "SELECT status,failure_kind FROM day12.agent_runs WHERE run_id=$1", run.RunID).Scan(&status, &kind); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || kind != "repair_budget_exhausted" {
		t.Fatalf("budget failure %s/%s", status, kind)
	}
}

func TestPre20MaintenanceErrorDoesNotStopScheduler(t *testing.T) {
	pool, err := pgxpool.New(context.Background(), "postgres://localhost/unused_test")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	logs := make(chan string, 8)
	scheduler := Scheduler{Queue: Queue{Pool: pool}, Owner: "closed-pool", Workers: 1,
		PollEvery: 20 * time.Millisecond, MaintenanceEvery: 20 * time.Millisecond, TTL: time.Second, RenewEvery: 100 * time.Millisecond, RunTimeout: time.Second,
		Models: func(string) (repodiag.Model, error) { return repodiag.DemoModel{}, nil },
		Logf: func(format string, args ...any) {
			select {
			case logs <- format:
			default:
			}
		}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	observed := false
	for !observed {
		select {
		case line := <-logs:
			observed = strings.HasPrefix(line, "SWEEP_ERROR")
		case err := <-done:
			t.Fatalf("maintenance stopped scheduler: %v", err)
		case <-deadline.C:
			t.Fatal("maintenance failure was not logged")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("scheduler stopped after maintenance error: %v", err)
	default:
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not cancel")
	}
}
