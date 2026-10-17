package durable

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"runtime-app/reliability15"
	"runtime-app/repodiag"
)

func TestPre20ClaimBudgetAndConcurrency(t *testing.T) {
	for _, budget := range []int{1, 3} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			pool, ctx := day13DB(t)
			run := day14Run(t, pool, "/tmp/pre20-budget", budget)
			if _, err := pool.Exec(ctx, "UPDATE day12.agent_runs SET max_attempts=$2 WHERE run_id=$1", run.RunID, budget); err != nil {
				t.Fatal(err)
			}
			q := Queue{Pool: pool}
			for attempt := 1; attempt <= budget+1; attempt++ {
				var wg sync.WaitGroup
				outcomes := make(chan []ClaimedRun, 8)
				errs := make(chan error, 8)
				for worker := 0; worker < 8; worker++ {
					wg.Add(1)
					go func(worker int) {
						defer wg.Done()
						jobs, err := q.ClaimBatch(ctx, fmt.Sprintf("pre20-%d", worker), 1, 10*time.Second)
						outcomes <- jobs
						errs <- err
					}(worker)
				}
				wg.Wait()
				close(outcomes)
				close(errs)
				for err := range errs {
					if err != nil {
						t.Fatal(err)
					}
				}
				count := 0
				for jobs := range outcomes {
					for _, job := range jobs {
						if job.RunID != run.RunID || job.Lease.Epoch != int64(attempt) {
							t.Fatalf("unexpected claim %+v", job)
						}
						count++
					}
				}
				want := 1
				if attempt > budget {
					want = 0
				}
				if count != want {
					t.Fatalf("attempt=%d grants=%d want=%d", attempt, count, want)
				}
				if _, err := pool.Exec(ctx, "UPDATE day12.agent_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", run.RunID); err != nil {
					t.Fatal(err)
				}
			}
			var epoch, retries int
			if err := pool.QueryRow(ctx, "SELECT lease_epoch,retry_count FROM day12.agent_runs WHERE run_id=$1", run.RunID).Scan(&epoch, &retries); err != nil {
				t.Fatal(err)
			}
			if epoch != budget || retries != 0 {
				t.Fatalf("attempts=%d retries=%d", epoch, retries)
			}
		})
	}
}

