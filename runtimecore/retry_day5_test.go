package runtimecore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"runtime-app/executor"
	"runtime-app/task"
)

// flakyExecutor 模拟前 N 次失败，之后成功。
type flakyExecutor struct {
	mu        sync.Mutex
	calls     int
	failFirst int

	// 用于确定性地协调取消测试。
	firstAttemptFinished chan struct{}
}

func (f *flakyExecutor) Execute(
	ctx context.Context,
	t task.Task,
) (executor.Output, error) {

	if err := ctx.Err(); err != nil {
		return executor.Output{ExitCode: -1}, err
	}

	f.mu.Lock()
	f.calls++
	current := f.calls
	f.mu.Unlock()

	if current <= f.failFirst {

		// 只在第一次失败时发出信号。
		if current == 1 && f.firstAttemptFinished != nil {
			close(f.firstAttemptFinished)
		}

		return executor.Output{
			ExitCode: -1,
		}, fmt.Errorf(
			"temporary failure %d: %w",
			current,
			ErrTransient,
		)
	}

	return executor.Output{
		Stdout:   "ok",
		ExitCode: 0,
	}, nil
}

func (f *flakyExecutor) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func day5Policy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    40 * time.Millisecond,
	}
}

// 1. 验证指数退避计算。
func TestDay5RetryBackoff(t *testing.T) {
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
				"attempt=%d got=%v want=%v",
				tc.n, got, tc.want,
			)
		}
	}
}

// 2. 前两次失败，第三次成功。
func TestDay5RetryThirdSuccess(t *testing.T) {
	fake := &flakyExecutor{failFirst: 2}

	output, err, attempts := executeWithRetry(
		context.Background(),
		task.Task{
			ID:        100,
			Name:      "retry-task",
			RetrySafe: true,
		},
		fake,
		day5Policy(),
	)

	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}

	if output.Stdout != "ok" || output.ExitCode != 0 {
		t.Fatalf("unexpected output: %+v", output)
	}

	if fake.Calls() != 3 {
		t.Fatalf("calls=%d, want 3", fake.Calls())
	}

	if len(attempts) != 3 {
		t.Fatalf("attempts=%d, want 3", len(attempts))
	}

	for i := 0; i < 2; i++ {
		if attempts[i].Number != i+1 {
			t.Errorf("unexpected attempt number: %d", attempts[i].Number)
		}

		if !errors.Is(attempts[i].Err, ErrTransient) {
			t.Errorf(
				"attempt %d should be transient: %v",
				i+1,
				attempts[i].Err,
			)
		}

		if attempts[i].ErrorKind != ClassifyError(ErrTransient) {
			t.Errorf("attempt %d wrong error kind", i+1)
		}
	}

	if attempts[2].Number != 3 ||
		attempts[2].Err != nil ||
		attempts[2].ErrorKind != ClassifyError(nil) {
		t.Fatalf("third attempt should succeed: %+v", attempts[2])
	}
}

// 3. 三次全部失败。
func TestDay5RetryExhausted(t *testing.T) {
	fake := &flakyExecutor{failFirst: 100}

	_, err, attempts := executeWithRetry(
		context.Background(),
		task.Task{ID: 101, RetrySafe: true},
		fake,
		day5Policy(),
	)

	if !errors.Is(err, ErrTransient) {
		t.Fatalf("expected transient error, got %v", err)
	}

	if fake.Calls() != 3 {
		t.Fatalf("calls=%d, want 3", fake.Calls())
	}

	if len(attempts) != 3 {
		t.Fatalf("attempts=%d, want 3", len(attempts))
	}

	if TerminalState(ClassifyError(err)) != StateFailed {
		t.Fatalf("exhausted task should be failed")
	}
}

// 4. RetrySafe=false 禁止重试。
func TestDay5RetryUnsafeTask(t *testing.T) {
	fake := &flakyExecutor{failFirst: 100}

	_, err, attempts := executeWithRetry(
		context.Background(),
		task.Task{ID: 102, RetrySafe: false},
		fake,
		day5Policy(),
	)

	if err == nil {
		t.Fatal("expected an error")
	}

	if fake.Calls() != 1 || len(attempts) != 1 {
		t.Fatalf(
			"calls=%d attempts=%d, want 1 each",
			fake.Calls(),
			len(attempts),
		)
	}
}

