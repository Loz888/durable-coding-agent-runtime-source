package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"runtime-app/executor"
	"runtime-app/runtimecore"
	"runtime-app/task"
)

func TestCollectorMetrics(t *testing.T) {
	c := NewCollector()

	results := []runtimecore.Result{
		{
			TaskID:    1,
			State:     runtimecore.StateSucceeded,
			ErrorKind: runtimecore.KindNone,
			Attempts: []runtimecore.Attempt{
				{
					Number:    1,
					Duration:  10 * time.Millisecond,
					ErrorKind: runtimecore.KindTransient,
				},
				{
					Number:    2,
					Duration:  20 * time.Millisecond,
					ErrorKind: runtimecore.KindNone,
				},
			},
		},
		{
			TaskID:    2,
			State:     runtimecore.StateFailed,
			ErrorKind: runtimecore.KindToolNotFound,
			Attempts: []runtimecore.Attempt{
				{
					Number:    1,
					Duration:  5 * time.Millisecond,
					ErrorKind: runtimecore.KindToolNotFound,
				},
			},
		},
		{
			TaskID:    3,
			State:     runtimecore.StateCancelled,
			ErrorKind: runtimecore.KindCancelled,
		},
	}

	for _, result := range results {
		if err := c.Record(result); err != nil {
			t.Fatal(err)
		}
	}

	got := c.Snapshot()

	if got.Completed != 3 ||
		got.Succeeded != 1 ||
		got.Failed != 1 ||
		got.Cancelled != 1 {
		t.Fatalf("unexpected task metrics: %+v", got)
	}

	if got.Attempts != 3 {
		t.Fatalf(
			"attempts=%d, want 3",
			got.Attempts,
		)
	}

	if got.Retries != 1 {
		t.Fatalf(
			"retries=%d, want 1",
			got.Retries,
		)
	}

	if got.TotalAttemptDuration != 35*time.Millisecond {
		t.Fatalf(
			"duration=%v, want 35ms",
			got.TotalAttemptDuration,
		)
	}

	if got.ErrorsByKind[runtimecore.KindNone] != 1 ||
		got.ErrorsByKind[runtimecore.KindToolNotFound] != 1 ||
		got.ErrorsByKind[runtimecore.KindCancelled] != 1 {
		t.Fatal("incorrect error kind counters")
	}
}

func TestCollectorDuplicateResult(t *testing.T) {
	c := NewCollector()

	result := runtimecore.Result{
		TaskID:    10,
		State:     runtimecore.StateSucceeded,
		ErrorKind: runtimecore.KindNone,
	}

	if err := c.Record(result); err != nil {
		t.Fatal(err)
	}

	if err := c.Record(result); err == nil {
		t.Fatal("expected duplicate result error")
	}

	if c.Snapshot().Completed != 1 {
		t.Fatal("duplicate result must not be counted")
	}
}

func TestCollectorSnapshotIsolation(t *testing.T) {
	c := NewCollector()

	if err := c.Record(runtimecore.Result{
		TaskID:    20,
		State:     runtimecore.StateSucceeded,
		ErrorKind: runtimecore.KindNone,
	}); err != nil {
		t.Fatal(err)
	}

	first := c.Snapshot()

	first.ErrorsByKind[runtimecore.KindNone] = 999

	second := c.Snapshot()

	if second.ErrorsByKind[runtimecore.KindNone] != 1 {
		t.Fatal("snapshot modified internal collector")
	}
}

func TestCollectorRejectsNonTerminal(t *testing.T) {
	c := NewCollector()

	err := c.Record(runtimecore.Result{
		TaskID: 30,
		State:  runtimecore.StateRunning,
	})

	if err == nil {
		t.Fatal("expected non-terminal state error")
	}

	if c.Snapshot().Completed != 0 {
		t.Fatal("invalid result should not be counted")
	}
}

func TestCollectorConcurrent(t *testing.T) {
	c := NewCollector()

	const n = 100

	var wg sync.WaitGroup

	errorsCh := make(chan error, n)

	for i := 1; i <= n; i++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			errorsCh <- c.Record(
				runtimecore.Result{
					TaskID:    id,
					State:     runtimecore.StateSucceeded,
					ErrorKind: runtimecore.KindNone,
					Attempts: []runtimecore.Attempt{
						{
							Number:    1,
							ErrorKind: runtimecore.KindNone,
						},
					},
				},
			)
		}(i)
	}

	wg.Wait()
	close(errorsCh)

	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	got := c.Snapshot()

	if got.Completed != n ||
		got.Succeeded != n ||
		got.Attempts != n ||
		got.Retries != 0 {
		t.Fatalf("incorrect concurrent metrics: %+v", got)
	}
}

func TestLogResultJSON(t *testing.T) {
	var buf bytes.Buffer

	logger := slog.New(
		slog.NewJSONHandler(&buf, nil),
	)

	LogResult(logger, runtimecore.Result{
		TaskID:    50,
		TaskName:  "test-task",
		State:     runtimecore.StateFailed,
		ErrorKind: runtimecore.KindToolNotFound,
		Stdout:    "private-output-do-not-log",
		Err:       errors.New("tool not found"),
	})

	var decoded map[string]any

	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded["msg"] != "task_completed" {
		t.Fatal("wrong log message")
	}

	if decoded["task_id"] != float64(50) {
		t.Fatal("wrong task_id")
	}

	if decoded["state"] != "failed" {
		t.Fatal("wrong state")
	}

	if strings.Contains(
		buf.String(),
		"private-output-do-not-log",
	) {
		t.Fatal("stdout should not be logged")
	}
}

func TestCollectorWithRuntime(t *testing.T) {
	ctx, cancel := context.WithTimeout(
		context.Background(),
		3*time.Second,
	)
	defer cancel()

	r := runtimecore.NewRuntime(
		executor.UppercaseExecutor{},
		1,
		1,
	)

	r.Start(ctx)

	job := task.Task{
		ID:      60,
		Name:    "integration-task",
		Payload: "hello agent",
	}

	if err := r.Submit(ctx, job); err != nil {
		t.Fatal(err)
	}

	r.Close()

	c := NewCollector()

	for result := range r.Results() {
		if err := c.Record(result); err != nil {
			t.Fatal(err)
		}
	}

	got := c.Snapshot()

	if got.Completed != 1 ||
		got.Succeeded != 1 ||
		got.Attempts != 1 ||
		got.Retries != 0 {
		t.Fatalf("unexpected integration metrics: %+v", got)
	}
}
