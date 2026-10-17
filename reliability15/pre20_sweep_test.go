package reliability15

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestPre20SweepLeaseMatrixAndConcurrency(t *testing.T) {
	s, ctx := newTestStore(t)
	cases := []struct {
		id, status, lease string
		epoch             int
		want              bool
	}{
		{"expired", "running", "clock_timestamp()-interval '1 second'", 3, true},
		{"released", "running", "NULL", 3, true},
		{"null-owner", "running", "clock_timestamp()+interval '1 hour'", 3, true},
		{"valid", "running", "clock_timestamp()+interval '1 hour'", 3, false},
		{"budget-left", "running", "NULL", 2, false},
		{"success", "succeeded", "NULL", 3, false},
		{"failed", "failed", "NULL", 3, false},
		{"pending", "pending", "NULL", 3, true},
	}
	for _, c := range cases {
		putRun(t, s, ctx, c.id, 3)
		owner := "'owner'"
		if c.id == "released" || c.id == "null-owner" {
			owner = "NULL"
		}
		query := fmt.Sprintf("UPDATE %s SET status=$2,lease_epoch=$3,lease_owner=%s,lease_until=%s WHERE run_id=$1", s.table("agent_runs"), owner, c.lease)
		if _, err := s.Pool.Exec(ctx, query, c.id, c.status, c.epoch); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	counts := make(chan int, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := s.SweepExhausted(context.Background(), 100)
			counts <- n
			errs <- err
		}()
	}
	wg.Wait()
	close(counts)
	close(errs)
	total := 0
	for n := range counts {
		total += n
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if total != 4 {
		t.Fatalf("swept=%d want=4", total)
	}
	for _, c := range cases {
		info, err := s.InspectRun(ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		want := c.status
		if c.want {
			want = "failed"
		}
		if info.Status != want {
			t.Fatalf("%s status=%s want=%s", c.id, info.Status, want)
		}
		if c.want && (info.FailureKind != "retry_exhausted" || info.Finished == nil) {
			t.Fatalf("incomplete terminal %+v", info)
		}
	}
	if n, err := s.SweepExhausted(ctx, 100); err != nil || n != 0 {
		t.Fatalf("repeat sweep=%d %v", n, err)
	}
	var events int
	if err := s.Pool.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s WHERE event_type='budget_exhausted'", s.table("run_events"))).Scan(&events); err != nil || events != 4 {
		t.Fatalf("failure events=%d %v", events, err)
	}
}