// 5. Registry 找不到 Tool，不应该重试。
func TestDay5RetryToolNotFound(t *testing.T) {
	registry := executor.NewRegistry()

	_, err, attempts := executeWithRetry(
		context.Background(),
		task.Task{
			ID:        103,
			ToolName:  "missing-tool",
			RetrySafe: true,
		},
		registry,
		day5Policy(),
	)

	if !errors.Is(err, executor.ErrToolNotFound) {
		t.Fatalf("expected tool not found, got %v", err)
	}

	if len(attempts) != 1 {
		t.Fatalf("attempts=%d, want 1", len(attempts))
	}
}

// 6. 第一次失败后取消，不启动第二次执行。
func TestDay5CancelPreventsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	firstFinished := make(chan struct{})

	fake := &flakyExecutor{
		failFirst:            100,
		firstAttemptFinished: firstFinished,
	}

	type outcome struct {
		err      error
		attempts []Attempt
	}

	done := make(chan outcome, 1)

	go func() {
		_, err, attempts := executeWithRetry(
			ctx,
			task.Task{ID: 104, RetrySafe: true},
			fake,
			RetryPolicy{
				MaxAttempts: 5,
				BaseDelay:   time.Second,
				MaxDelay:    2 * time.Second,
			},
		)
		done <- outcome{err: err, attempts: attempts}
	}()

	select {
	case <-firstFinished:
		cancel()

	case <-time.After(2 * time.Second):
		t.Fatal("first execution never finished")
	}

	select {
	case got := <-done:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", got.err)
		}

		if fake.Calls() != 1 {
			t.Fatalf("calls=%d, want 1", fake.Calls())
		}

		if len(got.attempts) != 1 {
			t.Fatalf("attempts=%d, want 1", len(got.attempts))
		}

	case <-time.After(2 * time.Second):
		t.Fatal("retry did not stop after cancellation")
	}
}

// 7. 校验非法 RetryPolicy。
func TestDay5RetryPolicyValidation(t *testing.T) {
	cases := []RetryPolicy{
		{MaxAttempts: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second},
		{MaxAttempts: 3, BaseDelay: 0, MaxDelay: time.Second},
		{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: time.Millisecond},
	}

	for i, p := range cases {
		if err := p.Validate(); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}

	if err := day5Policy().Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}

	fake := &flakyExecutor{}

	_, err := NewRuntimeWithRetryPolicy(
		fake,
		1,
		2,
		cases[0],
	)

	if err == nil {
		t.Fatal("constructor should reject invalid policy")
	}
}

// 8. Backoff 等待必须响应 Context 取消。
func TestDay5BackoffCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{})
	done := make(chan error, 1)

	go func() {
		close(started)
		done <- waitBackoff(ctx, time.Hour)
	}()

	<-started
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}

	case <-time.After(time.Second):
		t.Fatal("backoff failed to respond to cancellation")
	}
}

// 9. 旧版 NewRuntime 默认仅尝试一次。
func TestDay5DefaultRuntimeOnlyOneAttempt(t *testing.T) {
	fake := &flakyExecutor{failFirst: 100}

	r := NewRuntime(fake, 1, 2)

	_, err, attempts := r.executeWithRetry(
		context.Background(),
		task.Task{ID: 105, RetrySafe: true},
	)

	if err == nil {
		t.Fatal("expected execution failure")
	}

	if fake.Calls() != 1 {
		t.Fatalf("calls=%d, want 1", fake.Calls())
	}

	if len(attempts) != 1 {
		t.Fatalf("attempts=%d, want 1", len(attempts))
	}
}

// 10. Runtime 集成测试：结果只交付一次。
func TestDay5RuntimeIntegration(t *testing.T) {
	fake := &flakyExecutor{failFirst: 2}

	r, err := NewRuntimeWithRetryPolicy(
		fake,
		1,
		2,
		day5Policy(),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	r.Start(ctx)

	job := task.Task{
		ID:        106,
		Name:      "runtime-retry",
		RetrySafe: true,
	}

	if err := r.Submit(ctx, job); err != nil {
		t.Fatal(err)
	}

	// 只有一个 Task，Result 缓冲为 2；
	// 当前工程的 Close/Results 方案下不会因缓冲满而阻塞。
	r.Close()

	count := 0

	for result := range r.Results() {
		count++

		if result.TaskID != 106 {
			t.Errorf("wrong TaskID=%d", result.TaskID)
		}

		if result.Err != nil {
			t.Errorf("expected success, got %v", result.Err)
		}
	}

	if count != 1 {
		t.Fatalf("result count=%d, want 1", count)
	}

	if fake.Calls() != 3 {
		t.Fatalf("executor calls=%d, want 3", fake.Calls())
	}
}
