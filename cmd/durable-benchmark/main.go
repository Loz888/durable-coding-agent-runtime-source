package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"runtime-app/observability"
	"runtime-app/runtimebench"
	"strconv"
	"strings"
	"time"
)

// benchmarkPoolConfig preserves pgx/DSN defaults unless an explicit bounded
// override is supplied. One connection remains reserved by labdb.Lock.
func benchmarkPoolConfig(dsn string, max int) (*pgxpool.Config, error) {
	if max != 0 && (max < 2 || max > 64) {
		return nil, fmt.Errorf("pool-max-conns must be 0 (DSN/default) or 2..64")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("test database configuration rejected")
	}
	if max != 0 {
		cfg.MaxConns = int32(max)
	}
	if cfg.MaxConns < 2 {
		return nil, fmt.Errorf("benchmark pool requires at least 2 connections; one is reserved for the queue guard")
	}
	if cfg.MinConns > cfg.MaxConns || cfg.MinIdleConns > cfg.MaxConns {
		return nil, fmt.Errorf("configured minimum pool connections exceed the benchmark pool limit")
	}
	return cfg, nil
}

func run() error {
	workload := flag.String("workload", "fixed-delay", "fixed-delay or trusted synthetic go-test")
	arrival := flag.Duration("arrival-every", 0, "0 preloads a burst; positive submits a paced stream during execution")
	tasks := flag.Int("tasks", 200, "synthetic fixed-delay tasks per trial")
	workers := flag.String("workers", "1,2,4,8", "comma-separated bounded worker counts")
	repeats := flag.Int("repetitions", 3, "trials per worker count, 1..10")
	delay := flag.Duration("delay", 20*time.Millisecond, "synthetic callback delay, 1ms..90s; whole-wave drain <=2m")
	poolMax := flag.Int("pool-max-conns", 0, "0 keeps DSN/pgx default; explicit connection limit 2..64")
	output := flag.String("output", "", "required new private JSON evidence file")
	traceFile := flag.String("trace-file", "", "optional new local OpenTelemetry NDJSON file")
	otlpEndpoint := flag.String("otlp-endpoint", "", "optional loopback OTLP base URL; default disabled")
	flag.Parse()
	if *output == "" || *repeats < 1 || *repeats > 10 {
		return fmt.Errorf("new output file and repetitions 1..10 required")
	}
	counts := []int{}
	for _, s := range strings.Split(*workers, ",") {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 64 {
			return fmt.Errorf("workers must be 1..64")
		}
		counts = append(counts, n)
	}
	if len(counts) > 8 {
		return fmt.Errorf("at most eight worker configurations")
	}
	dsn := os.Getenv("DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DAY12_TEST_DATABASE_URL must name the dedicated queue test database")
	}
	file, e := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return fmt.Errorf("output must be a new writable file")
	}
	defer file.Close()
	closeTrace, e := observability.ConfigureTelemetry(*traceFile, *otlpEndpoint, "durable-benchmark")
	if e != nil {
		return e
	}
	defer closeTrace()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	poolConfig, e := benchmarkPoolConfig(dsn, *poolMax)
	if e != nil {
		return e
	}
	pool, e := pgxpool.NewWithConfig(ctx, poolConfig)
	if e != nil {
		return fmt.Errorf("test database configuration rejected")
	}
	defer pool.Close()
	type trial struct {
		Trial  int                 `json:"trial"`
		Result runtimebench.Result `json:"result"`
	}
	results := []trial{}
	for _, n := range counts {
		for j := 1; j <= *repeats; j++ {
			v, e := runtimebench.Run(ctx, pool, runtimebench.Config{Tasks: *tasks, Workers: n, Delay: *delay, Workload: *workload, ArrivalEvery: *arrival})
			results = append(results, trial{j, v})
			if e != nil {
				_ = json.NewEncoder(file).Encode(map[string]any{"status": "FAIL", "trials": results})
				return fmt.Errorf("benchmark failed; partial evidence retained")
			}
			fmt.Printf("workers=%d trial=%d completed=%d drain_tasks_s=%.2f total_tasks_s=%.2f queue_p95_ms=%.2f\n", n, j, v.Completed, v.DrainThroughput, v.EndToEndThroughput, v.QueueWait.P95)
		}
	}
	return json.NewEncoder(file).Encode(map[string]any{"status": "PASS", "scope": "owned isolated synthetic PostgreSQL workload; no LLM or production capacity claim", "tracing_enabled": *traceFile != "" || *otlpEndpoint != "", "trials": results})
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
