package modelbudget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime-app/durable"
	"runtime-app/internal/testdb"
	"runtime-app/repair18"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*pgxpool.Pool, durable.Lease, string) {
	t.Helper()
	ctx := context.Background()
	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated database required; no SKIP")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var db string
	if pool.QueryRow(ctx, "SELECT current_database()").Scan(&db) != nil || db != "agent_runtime_day14_test" {
		t.Fatal("not test db")
	}
	testdb.LockQueue(t, pool)
	for _, p := range []string{"../migrations/007_pilot_identity.sql", "../migrations/008_model_budget.sql"} {
		data, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, string(data)); e != nil {
			t.Fatal(e)
		}
	}
	id := fmt.Sprintf("budget-%d-%d", os.Getpid(), time.Now().UnixNano())
	q := durable.Queue{Pool: pool}
	err = q.Submit(ctx, durable.Snapshot{RunID: id, Repo: "/controlled-no-execution", Provider: "coding:mock", Goal: "synthetic gate", Status: durable.Running, History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"synthetic gate"}`)}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := q.ClaimBatch(ctx, "gate-owner", 1, time.Minute)
	if err != nil || len(jobs) != 1 || jobs[0].RunID != id {
		t.Fatal("real claim", jobs, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM pilot.model_attempts WHERE run_id=$1", id)
		_, _ = pool.Exec(ctx, "DELETE FROM pilot.model_budgets WHERE experiment=$1", id)
		_, _ = pool.Exec(ctx, "DELETE FROM day12.agent_runs WHERE run_id=$1", id)
	})
	return pool, jobs[0].Lease, id
}
func governed(pool *pgxpool.Pool, lease durable.Lease, id, url string) Proposer {
	return Proposer{Pool: pool, RunID: id, Owner: lease.Owner, Epoch: lease.Epoch, Next: repair18.DeepSeek{APIKey: "synthetic-key", Endpoint: url}, Policy: Policy{Experiment: id, Team: "synthetic", BudgetNano: 2000000000, GlobalLimit: 2, TeamLimit: 2, MaxRunCalls: 4, MaxProjectCalls: 200, Timeout: time.Second, UncertainFor: time.Minute}}
}
func TestKnownUsageAtomicSettlementAndOwnerGuard(t *testing.T) {
	pool, lease, id := fixture(t)
	var receives atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receives.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["reasoning_effort"] != "high" {
			t.Error("not High")
		}
		w.Header().Set("X-Request-ID", "synthetic-request")
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"old\":\"a*b\",\"new\":\"a/b\",\"reason\":\"synthetic\"}"}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`)
	}))
	defer server.Close()
	p := governed(pool, lease, id, server.URL)
	out, err := p.Suggest(context.Background(), repair18.Request{File: "math.go", Source: "a*b"})
	if err != nil || !out.UsageKnown {
		t.Fatal(out, err)
	}
	var amount int64
	var calls int
	err = pool.QueryRow(context.Background(), "SELECT reserved_nano,calls FROM pilot.model_budgets WHERE experiment=$1", id).Scan(&amount, &calls)
	if err != nil || amount != 54000 || calls != 1 {
		t.Fatal(amount, calls, err)
	}
	var attempt string
	if err = pool.QueryRow(context.Background(), "SELECT attempt_id FROM pilot.model_attempts WHERE run_id=$1", id).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	if err = p.Settle(context.Background(), attempt, out, true); err != nil {
		t.Fatal(err)
	}
	_ = pool.QueryRow(context.Background(), "SELECT reserved_nano FROM pilot.model_budgets WHERE experiment=$1", id).Scan(&amount)
	if amount != 54000 {
		t.Fatal("double refund", amount)
	}
	p.Epoch++
	if _, err = p.Suggest(context.Background(), repair18.Request{Source: "x"}); !errors.Is(err, ErrOwner) {
		t.Fatal(err)
	}
	if receives.Load() != 1 {
		t.Fatal("stale owner sent HTTP")
	}
}
func TestGateChildProcess(t *testing.T) {
	if os.Getenv("PILOT_GATE_CHILD") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DAY12_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	id := os.Getenv("PILOT_GATE_RUN")
	p := governed(pool, durable.Lease{RunID: id, Owner: "gate-owner", Epoch: 1}, id, os.Getenv("PILOT_GATE_ENDPOINT"))
	out, err := p.Suggest(ctx, repair18.Request{Source: "a*b"})
	if errors.Is(err, ErrCapacity) {
		fmt.Println("ADMISSION_BUSY")
		return
	}
	if err != nil || out.UsageKnown {
		t.Fatal("unknown outcome", err, out)
	}
	fmt.Println("RECEIVED_UNKNOWN")
}
func TestMultipleWorkerProcessesCountRealHTTPAndRetainUncertainty(t *testing.T) {
	pool, lease, id := fixture(t)
	_ = lease
	var count, active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(150 * time.Millisecond)
		fmt.Fprint(w, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"old\":\"a*b\",\"new\":\"a/b\",\"reason\":\"synthetic\"}"}}]}`)
	}))
	defer server.Close()
	var wg sync.WaitGroup
	results := make(chan string, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestGateChildProcess$", "-test.v")
			cmd.Env = append(os.Environ(), "PILOT_GATE_CHILD=1", "PILOT_GATE_RUN="+id, "PILOT_GATE_ENDPOINT="+server.URL)
			out, e := cmd.CombinedOutput()
			if e != nil {
				results <- "ERROR " + string(out)
				return
			}
			results <- string(out)
		}()
	}
	wg.Wait()
	close(results)
	admitted, busy := 0, 0
	for result := range results {
		if strings.Contains(result, "RECEIVED_UNKNOWN") {
			admitted++
		} else if strings.Contains(result, "ADMISSION_BUSY") {
			busy++
		} else {
			t.Fatal(result)
		}
	}
	if admitted != 2 || busy != 4 || count.Load() != 2 || peak.Load() > 2 {
		t.Fatal(admitted, busy, count.Load(), peak.Load())
	}
	var unknown int
	var minimum float64
	if err := pool.QueryRow(context.Background(), "SELECT count(*),MIN(extract(epoch FROM held_until-clock_timestamp())) FROM pilot.model_attempts WHERE run_id=$1 AND NOT usage_known AND charged_nano IS NULL", id).Scan(&unknown, &minimum); err != nil || unknown != 2 || minimum < 30 {
		t.Fatal(unknown, minimum, err)
	}
}
