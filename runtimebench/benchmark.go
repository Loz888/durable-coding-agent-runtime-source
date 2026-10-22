// Package runtimebench measures the existing durable scheduler with synthetic
// fixed-delay work. It does not measure LLM throughput or code-repair quality.
package runtimebench

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime-app/durable"
	"runtime-app/internal/labdb"
	"runtime-app/repair17"
	"runtime-app/repodiag"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	Workload       string
	ArrivalEvery   time.Duration
	Tasks, Workers int
	Delay          time.Duration
}
type Distribution struct {
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	Max float64 `json:"max_ms"`
}

// PoolConnections is an instantaneous pool snapshot, not a sampled peak.
type PoolConnections struct {
	Acquired     int32 `json:"acquired"`
	Idle         int32 `json:"idle"`
	Constructing int32 `json:"constructing"`
	Total        int32 `json:"total"`
	Max          int32 `json:"max"`
}

// PoolStats contains cumulative-counter deltas for this Run invocation. Successful
// acquisition durations include pool overhead; only EmptyAcquireWaitMS measures
// the successful waits when no connection was immediately available. Canceled
// acquisitions have their own count and are not included in either duration.
type PoolStats struct {
	Scope                string          `json:"scope"`
	AcquireCount         int64           `json:"successful_acquire_count"`
	AcquireDurationMS    float64         `json:"successful_acquire_duration_ms"`
	EmptyAcquireCount    int64           `json:"empty_successful_acquire_count"`
	EmptyAcquireWaitMS   float64         `json:"empty_successful_acquire_wait_ms"`
	CanceledAcquireCount int64           `json:"canceled_acquire_count"`
	NewConnections       int64           `json:"new_connections"`
	LifetimeDestroyed    int64           `json:"lifetime_destroyed_connections"`
	IdleDestroyed        int64           `json:"idle_destroyed_connections"`
	Start                PoolConnections `json:"start_connections"`
	End                  PoolConnections `json:"end_connections"`
}

func poolConnections(s *pgxpool.Stat) PoolConnections {
	return PoolConnections{Acquired: s.AcquiredConns(), Idle: s.IdleConns(), Constructing: s.ConstructingConns(), Total: s.TotalConns(), Max: s.MaxConns()}
}

func poolStatsDelta(before, after *pgxpool.Stat) PoolStats {
	return PoolStats{
		Scope:                "pool-wide per-Run counter deltas including queue guard, execution and owned cleanup; start/end connection gauges, not peaks; no unrelated pool users permitted",
		AcquireCount:         after.AcquireCount() - before.AcquireCount(),
		AcquireDurationMS:    float64(after.AcquireDuration()-before.AcquireDuration()) / float64(time.Millisecond),
		EmptyAcquireCount:    after.EmptyAcquireCount() - before.EmptyAcquireCount(),
		EmptyAcquireWaitMS:   float64(after.EmptyAcquireWaitTime()-before.EmptyAcquireWaitTime()) / float64(time.Millisecond),
		CanceledAcquireCount: after.CanceledAcquireCount() - before.CanceledAcquireCount(),
		NewConnections:       after.NewConnsCount() - before.NewConnsCount(),
		LifetimeDestroyed:    after.MaxLifetimeDestroyCount() - before.MaxLifetimeDestroyCount(),
		IdleDestroyed:        after.MaxIdleDestroyCount() - before.MaxIdleDestroyCount(),
		Start:                poolConnections(before), End: poolConnections(after),
	}
}

