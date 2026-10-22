package runtimebench

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
	"io"
	"runtime-app/internal/testdb"
	"runtime-app/observability"
	"testing"
	"time"
)

func TestDistributionNearestRankAndBounds(t *testing.T) {
	d := distribution([]float64{9, 1, 3, 2, 4})
	if d.P50 != 3 || d.P95 != 9 || d.Max != 9 {
		t.Fatal(d)
	}
	if _, e := Run(context.Background(), nil, Config{Tasks: 1, Workers: 0, Delay: time.Millisecond}); e == nil {
		t.Fatal("invalid workers accepted")
	}
}
func TestDurableBenchmarkTraceAndCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated test DB required; no SKIP")
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	var data bytes.Buffer
	provider, e := observability.NewJSONTracing(&data, "benchmark-test")
	if e != nil {
		t.Fatal(e)
	}
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(noop.NewTracerProvider())
	defer provider.Shutdown(context.Background())
	out, e := Run(ctx, pool, Config{Tasks: 6, Workers: 2, Delay: 2 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	if out.Completed != 6 || out.Failed != 0 || out.ExtraClaims != 0 || out.PeakActive < 1 || out.PeakActive > 2 || !out.Cleanup {
		t.Fatal(out)
	}
	// The final pool snapshot must be taken after both owned cleanup and the
	// guard's reserved connection have been released. These are gauges, not peaks.
	stats := out.PoolStats
	if stats.AcquireCount < int64(out.Completed) || stats.EmptyAcquireCount > stats.AcquireCount || stats.CanceledAcquireCount < 0 || stats.AcquireDurationMS < 0 || stats.EmptyAcquireWaitMS < 0 || stats.Start.Acquired != 0 || stats.End.Acquired != 0 || stats.Start.Max != out.PoolMax || stats.End.Max != out.PoolMax || stats.Scope == "" {
		t.Fatalf("invalid per-trial pool accounting: %+v", stats)
	}
	if e = provider.ForceFlush(ctx); e != nil {
		t.Fatal(e)
	}
	traces := map[string]map[string]string{}
	parents := map[string]string{}
	submitIDs := map[string]string{}
	decoder := json.NewDecoder(&data)
	for {
		var span observability.JSONSpan
		e = decoder.Decode(&span)
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		id, _ := span.Attributes["run.id"].(string)
		if id == "" {
			continue
		}
		if traces[id] == nil {
			traces[id] = map[string]string{}
		}
		traces[id][span.Name] = span.TraceID
		if span.Name == "queue.submit_tx" {
			submitIDs[id] = span.SpanID
		}
		if span.Name == "worker.execute" {
			parents[id] = span.ParentSpanID
		}
	}
	if len(traces) != 6 {
		t.Fatalf("traced runs=%d", len(traces))
	}
	for id, names := range traces {
		traceID := names["queue.submit"]
		for _, name := range []string{"queue.submit_tx", "worker.execute", "checkpoint.save"} {
			if names[name] == "" || names[name] != traceID {
				t.Fatalf("not propagated: %s %v", id, names)
			}
		}
		if parents[id] != submitIDs[id] {
			t.Fatal("worker not parented by persisted submit span")
		}
	}
	var count int
	if e = pool.QueryRow(ctx, "SELECT count(*) FROM day12.agent_runs WHERE provider='coding:benchmark'").Scan(&count); e != nil || count != 0 {
		t.Fatal("owned rows left", count, e)
	}
}

func TestPacedGoTestWorkload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("test DB required")
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	out, e := Run(ctx, pool, Config{Tasks: 4, Workers: 2, Delay: time.Millisecond, Workload: "go-test", ArrivalEvery: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	if out.ActualTestPasses != 4 || out.Arrival != "paced-stream" || len(out.FixtureSHA) != 64 || out.ExtraClaims != 0 || !out.Cleanup {
		t.Fatal(out)
	}
}
