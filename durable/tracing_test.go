package durable

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace/noop"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime-app/internal/testdb"
	"runtime-app/observability"
	"runtime-app/repodiag"
	"strings"
	"testing"
	"time"
)

func TestTraceWorkerProcess(t *testing.T) {
	if os.Getenv("RUNTIME_TRACE_CHILD") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated test DB required")
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	closeTrace, e := observability.ConfigureTraceFile(os.Getenv("RUNTIME_TRACE_OUTPUT"), "child-worker")
	if e != nil {
		t.Fatal(e)
	}
	jobs, e := (Queue{Pool: pool}).ClaimBatch(ctx, "trace-child", 1, 10*time.Second)
	if e != nil || len(jobs) != 1 || jobs[0].RunID != os.Getenv("RUNTIME_TRACE_RUN") {
		t.Fatal("real child claim", e)
	}
	scheduler := Scheduler{Queue: Queue{Pool: pool}, Owner: "trace-child", RenewEvery: time.Second, TTL: 10 * time.Second, RunTimeout: 5 * time.Second, Models: func(string) (repodiag.Model, error) { return nil, errors.New("no model") }, CodingRun: func(c context.Context, j ClaimedRun) (Snapshot, error) {
		f := FencedStore{Pool: pool, Lease: j.Lease}
		s, e := f.Load(c, j.RunID)
		if e != nil {
			return s, e
		}
		before := s.Revision
		s.Revision++
		s.Status = Succeeded
		s.Final = "trace fixture complete"
		return s, f.Save(c, s, before)
	}}
	scheduler.execute(ctx, jobs[0])
	if e = closeTrace(); e != nil {
		t.Fatal(e)
	}
}
func TestTracePersistenceAcrossWorkerProcess(t *testing.T) {
	pool, ctx := day13DB(t)
	var parent bytes.Buffer
	provider, e := observability.NewJSONTracing(&parent, "parent-submit")
	if e != nil {
		t.Fatal(e)
	}
	otel.SetTracerProvider(provider)
	defer otel.SetTracerProvider(noop.NewTracerProvider())
	defer provider.Shutdown(context.Background())
	id := fmt.Sprintf("trace-%d", time.Now().UnixNano())
	childOutput := filepath.Join(t.TempDir(), "child.jsonl")
	t.Cleanup(func() {
		_, e := pool.Exec(context.Background(), "DELETE FROM day12.agent_runs WHERE run_id=$1", id)
		if e != nil {
			t.Error(e)
		}
	})
	s := Snapshot{RunID: id, Repo: "synthetic-trace", Provider: "coding:trace-fixture", Goal: "trace serialization", Status: Running, History: []json.RawMessage{json.RawMessage(`{"role":"user","content":"trace fixture"}`)}}
	if e = (Queue{Pool: pool}).Submit(ctx, s, 0); e != nil {
		t.Fatal(e)
	}
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestTraceWorkerProcess$", "-test.v")
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "DEEPSEEK_API_KEY=") {
			cmd.Env = append(cmd.Env, item)
		}
	}
	cmd.Env = append(cmd.Env, "RUNTIME_TRACE_CHILD=1", "RUNTIME_TRACE_RUN="+id, "RUNTIME_TRACE_OUTPUT="+childOutput, "DEEPSEEK_API_KEY=")
	if data, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("child failed %v %s", e, data)
	}
	if e = provider.ForceFlush(ctx); e != nil {
		t.Fatal(e)
	}
	child, e := os.ReadFile(childOutput)
	if e != nil {
		t.Fatal(e)
	}
	byName := map[string]observability.JSONSpan{}
	decoder := json.NewDecoder(io.MultiReader(bytes.NewReader(parent.Bytes()), bytes.NewReader(child)))
	for {
		var span observability.JSONSpan
		e = decoder.Decode(&span)
		if e == io.EOF {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		if span.Attributes["run.id"] == id {
			byName[span.Name] = span
		}
	}
	submit, worker, checkpoint := byName["queue.submit_tx"], byName["worker.execute"], byName["checkpoint.save"]
	if submit.TraceID == "" || submit.TraceID != worker.TraceID || submit.TraceID != checkpoint.TraceID || worker.ParentSpanID != submit.SpanID || worker.Service != "child-worker" {
		t.Fatalf("cross-process trace broken: %+v", byName)
	}
	final, e := (PostgresStore{Pool: pool}).Load(ctx, id)
	if e != nil || final.Status != Succeeded || final.Revision != 1 {
		t.Fatal("child checkpoint missing", final, e)
	}
}