type Result struct {
	PoolStats            PoolStats    `json:"pool_stats"`
	RunTimeoutMS         float64      `json:"run_timeout_ms"`
	LeaseTTLMS           float64      `json:"lease_ttl_ms"`
	RenewEveryMS         float64      `json:"renew_every_ms"`
	Arrival              string       `json:"arrival_mode"`
	ArrivalMS            float64      `json:"arrival_interval_ms"`
	FixtureSHA           string       `json:"fixture_sha256,omitempty"`
	ActualTestPasses     int64        `json:"actual_go_test_passes"`
	CachePolicy          string       `json:"cache_policy"`
	TimingMethod         string       `json:"timing_method"`
	DBWallClockAnomalies int          `json:"db_wall_clock_anomalies"`
	DatabaseQueueWait    Distribution `json:"database_queue_wait"`
	DatabaseService      Distribution `json:"database_claim_to_terminal"`
	DatabaseEndToEnd     Distribution `json:"database_created_to_terminal"`
	ExecutionErrors      int64        `json:"execution_errors"`

	FailureCode        string       `json:"failure_code,omitempty"`
	InvalidMeasurement []float64    `json:"invalid_measurement_ms,omitempty"`
	Tasks              int          `json:"tasks"`
	Workers            int          `json:"workers"`
	DelayMS            float64      `json:"synthetic_delay_ms"`
	Completed          int          `json:"completed"`
	Failed             int          `json:"failed"`
	PeakActive         int64        `json:"peak_active"`
	ExtraClaims        int          `json:"extra_claims"`
	SubmitMS           float64      `json:"submit_ms"`
	DrainMS            float64      `json:"drain_ms"`
	TotalMS            float64      `json:"total_ms"`
	DrainThroughput    float64      `json:"drain_tasks_per_second"`
	EndToEndThroughput float64      `json:"total_tasks_per_second"`
	QueueWait          Distribution `json:"submit_to_dispatch_monotonic"`
	Service            Distribution `json:"dispatch_to_commit_monotonic"`
	EndToEnd           Distribution `json:"submit_to_commit_monotonic"`
	GoVersion          string       `json:"go_version"`
	OS                 string       `json:"os"`
	Architecture       string       `json:"architecture"`
	LogicalCPUs        int          `json:"visible_cpus"`
	GOMAXPROCS         int          `json:"gomaxprocs"`
	PGVersion          string       `json:"postgres_version"`
	PoolMax            int32        `json:"pool_max_connections"`
	Workload           string       `json:"workload"`
	Cleanup            bool         `json:"owned_rows_cleaned"`
}

func distribution(values []float64) Distribution {
	sort.Float64s(values)
	if len(values) == 0 {
		return Distribution{}
	}
	return Distribution{values[int(math.Ceil(float64(len(values))*.5))-1], values[int(math.Ceil(float64(len(values))*.95))-1], values[len(values)-1]}
}

// normalizeConfig validates integer bounds before duration arithmetic, then uses
// whole worker waves to include a final partial batch in the drain estimate.
func normalizeConfig(c Config) (Config, error) {
	if c.Workload == "" {
		c.Workload = "fixed-delay"
	}
	if c.Tasks < 1 || c.Tasks > 10000 || c.Workers < 1 || c.Workers > 64 || c.Delay < time.Millisecond || c.Delay > 90*time.Second {
		return c, errors.New("bounded tasks 1..10000, workers 1..64 and delay 1ms..90s required")
	}
	if (c.Workload != "fixed-delay" && c.Workload != "go-test") || c.ArrivalEvery < 0 || c.ArrivalEvery > time.Second || time.Duration(c.Tasks)*c.ArrivalEvery > 2*time.Minute || (c.Workload == "go-test" && (c.Tasks > 128 || c.Workers > 8)) {
		return c, errors.New("bounded workload and arrival policy required")
	}
	waves := (c.Tasks + c.Workers - 1) / c.Workers
	if time.Duration(waves)*c.Delay > 2*time.Minute {
		return c, errors.New("estimated whole-wave drain must be <=2m")
	}
	return c, nil
}

func callbackTimeout(c Config) time.Duration {
	timeout := 35 * time.Second
	if c.Workload == "fixed-delay" && c.Delay+5*time.Second > timeout {
		timeout = c.Delay + 5*time.Second
	}
	return timeout
}

