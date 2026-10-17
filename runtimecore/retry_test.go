package runtimecore

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"runtime-app/executor"
	"runtime-app/task"
)

type retryUnitFlakyExecutor struct {
	mu        sync.Mutex
	calls     int
	failFirst int
}

func (f *retryUnitFlakyExecutor) Execute(
	ctx context.Context,
	t task.Task,
) (executor.Output, error) {

	if err := ctx.Err(); err != nil {
		return executor.Output{
			ExitCode: -1,
		}, err
	}

	f.mu.Lock()

	f.calls++
	current := f.calls

	f.mu.Unlock()

	if current <= f.failFirst {
		return executor.Output{
			ExitCode: -1,
		}, fmt.Errorf(
			"temporary failure %d: %w",
			current,
			executor.ErrTemporary,
		)
	}

	return executor.Output{
		Stdout:   "ok",
		ExitCode: 0,
	}, nil
}
func TestRetryBackoff(t *testing.T) {
	p := RetryPolicy{
		MaxAttempts: 5,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    400 * time.Millisecond,
	}

	cases := []struct {
		n    int
		want time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 400 * time.Millisecond},
	}

	for _, tc := range cases {
		got := p.delayAfter(tc.n)

		if got != tc.want {
			t.Errorf(
				"n=%d got=%v want=%v",
				tc.n,
				got,
				tc.want,
			)
		}
	}
}