func TestPre20QueueOrderingAndRetrySemantics(t *testing.T) {
	pool, ctx := day13DB(t)
	q := Queue{Pool: pool}
	low := day14Run(t, pool, "/tmp/pre20-order", 1)
	high := day14Run(t, pool, "/tmp/pre20-order", 2)
	future := day14Run(t, pool, "/tmp/pre20-order", 3)
	// Availability fixtures must not depend on the wall clock staying ahead of
	// Submit's timestamp (WSL clock corrections can move it backwards).
	// Infinities make ready/blocked states explicit without changing system time.
	if _, err := pool.Exec(ctx, `UPDATE day12.agent_runs SET priority=CASE WHEN run_id=$2 THEN 90 WHEN run_id=$3 THEN 100 ELSE 0 END,
 available_at=CASE WHEN run_id=$3 THEN 'infinity'::timestamptz ELSE '-infinity'::timestamptz END
 WHERE run_id IN ($1,$2,$3)`, low.RunID, high.RunID, future.RunID); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimBatch(ctx, "ordered-a", 1, 10*time.Second)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != high.RunID {
		t.Fatalf("priority: %+v %v", jobs, err)
	}
	if err = q.RetryLater(ctx, jobs[0].Lease, time.Hour); err != nil {
		t.Fatal(err)
	}
	var epoch, retries int
	if err = pool.QueryRow(ctx, "SELECT lease_epoch,retry_count FROM day12.agent_runs WHERE run_id=$1", high.RunID).Scan(&epoch, &retries); err != nil {
		t.Fatal(err)
	}
	if epoch != 1 || retries != 1 {
		t.Fatalf("retry double-counted: %d/%d", epoch, retries)
	}
	// A run whose available_at is ahead of the database clock must not be
	// claimed. This is also the state left by a clock correction after Submit.
	if _, err = pool.Exec(ctx, "UPDATE day12.agent_runs SET available_at='infinity'::timestamptz WHERE run_id=$1", low.RunID); err != nil {
		t.Fatal(err)
	}
	jobs, err = q.ClaimBatch(ctx, "ordered-wait", 2, 10*time.Second)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("not-yet-available: %+v %v", jobs, err)
	}
	var untouched int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM day12.agent_runs
 WHERE run_id IN ($1,$2) AND lease_epoch=0 AND retry_count=0 AND lease_owner IS NULL`, low.RunID, future.RunID).Scan(&untouched); err != nil || untouched != 2 {
		t.Fatalf("blocked claims consumed attempts: untouched=%d err=%v", untouched, err)
	}
	if _, err = pool.Exec(ctx, "UPDATE day12.agent_runs SET available_at='-infinity'::timestamptz WHERE run_id=$1", low.RunID); err != nil {
		t.Fatal(err)
	}
	jobs, err = q.ClaimBatch(ctx, "ordered-b", 2, 10*time.Second)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != low.RunID || jobs[0].Lease.Epoch != 1 || jobs[0].RetryCount != 0 {
		t.Fatalf("availability: %+v %v", jobs, err)
	}
	// The delayed high-priority run must still have only its original claim
	// and explicit retry; the empty poll must not consume either counter.
	if err = pool.QueryRow(ctx, "SELECT lease_epoch,retry_count FROM day12.agent_runs WHERE run_id=$1", high.RunID).Scan(&epoch, &retries); err != nil {
		t.Fatal(err)
	}
	if epoch != 1 || retries != 1 {
		t.Fatalf("delayed run counters changed: %d/%d", epoch, retries)
	}
	if _, err := (LeaseManager{Pool: pool}).Claim(ctx, future.RunID, "bypass", time.Second); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("manual queue bypass: %v", err)
	}
}

func TestPre20ManualClaimBudget(t *testing.T) {
	pool, ctx := day13DB(t)
	run := day13Initial(t, ctx, pool, "/tmp/pre20-manual")
	if _, err := pool.Exec(ctx, "UPDATE day12.agent_runs SET max_attempts=1 WHERE run_id=$1", run.RunID); err != nil {
		t.Fatal(err)
	}
	m := LeaseManager{Pool: pool}
	a, err := m.Claim(ctx, run.RunID, "a", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Release(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err = m.Claim(ctx, run.RunID, "b", time.Second); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("manual budget bypass: %v", err)
	}
	store, err := reliability15.NewStore(pool, "day12")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := store.SweepExhausted(ctx, 100); err != nil || n != 1 {
		t.Fatalf("manual exhaustion not finalized: %d %v", n, err)
	}
	var status string
	if err := pool.QueryRow(ctx, "SELECT status FROM day12.agent_runs WHERE run_id=$1", run.RunID).Scan(&status); err != nil || status != "failed" {
		t.Fatalf("manual final status %s %v", status, err)
	}

}

func TestPre20SchedulerMaintenanceFinalizesReleasedRun(t *testing.T) {
	pool, ctx := day13DB(t)
	q := Queue{Pool: pool}
	run := day14Run(t, pool, "/tmp/pre20-sweep", 1)
	if _, err := pool.Exec(ctx, "UPDATE day12.agent_runs SET max_attempts=1 WHERE run_id=$1", run.RunID); err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimBatch(ctx, "crashed", 1, 10*time.Second)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim: %v %+v", err, jobs)
	}
	if err = q.RetryLater(ctx, jobs[0].Lease, time.Hour); err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	scheduler := Scheduler{Queue: q, Owner: "maintenance", Workers: 1, PollEvery: 20 * time.Millisecond,
		MaintenanceEvery: 20 * time.Millisecond, TTL: time.Second, RenewEvery: 100 * time.Millisecond, RunTimeout: time.Second,
		Models: func(string) (repodiag.Model, error) {
			return nil, errors.New("exhausted run must never reach model factory")
		}}
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(runCtx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("scheduler did not stop")
		}
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		var status, kind string
		if err := pool.QueryRow(ctx, "SELECT status,COALESCE(failure_kind,'') FROM day12.agent_runs WHERE run_id=$1", run.RunID).Scan(&status, &kind); err != nil {
			t.Fatal(err)
		}
		if status == "failed" {
			if kind != "retry_exhausted" {
				t.Fatalf("failure kind=%s", kind)
			}
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("maintenance did not finalize run")
		case <-time.After(10 * time.Millisecond):
		}
	}
	store, err := reliability15.NewStore(pool, "day12")
	if err != nil {
		t.Fatal(err)
	}
	n, err := store.SweepExhausted(ctx, 100)
	if err != nil || n != 0 {
		t.Fatalf("repeat sweep %d %v", n, err)
	}
	var events int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM day12.run_events WHERE run_id=$1 AND event_type='budget_exhausted'", run.RunID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("failure events=%d %v", events, err)
	}
}

// Real child-process crash: Claim succeeds and the owner dies without RetryLater.
func TestPre20ClaimCrashProcess(t *testing.T) {
	id := os.Getenv("PRE20_CRASH_RUN")
	if id == "" {
		return
	}
	pool, ctx := day13DB(t)
	jobs, err := (Queue{Pool: pool}).ClaimBatch(ctx, "sigkill-child", 1, 10*time.Second)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != id {
		t.Fatalf("child claim: %v %+v", err, jobs)
	}
	fmt.Printf("CLAIMED %d\n", jobs[0].Lease.Epoch)
	select {}
}
func TestPre20ThreeSIGKILLsCannotBypassClaimBudget(t *testing.T) {
	pool, ctx := day13DB(t)
	run := day14Run(t, pool, "/tmp/pre20-sigkill", 1)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		child := exec.Command(binary, "-test.run=^TestPre20ClaimCrashProcess$", "-test.timeout=20s")
		child.Env = append(os.Environ(), "PRE20_CRASH_RUN="+run.RunID, "PRE20_PARENT_HOLDS_QUEUE_LOCK=1")
		stdout, err := child.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = child.Start(); err != nil {
			t.Fatal(err)
		}
		line := make(chan string, 1)
		go func() {
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				if strings.HasPrefix(scanner.Text(), "CLAIMED ") {
					line <- scanner.Text()
					return
				}
			}
			line <- ""
		}()
		select {
		case got := <-line:
			if got != "CLAIMED "+strconv.Itoa(attempt) {
				_ = child.Process.Kill()
				_ = child.Wait()
				t.Fatalf("child output %q", got)
			}
		case <-time.After(5 * time.Second):
			_ = child.Process.Kill()
			_ = child.Wait()
			t.Fatal("child claim timeout")
		}
		if err = child.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		if err = child.Wait(); err == nil {
			t.Fatal("child unexpectedly exited successfully")
		}
		if _, err = pool.Exec(ctx, "UPDATE day12.agent_runs SET lease_until=clock_timestamp()-interval '1 second' WHERE run_id=$1", run.RunID); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := (Queue{Pool: pool}).ClaimBatch(ctx, "fourth", 1, time.Second)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("fourth authorization: %v %+v", err, jobs)
	}
}
