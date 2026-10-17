package runtimecore

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"runtime-app/executor"
	"runtime-app/task"
)

type retryTestExecutor func(context.Context, task.Task) (executor.Output, error)

func (f retryTestExecutor) Execute(ctx context.Context, input task.Task) (executor.Output, error) {
	return f(ctx, input)
}

func TestExecuteWithRetry(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
	tests := []struct {
		name      string
		safe      bool
		failures  int
		failure   error
		wantCalls int
		wantErr   error
	}{
		{"immediate_success", true, 0, nil, 1, nil},
		{"transient_then_success", true, 2, executor.ErrTemporary, 3, nil},
		{"wrapped_transient", true, 1, fmt.Errorf("wrapped: %w", ErrTransient), 2, nil},
		{"attempts_exhausted", true, 10, executor.ErrTemporary, 3, executor.ErrTemporary},
		{"unsafe_task", false, 10, executor.ErrTemporary, 1, executor.ErrTemporary},
		{"permanent_failure", true, 10, executor.ErrToolNotFound, 1, executor.ErrToolNotFound},
		{"timeout_not_retried", true, 10, context.DeadlineExceeded, 1, context.DeadlineExceeded},
		{"cancelled_not_retried", true, 10, context.Canceled, 1, context.Canceled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			e := retryTestExecutor(func(ctx context.Context, input task.Task) (executor.Output, error) {
				calls++
				if calls <= tc.failures {
					return executor.Output{Stdout: fmt.Sprintf("failure-%d", calls), ExitCode: -1}, tc.failure
				}
				return executor.Output{Stdout: "done", ExitCode: 0}, nil
			})
			output, err, attempts := executeWithRetry(context.Background(), task.Task{ID: 1, RetrySafe: tc.safe}, e, policy)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v, want=%v", err, tc.wantErr)
			}
			if calls != tc.wantCalls || len(attempts) != tc.wantCalls {
				t.Fatalf("calls=%d attempts=%d, want=%d", calls, len(attempts), tc.wantCalls)
			}
			wantOutput := executor.Output{Stdout: "done", ExitCode: 0}
			if tc.wantErr != nil {
				wantOutput = executor.Output{Stdout: fmt.Sprintf("failure-%d", calls), ExitCode: -1}
			}
			if output != wantOutput {
				t.Fatalf("output=%+v, want=%+v", output, wantOutput)
			}
			for i, attempt := range attempts {
				expectedErr := tc.failure
				if i+1 > tc.failures {
					expectedErr = nil
				}
				if attempt.Number != i+1 || attempt.Duration < 0 ||
					attempt.ErrorKind != ClassifyError(expectedErr) || !errors.Is(attempt.Err, expectedErr) {
					t.Fatalf("attempt %d: %+v", i+1, attempt)
				}
			}
		})
	}
}

func TestRetryCancelledBeforeExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := retryTestExecutor(func(context.Context, task.Task) (executor.Output, error) {
		t.Fatal("executor started with cancelled context")
		return executor.Output{}, nil
	})
	output, err, attempts := executeWithRetry(ctx, task.Task{RetrySafe: true}, e,
		RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Second})
	if !errors.Is(err, context.Canceled) || len(attempts) != 0 || output.ExitCode != -1 {
		t.Fatalf("output=%+v error=%v attempts=%v", output, err, attempts)
	}
}

func TestRetryCancellationPreservesLastOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	last := executor.Output{Stdout: "partial output", Stderr: "temporary failure", ExitCode: 7}
	calls := 0
	e := retryTestExecutor(func(context.Context, task.Task) (executor.Output, error) {
		calls++
		return last, executor.ErrTemporary
	})
	// Cancel while waiting for the one-second backoff, not while executing.
	timer := time.AfterFunc(20*time.Millisecond, cancel)
	defer timer.Stop()
	output, err, attempts := executeWithRetry(ctx, task.Task{RetrySafe: true}, e,
		RetryPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: time.Second})
	if !errors.Is(err, context.Canceled) || calls != 1 || len(attempts) != 1 || output != last {
		t.Fatalf("output=%+v error=%v calls=%d attempts=%v", output, err, calls, attempts)
	}
}

func TestInvalidRetryPolicy(t *testing.T) {
	for _, policy := range []RetryPolicy{
		{MaxAttempts: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second},
		{MaxAttempts: 1, BaseDelay: 0, MaxDelay: time.Second},
		{MaxAttempts: 1, BaseDelay: time.Second, MaxDelay: time.Millisecond},
	} {
		e := retryTestExecutor(func(context.Context, task.Task) (executor.Output, error) {
			t.Fatal("executor started with invalid policy")
			return executor.Output{}, nil
		})
		output, err, attempts := executeWithRetry(context.Background(), task.Task{}, e, policy)
		if err == nil || len(attempts) != 0 || output.ExitCode != -1 {
			t.Fatalf("policy=%+v output=%+v error=%v attempts=%v", policy, output, err, attempts)
		}
		if r, err := NewRuntimeWithRetryPolicy(e, 1, 1, policy); err == nil || r != nil {
			t.Fatalf("invalid policy accepted: runtime=%v error=%v", r, err)
		}
	}
}

func TestRuntimeRetryIntegration(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("retry_enabled_%v", enabled), func(t *testing.T) {
			var calls atomic.Int32
			registry := executor.NewRegistry()
			e := retryTestExecutor(func(context.Context, task.Task) (executor.Output, error) {
				if calls.Add(1) < 3 {
					return executor.Output{ExitCode: -1}, executor.ErrTemporary
				}
				return executor.Output{Stdout: "recovered", ExitCode: 0}, nil
			})
			if err := registry.Register("flaky", e); err != nil {
				t.Fatal(err)
			}
			r := NewRuntime(registry, 1, 1)
			if enabled {
				var err error
				r, err = NewRuntimeWithRetryPolicy(registry, 1, 1,
					RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond})
				if err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			r.Start(ctx)
			if err := r.Submit(ctx, task.Task{ID: 42, ToolName: "flaky", RetrySafe: true}); err != nil {
				r.Close()
				t.Fatal(err)
			}
			r.Close()
			results := collectDay4Results(t, r.Results())
			if len(results) != 1 {
				t.Fatalf("results=%d, want=1", len(results))
			}
			result := results[0]
			wantCalls, wantState, wantKind := 1, StateFailed, KindTransient
			if enabled {
				wantCalls, wantState, wantKind = 3, StateSucceeded, KindNone
			}
			state, ok := r.State(42)
			if int(calls.Load()) != wantCalls || len(result.Attempts) != wantCalls ||
				result.State != wantState || result.ErrorKind != wantKind || !ok || state != wantState {
				t.Fatalf("calls=%d result=%+v stored=%s", calls.Load(), result, state)
			}
			if enabled && (result.Err != nil || result.Stdout != "recovered" || result.ExitCode != 0) {
				t.Fatalf("successful retry result=%+v", result)
			}
		})
	}
}