func Run(ctx context.Context, pool *pgxpool.Pool, c Config) (out Result, err error) {
	c, err = normalizeConfig(c)
	if err != nil {
		return out, err
	}
	// labdb.Lock reserves one connection until this invocation exits.
	if pool == nil || pool.Config().MaxConns < 2 {
		return out, errors.New("dedicated test pool with at least two connections required")
	}
	poolBefore := pool.Stat()
	defer func() { out.PoolStats = poolStatsDelta(poolBefore, pool.Stat()) }()
	unlock, err := labdb.Lock(ctx, pool)
	if err != nil {
		return out, err
	}
	defer unlock()
	var nonce [12]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return out, err
	}
	prefix := "bench-" + hex.EncodeToString(nonce[:])
	ids := make([]string, 0, c.Tasks)
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_, e := pool.Exec(cleanup, "DELETE FROM day12.agent_runs WHERE run_id=ANY($1)", ids)
		out.Cleanup = e == nil
		if err == nil && e != nil {
			err = errors.New("owned benchmark cleanup failed")
		}
	}()
	out = Result{Tasks: c.Tasks, Workers: c.Workers, DelayMS: float64(c.Delay) / float64(time.Millisecond), GoVersion: runtime.Version(), OS: runtime.GOOS, Architecture: runtime.GOARCH, LogicalCPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), PoolMax: pool.Config().MaxConns, Workload: "synthetic fixed-delay callback + real PG Queue/Scheduler/FencedStore; no model or go-test execution", RunTimeoutMS: float64(callbackTimeout(c)) / float64(time.Millisecond), LeaseTTLMS: 30000, RenewEveryMS: 1000}
	if err = pool.QueryRow(ctx, "SHOW server_version").Scan(&out.PGVersion); err != nil {
		return out, err
	}
	q := durable.Queue{Pool: pool}
	out.TimingMethod = "v2: Go monotonic intervals in one benchmark process; raw DB wall-clock measurements retained separately"
	submitted := make(map[string]time.Time, c.Tasks)
	var metricLock sync.Mutex
	var monotonicWait, monotonicService, monotonicTotal []float64
	var recorded, callbackErrors atomic.Int64
	started := time.Now()
	for n := 0; n < c.Tasks; n++ {
		ids = append(ids, fmt.Sprintf("%s-%05d", prefix, n))
	}
	admit := func() error {
		for n, id := range ids {
			if n > 0 && c.ArrivalEvery > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(c.ArrivalEvery):
				}
			}
			metricLock.Lock()
			submitted[id] = time.Now()
			metricLock.Unlock()
			if e := q.Submit(ctx, durable.Snapshot{RunID: id, Repo: "synthetic-benchmark", Provider: "coding:benchmark", Goal: "bounded benchmark task", Status: durable.Running, History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"synthetic benchmark"}`)}}, 0); e != nil {
				return e
			}
		}
		out.SubmitMS = float64(time.Since(started)) / float64(time.Millisecond)
		return nil
	}
	out.Arrival = "preloaded-burst"
	out.ArrivalMS = float64(c.ArrivalEvery) / float64(time.Millisecond)
	var fixtureRoot, fixtureSource string
	var testPasses atomic.Int64
	if c.Workload == "go-test" {
		fixtureRoot, err = os.MkdirTemp("", "runtime-benchmark-")
		if err != nil {
			return out, err
		}
		defer os.RemoveAll(fixtureRoot)
		fixtureSource = filepath.Join(fixtureRoot, "source")
		if err = os.Mkdir(fixtureSource, 0700); err != nil {
			return out, err
		}
		fixture := map[string]string{"go.mod": "module example.invalid/benchfixture\n\ngo 1.23\n", "sum.go": "package fixture\nfunc Sum(v []int)int{s:=0;for _,x:=range v{s+=x};return s}\n", "sum_test.go": "package fixture\nimport \"testing\"\nfunc TestSum(t *testing.T){for n:=0;n<100;n++{if Sum([]int{n,-n,7})!=7{t.Fatal(n)}}}\n"}
		raw, _ := json.Marshal(fixture)
		h := sha256.Sum256(raw)
		out.FixtureSHA = hex.EncodeToString(h[:])
		for name, data := range fixture {
			if err = os.WriteFile(filepath.Join(fixtureSource, name), []byte(data), 0600); err != nil {
				return out, err
			}
		}
		for _, args := range [][]string{{"init", "-q"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "benchmark fixture"}} {
			cmd := exec.CommandContext(ctx, "git", append([]string{"-C", fixtureSource}, args...)...)
			if e := cmd.Run(); e != nil {
				return out, errors.New("benchmark fixture initialization failed")
			}
		}
		out.Workload = "trusted synthetic Worktree Prepare + real go test -count=1 + fenced checkpoint; no model"
	}
	out.CachePolicy = "Go build cache shared/warmable, test-result cache disabled (-count=1); no cache flushing"
	if c.ArrivalEvery == 0 {
		if err = admit(); err != nil {
			return out, err
		}
	} else {
		out.Arrival = "paced-stream"
	}

	var active, peak atomic.Int64
	scheduler := durable.Scheduler{Queue: q, Owner: prefix, Workers: c.Workers, PollEvery: 10 * time.Millisecond, TTL: 30 * time.Second, RenewEvery: time.Second, RunTimeout: callbackTimeout(c),
		Models: func(string) (repodiag.Model, error) { return nil, errors.New("benchmark must never call a model") },
		CodingRun: func(call context.Context, job durable.ClaimedRun) (snapshot durable.Snapshot, callErr error) {
			dispatch := time.Now()
			defer func() {
				committed := time.Now()
				if callErr != nil {
					callbackErrors.Add(1)
				}
				metricLock.Lock()
				monotonicWait = append(monotonicWait, float64(dispatch.Sub(submitted[job.RunID]))/float64(time.Millisecond))
				monotonicService = append(monotonicService, float64(committed.Sub(dispatch))/float64(time.Millisecond))
				monotonicTotal = append(monotonicTotal, float64(committed.Sub(submitted[job.RunID]))/float64(time.Millisecond))
				metricLock.Unlock()
				recorded.Add(1)
			}()
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			if c.Workload == "go-test" {
				w, e := repair17.Prepare(call, fixtureSource, filepath.Join(fixtureRoot, "worktrees"), job.RunID)
				if e != nil {
					return snapshot, e
				}
				verdict, e := repair17.VerifyGo(call, w.Path, 30*time.Second)
				if e != nil {
					return snapshot, e
				}
				if !verdict.Passed {
					return snapshot, errors.New("benchmark trusted tests failed")
				}
				testPasses.Add(1)
			} else {
				timer := time.NewTimer(c.Delay)
				defer timer.Stop()
				select {
				case <-call.Done():
					return snapshot, call.Err()
				case <-timer.C:
				}
			}

			store := durable.FencedStore{Pool: pool, Lease: job.Lease}
			s, e := store.Load(call, job.RunID)
			if e != nil {
				return s, e
			}
			before := s.Revision
			s.Revision++
			s.Status = durable.Succeeded
			s.Final = "synthetic benchmark completed"
			return s, store.Save(call, s, before)
		}}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	drainStart := time.Now()
	go func() { done <- scheduler.Run(runCtx) }()
	defer func() {
		cancel()
		select {
		case e := <-done:
			if err == nil && e != nil {
				err = e
			}
		case <-time.After(10 * time.Second):
			if err == nil {
				err = errors.New("benchmark scheduler did not stop")
			}
		}
	}()
	if c.ArrivalEvery > 0 {
		if err = admit(); err != nil {
			return out, err
		}
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err = pool.QueryRow(ctx, "SELECT count(*) FILTER(WHERE status='succeeded'),count(*) FILTER(WHERE status='failed') FROM day12.agent_runs WHERE run_id=ANY($1)", ids).Scan(&out.Completed, &out.Failed); err != nil {
			return out, err
		}
		if out.Completed+out.Failed == c.Tasks {
			break
		}
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-ticker.C:
		}
	}
	for recorded.Load() < int64(c.Tasks) && out.Failed == 0 && callbackErrors.Load() == 0 {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-ticker.C:
		}
	}
	out.ActualTestPasses = testPasses.Load()
	out.ExecutionErrors = callbackErrors.Load()
	out.DrainMS = float64(time.Since(drainStart)) / float64(time.Millisecond)
	out.TotalMS = float64(time.Since(started)) / float64(time.Millisecond)
	out.PeakActive = peak.Load()
	out.DrainThroughput = float64(out.Completed) / (out.DrainMS / 1000)
	out.EndToEndThroughput = float64(out.Completed) / (out.TotalMS / 1000)
	rows, e := pool.Query(ctx, "SELECT EXTRACT(EPOCH FROM(first_claimed_at-created_at))*1000,EXTRACT(EPOCH FROM(finished_at-first_claimed_at))*1000,EXTRACT(EPOCH FROM(finished_at-created_at))*1000,lease_epoch FROM day12.agent_runs WHERE run_id=ANY($1)", ids)
	if e != nil {
		out.FailureCode = "measure_query"
		return out, e
	}
	defer rows.Close()
	var waits, services, totals []float64
	for rows.Next() {
		var w, s, t float64
		var epoch int
		if e = rows.Scan(&w, &s, &t, &epoch); e != nil {
			out.FailureCode = "measure_scan"
			return out, e
		}
		if w < 0 || s < 0 || t < 0 {

			out.DBWallClockAnomalies++
			if len(out.InvalidMeasurement) == 0 {
				out.InvalidMeasurement = []float64{w, s, t}
			}
		}
		waits = append(waits, w)
		services = append(services, s)
		totals = append(totals, t)
		out.ExtraClaims += epoch - 1
	}
	if e = rows.Err(); e != nil {
		out.FailureCode = "measure_rows"
		return out, e
	}
	out.DatabaseQueueWait = distribution(waits)
	out.DatabaseService = distribution(services)
	out.DatabaseEndToEnd = distribution(totals)
	metricLock.Lock()
	out.QueueWait = distribution(monotonicWait)
	out.Service = distribution(monotonicService)
	out.EndToEnd = distribution(monotonicTotal)
	measured := len(monotonicTotal)
	metricLock.Unlock()
	if measured != c.Tasks {
		return out, errors.New("missing monotonic completion measurements")
	}
	if (c.Workload == "go-test" && out.ActualTestPasses != int64(c.Tasks)) || out.Completed != c.Tasks || out.Failed != 0 || out.ExtraClaims != 0 || out.PeakActive > int64(c.Workers) || out.ExecutionErrors != 0 {
		return out, errors.New("benchmark correctness invariant failed")
	}
	return out, nil
}
