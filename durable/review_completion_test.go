package durable

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime-app/reliability15"
	"testing"
	"time"
)

func TestReviewTerminalTimeFreezesForBothCheckpointStores(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		for _, status := range []Status{Succeeded, Failed} {
			t.Run(fmt.Sprintf("fenced=%t/%s", fenced, status), func(t *testing.T) {
				pool, ctx := day13DB(t)
				initial := day13Initial(t, ctx, pool, "/tmp/review-completion-fixture")
				var store Store = PostgresStore{Pool: pool}
				if fenced {
					lease, err := (LeaseManager{Pool: pool}).Claim(ctx, initial.RunID, "completion-owner", time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					store = FencedStore{Pool: pool, Lease: lease}
				}
				next := initial
				next.Status = status
				next.Final = "fixture completion"
				next.Revision++
				if err := store.Save(ctx, next, 0); err != nil {
					t.Fatal(err)
				}
				var first time.Time
				if err := pool.QueryRow(ctx, "SELECT finished_at FROM day12.agent_runs WHERE run_id=$1", initial.RunID).Scan(&first); err != nil {
					t.Fatal(err)
				}
				time.Sleep(25 * time.Millisecond)
				if err := store.Save(ctx, next, 0); !errors.Is(err, ErrConflict) {
					t.Fatal("stale save accepted", err)
				}
				var second time.Time
				if err := pool.QueryRow(ctx, "SELECT finished_at FROM day12.agent_runs WHERE run_id=$1", initial.RunID).Scan(&second); err != nil || !first.Equal(second) {
					t.Fatal("terminal time changed", err)
				}
			})
		}
	}
}
func TestReviewQueueDurationStopsAfterSuccessfulCompletion(t *testing.T) {
	pool, ctx := day13DB(t)
	q := Queue{Pool: pool}
	id := newDay12TestID()
	initial := newDay12TestSnapshot(id)
	if err := q.Submit(ctx, initial, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupDay12TestRun(t, pool, id) })
	jobs, err := q.ClaimBatch(ctx, "duration-owner", 1, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != id {
		t.Fatal(jobs, err)
	}
	stats, err := reliability15.NewStore(pool, "day12")
	if err != nil {
		t.Fatal(err)
	}
	before, err := stats.QueueSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	running, err := stats.QueueSummary(ctx)
	if err != nil || running.MeanRunSeconds <= before.MeanRunSeconds {
		t.Fatal("running duration did not advance", err)
	}
	initial.Status = Succeeded
	initial.Final = "done"
	initial.Revision++
	if err := (FencedStore{Pool: pool, Lease: jobs[0].Lease}).Save(ctx, initial, 0); err != nil {
		t.Fatal(err)
	}
	done, err := stats.QueueSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	later, err := stats.QueueSummary(ctx)
	if err != nil || done.MeanRunSeconds != later.MeanRunSeconds {
		t.Fatal("finished duration drift", done, later, err)
	}
	if later.CompletedMeasurements != 1 || later.UnknownTerminalTimes != 0 || later.MeanCompletedSeconds != done.MeanCompletedSeconds || later.MeanRunningSeconds != 0 {
		t.Fatal("completion/running metrics mixed", later)
	}
	// A historical terminal row without a timestamp is unknown, never a growing duration.
	if _, err := pool.Exec(ctx, "UPDATE day12.agent_runs SET finished_at=NULL WHERE run_id=$1", id); err != nil {
		t.Fatal(err)
	}
	historical, err := stats.QueueSummary(ctx)
	if err != nil || historical.UnknownTerminalTimes != 1 || historical.CompletedMeasurements != 0 || historical.MeanRunSeconds != 0 {
		t.Fatal("unknown historical time treated as measured", historical, err)
	}
	loaded, err := (PostgresStore{Pool: pool}).Load(ctx, id)
	if err != nil || len(loaded.History) != 1 || !json.Valid(loaded.History[0]) {
		t.Fatal("checkpoint lost", err)
	}
}

func TestReviewTerminalCheckpointInsertFailureRollsBackTimeAndVerdict(t *testing.T) {
	for _, fenced := range []bool{false, true} {
		t.Run(fmt.Sprint(fenced), func(t *testing.T) {
			pool, ctx := day13DB(t)
			initial := day13Initial(t, ctx, pool, "/tmp/terminal-rollback")
			var store Store = PostgresStore{Pool: pool}
			if fenced {
				lease, err := (LeaseManager{Pool: pool}).Claim(ctx, initial.RunID, "rollback-owner", time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				store = FencedStore{Pool: pool, Lease: lease}
			}
			if _, err := pool.Exec(ctx, "INSERT INTO day12.checkpoints(run_id,revision,history) VALUES($1,1,'[]'::jsonb)", initial.RunID); err != nil {
				t.Fatal(err)
			}
			next := initial
			next.Status = Succeeded
			next.Revision = 1
			next.Final = "must not commit"
			next.OutcomeVerdict = "insufficient_evidence"
			if err := store.Save(ctx, next, 0); err == nil {
				t.Fatal("expected checkpoint insert conflict")
			}
			var status string
			var finished *time.Time
			var verdict string
			if err := pool.QueryRow(ctx, "SELECT status,finished_at,outcome_verdict FROM day12.agent_runs WHERE run_id=$1", initial.RunID).Scan(&status, &finished, &verdict); err != nil {
				t.Fatal(err)
			}
			if status != "running" || finished != nil || verdict != "not_evaluated" {
				t.Fatal("partial terminal update committed", status, finished, verdict)
			}
		})
	}
}
