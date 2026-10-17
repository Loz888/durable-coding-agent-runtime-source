package distributed

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func newTestCoordinator(t *testing.T) (*Coordinator, *time.Time) {
	t.Helper()
	c, err := NewCoordinator(5*time.Second, 20*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return now }
	return c, &now
}
func mustRegister(t *testing.T, c *Coordinator, id string) {
	t.Helper()
	if err := c.Register(id); err != nil {
		t.Fatal(err)
	}
}
func mustEnqueue(t *testing.T, c *Coordinator, id int64) {
	t.Helper()
	if _, err := c.Enqueue(id, "uppercase", "hello"); err != nil {
		t.Fatal(err)
	}
}
func mustClaim(t *testing.T, c *Coordinator, worker string) Job {
	t.Helper()
	j, ok, err := c.Claim(worker)
	if err != nil || !ok {
		t.Fatalf("claim(%s) ok=%v err=%v", worker, ok, err)
	}
	return j
}

func TestDay10ClaimIsExclusive(t *testing.T) {
	c, _ := newTestCoordinator(t)
	mustRegister(t, c, "a")
	mustRegister(t, c, "b")
	mustEnqueue(t, c, 1)
	first := mustClaim(t, c, "a")
	if first.Epoch != 1 || first.Attempts != 1 || first.WorkerID != "a" {
		t.Fatalf("first=%+v", first)
	}
	_, ok, err := c.Claim("b")
	if err != nil || ok {
		t.Fatalf("second claim ok=%v err=%v", ok, err)
	}
}

func TestDay10ExpiredLeaseReclaimAndFencing(t *testing.T) {
	c, now := newTestCoordinator(t)
	mustRegister(t, c, "a")
	mustRegister(t, c, "b")
	mustEnqueue(t, c, 2)
	first := mustClaim(t, c, "a")
	*now = now.Add(6 * time.Second)
	second := mustClaim(t, c, "b")
	if second.Epoch != first.Epoch+1 || second.Attempts != 2 {
		t.Fatalf("wrong reclaim first=%+v second=%+v", first, second)
	}
	if _, err := c.Complete(2, "a", first.Epoch, "STALE"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale complete error=%v", err)
	}
	done, err := c.Complete(2, "b", second.Epoch, "FRESH")
	if err != nil || done.State != StateSucceeded || done.Output != "FRESH" {
		t.Fatalf("fresh complete=%+v err=%v", done, err)
	}
	if _, err := c.Complete(2, "b", second.Epoch, "AGAIN"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("duplicate complete error=%v", err)
	}
}

func TestDay10LeaseRenewal(t *testing.T) {
	c, now := newTestCoordinator(t)
	mustRegister(t, c, "a")
	mustRegister(t, c, "b")
	mustEnqueue(t, c, 3)
	job := mustClaim(t, c, "a")
	*now = now.Add(4 * time.Second)
	renewed, err := c.Renew(3, "a", job.Epoch)
	if err != nil || !renewed.LeaseUntil.Equal(now.Add(5*time.Second)) {
		t.Fatalf("renew=%+v err=%v", renewed, err)
	}
	*now = now.Add(2 * time.Second)
	_, ok, err := c.Claim("b")
	if ok || err != nil {
		t.Fatalf("unexpected reclaim ok=%v err=%v", ok, err)
	}
}

func TestDay10ExpiredAckRejectedWithoutReclaim(t *testing.T) {
	c, now := newTestCoordinator(t)
	mustRegister(t, c, "a")
	mustEnqueue(t, c, 4)
	job := mustClaim(t, c, "a")
	*now = now.Add(5 * time.Second)
	if _, err := c.Complete(4, "a", job.Epoch, "late"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("late complete error=%v", err)
	}
}

func TestDay10HeartbeatDoesNotRenewJobLease(t *testing.T) {
	c, now := newTestCoordinator(t)
	mustRegister(t, c, "a")
	mustRegister(t, c, "b")
	mustEnqueue(t, c, 5)
	a := mustClaim(t, c, "a")
	*now = now.Add(6 * time.Second)
	if err := c.Heartbeat("a"); err != nil {
		t.Fatal(err)
	}
	b := mustClaim(t, c, "b")
	if b.Epoch == a.Epoch {
		t.Fatal("heartbeat accidentally renewed lease")
	}
}

func TestDay10WorkerLiveness(t *testing.T) {
	c, now := newTestCoordinator(t)
	mustRegister(t, c, "a")
	if !c.Workers()[0].Alive {
		t.Fatal("new worker should be alive")
	}
	*now = now.Add(21 * time.Second)
	if c.Workers()[0].Alive {
		t.Fatal("stale worker shown alive")
	}
	if _, _, err := c.Claim("a"); !errors.Is(err, ErrWorkerNotAlive) {
		t.Fatalf("claim after death=%v", err)
	}
	if err := c.Heartbeat("a"); err != nil {
		t.Fatal(err)
	}
	if !c.Workers()[0].Alive {
		t.Fatal("worker did not recover after heartbeat")
	}
}

func TestDay10ConcurrentClaimAndComplete(t *testing.T) {
	c, _ := newTestCoordinator(t)
	const workers = 10
	const totalJobs = 100
	for i := 0; i < workers; i++ {
		mustRegister(t, c, string(rune('a'+i)))
	}
	for i := int64(1); i <= totalJobs; i++ {
		mustEnqueue(t, c, i)
	}
	var wg sync.WaitGroup
	got := make(chan int64, totalJobs)
	errorsCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		id := string(rune('a' + i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				job, ok, err := c.Claim(id)
				if err != nil {
					errorsCh <- err
					return
				}
				if !ok {
					return
				}
				if _, err := c.Complete(job.ID, id, job.Epoch, "OK"); err != nil {
					errorsCh <- err
					return
				}
				got <- job.ID
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	close(got)
	for err := range errorsCh {
		t.Errorf("worker error=%v", err)
	}
	seen := make(map[int64]bool)
	for id := range got {
		if seen[id] {
			t.Errorf("duplicate execution result id=%d", id)
		}
		seen[id] = true
	}
	if len(seen) != totalJobs {
		t.Fatalf("completed=%d want=%d", len(seen), totalJobs)
	}
}

func TestDay10HTTPEndToEnd(t *testing.T) {
	c, _ := newTestCoordinator(t)
	server := httptest.NewServer(c.Handler())
	defer server.Close()
	post := func(path string, body any, want int) map[string]any {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.Post(server.URL+path, "application/json", bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s status=%d want=%d", path, resp.StatusCode, want)
		}
		if want == 204 {
			return nil
		}
		var result map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	post("/workers/register", map[string]any{"worker_id": "remote-a"}, 201)
	post("/jobs", map[string]any{"id": 101, "tool_name": "uppercase", "payload": "hello agent"}, 201)
	assigned := post("/jobs/claim", map[string]any{"worker_id": "remote-a"}, 200)
	if assigned["epoch"] != float64(1) {
		t.Fatalf("claim=%v", assigned)
	}
	post("/jobs/complete", map[string]any{"worker_id": "remote-a", "job_id": 101, "epoch": 1, "output": "HELLO AGENT"}, 200)
	resp, err := http.Get(server.URL + "/jobs/101")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j Job
	if err := json.NewDecoder(resp.Body).Decode(&j); err != nil {
		t.Fatal(err)
	}
	if j.State != StateSucceeded || j.Output != "HELLO AGENT" {
		t.Fatalf("job=%+v", j)
	}
}
