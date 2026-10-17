package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"runtime-app/executor"
	"runtime-app/observability"
	"runtime-app/runtimecore"
	"runtime-app/task"
)

func main() {
	logger := slog.New(
		slog.NewJSONHandler(os.Stdout, nil),
	)

	registry := executor.NewRegistry()

	if err := registry.Register(
		"uppercase",
		executor.UppercaseExecutor{},
	); err != nil {
		panic(err)
	}

	if err := registry.Register(
		"shell",
		executor.ShellExecutor{
			Timeout: time.Second,
		},
	); err != nil {
		panic(err)
	}

	policy := runtimecore.RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    40 * time.Millisecond,
	}

	r, err := runtimecore.NewRuntimeWithRetryPolicy(
		registry,
		2,
		2,
		policy,
	)

	if err != nil {
		panic(err)
	}

	metrics := observability.NewCollector()

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	jobs := []task.Task{
		{
			ID:       1,
			Name:     "uppercase-task",
			ToolName: "uppercase",
			Payload:  "hello agent",
		},
		{
			ID:       2,
			Name:     "echo-task",
			ToolName: "shell",
			Command:  "echo",
			Args:     []string{"hello day6"},
		},
		{
			ID:       3,
			Name:     "fail-task",
			ToolName: "shell",
			Command:  "false",
		},
		{
			ID:       4,
			Name:     "unknown-task",
			ToolName: "missing-tool",
		},
	}

	start := time.Now()

	r.Start(ctx)

	submittedCh := make(chan int, 1)

	// Producer
	go func() {
		submitted := 0

		defer func() {
			r.Close()
			submittedCh <- submitted
		}()

		for _, job := range jobs {
			if err := r.Submit(ctx, job); err != nil {
				logger.Error(
					"submit_failed",
					"task_id", job.ID,
					"error", err,
				)
				return
			}

			submitted++
		}
	}()

	// Result Collector
	for result := range r.Results() {
		if err := metrics.Record(result); err != nil {
			logger.Error(
				"metrics_record_failed",
				"task_id", result.TaskID,
				"error", err,
			)

			continue
		}

		observability.LogResult(logger, result)
	}

	submitted := <-submittedCh

	elapsed := time.Since(start)

	snapshot := metrics.Snapshot()

	throughput := 0.0

	if elapsed > 0 {
		throughput = float64(
			snapshot.Completed,
		) / elapsed.Seconds()
	}

	avgAttemptMS := 0.0

	if snapshot.Attempts > 0 {
		avgAttemptMS = float64(
			snapshot.TotalAttemptDuration.Microseconds(),
		) / 1000 / float64(snapshot.Attempts)
	}

	fmt.Println()
	fmt.Println("===== DAY 6 METRICS =====")

	fmt.Println("submitted:", submitted)
	fmt.Println("completed:", snapshot.Completed)
	fmt.Println("succeeded:", snapshot.Succeeded)
	fmt.Println("failed:", snapshot.Failed)
	fmt.Println("cancelled:", snapshot.Cancelled)

	fmt.Println("attempts:", snapshot.Attempts)
	fmt.Println("retries:", snapshot.Retries)

	fmt.Printf(
		"throughput: %.2f tasks/s\n",
		throughput,
	)

	fmt.Printf(
		"avg attempt duration: %.3f ms\n",
		avgAttemptMS,
	)

	for kind, count := range snapshot.ErrorsByKind {
		fmt.Printf(
			"error kind %s: %d\n",
			kind,
			count,
		)
	}

	if snapshot.Completed != submitted {
		fmt.Fprintln(
			os.Stderr,
			"ERROR: completed count mismatch",
		)

		os.Exit(1)
	}

	fmt.Println("DAY 6 DEMO PASSED")
}
