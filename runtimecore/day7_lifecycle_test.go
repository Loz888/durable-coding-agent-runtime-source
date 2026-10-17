package runtimecore

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"runtime-app/executor"
	"runtime-app/task"
)

type day7InstantExecutor struct{}

func (day7InstantExecutor) Execute(
	ctx context.Context,
	t task.Task,
) (executor.Output, error) {

	if err := ctx.Err(); err != nil {
		return executor.Output{
			ExitCode: -1,
		}, err
	}

	return executor.Output{
		Stdout:   "ok",
		ExitCode: 0,
	}, nil
}
func waitDay7Condition(
	t *testing.T,
	condition func() bool,
) {
	t.Helper()

	deadline := time.NewTimer(
		2 * time.Second,
	)
	defer deadline.Stop()

	ticker := time.NewTicker(
		time.Millisecond,
	)
	defer ticker.Stop()

	for {
		if condition() {
			return
		}

		select {
		case <-deadline.C:
			t.Fatal("condition not reached")
		case <-ticker.C:
		}
	}
}

func TestDay7CloseIdempotent(t *testing.T) {
	r := NewRuntime(
		day7InstantExecutor{},
		1,
		1,
	)

	ctx := context.Background()

	r.Start(ctx)

	r.Close()
	r.Close()
	r.Close()

	for range r.Results() {
	}

	err := r.Submit(
		ctx,
		task.Task{ID: 1},
	)

	if !errors.Is(err, ErrRuntimeClosed) {
		t.Fatalf(
			"got %v, want ErrRuntimeClosed",
			err,
		)
	}
}

func TestDay7CloseWaitsForActiveSubmit(
	t *testing.T,
) {
	r := NewRuntime(
		day7InstantExecutor{},
		1,
		0,
	)

	ctx := context.Background()

	submitDone := make(chan error, 1)

	go func() {
		submitDone <- r.Submit(
			ctx,
			task.Task{
				ID:   10,
				Name: "blocked-submit",
			},
		)
	}()

	// 等待 Submit 完成 Pending 登记。
	waitDay7Condition(t, func() bool {
		state, ok := r.State(10)
		return ok && state == StatePending
	})

	closeDone := make(chan struct{})

	go func() {
		r.Close()
		close(closeDone)
	}()

	// 确认 Close 已经开始禁止新提交。
	waitDay7Condition(t, func() bool {
		r.submitMu.Lock()
		defer r.submitMu.Unlock()

		return r.submissionsClosed
	})

	// 因为还没有 Worker 接收，
	// Submit 未完成，Close 不应结束。
	select {
	case <-closeDone:
		t.Fatal("Close returned too early")
	default:
	}

	// 现在启动 Worker，让 Submit 可以完成。
	r.Start(ctx)

	if err := <-submitDone; err != nil {
		t.Fatal(err)
	}

	<-closeDone

	received := 0

	for result := range r.Results() {
		received++

		if result.TaskID != 10 {
			t.Errorf(
				"unexpected TaskID=%d",
				result.TaskID,
			)
		}
	}

	if received != 1 {
		t.Fatalf(
			"received=%d, want 1",
			received,
		)
	}
}

func TestDay7CancelledSubmitRollback(
	t *testing.T,
) {
	r := NewRuntime(
		day7InstantExecutor{},
		1,
		0,
	)

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	errCh := make(chan error, 1)

	go func() {
		errCh <- r.Submit(
			ctx,
			task.Task{
				ID:   20,
				Name: "cancelled-submit",
			},
		)
	}()

	waitDay7Condition(t, func() bool {
		state, ok := r.State(20)
		return ok && state == StatePending
	})

	cancel()

	err := <-errCh

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"got %v, want context.Canceled",
			err,
		)
	}

	if _, exists := r.State(20); exists {
		t.Fatal(
			"cancelled Submit left a Pending state",
		)
	}

	r.Close()
}

func TestDay7ConcurrentSubmitAndClose(
	t *testing.T,
) {
	ctx := context.Background()

	r := NewRuntime(
		day7InstantExecutor{},
		4,
		2,
	)

	r.Start(ctx)

	resultsDone := make(chan []Result, 1)

	// Collector 从开始到结束持续读取。
	go func() {
		var results []Result

		for result := range r.Results() {
			results = append(results, result)
		}

		resultsDone <- results
	}()

	const total = 120

	accepted := make(chan int, total)
	unexpectedErrors := make(chan error, total)

	// 先确保至少有部分任务成功提交。
	for id := 1; id <= 5; id++ {
		err := r.Submit(
			ctx,
			task.Task{ID: id},
		)

		if err != nil {
			t.Fatal(err)
		}

		accepted <- id
	}

	var producers sync.WaitGroup

	for id := 6; id <= total; id++ {
		producers.Add(1)

		go func(taskID int) {
			defer producers.Done()

			err := r.Submit(
				ctx,
				task.Task{ID: taskID},
			)

			if err == nil {
				accepted <- taskID
				return
			}

			if !errors.Is(err, ErrRuntimeClosed) {
				unexpectedErrors <- err
			}
		}(id)
	}

	closeDone := make(chan struct{})

	// 与 Producer 并发关闭 Runtime。
	go func() {
		r.Close()
		close(closeDone)
	}()

	producers.Wait()

	<-closeDone

	close(accepted)
	close(unexpectedErrors)

	for err := range unexpectedErrors {
		t.Errorf("unexpected submit error: %v", err)
	}

	expected := make(map[int]struct{})

	for id := range accepted {
		expected[id] = struct{}{}
	}

	results := <-resultsDone

	if len(results) != len(expected) {
		t.Fatalf(
			"results=%d, accepted=%d",
			len(results),
			len(expected),
		)
	}

	seen := make(map[int]struct{})

	for _, result := range results {
		if _, ok := expected[result.TaskID]; !ok {
			t.Errorf(
				"result for unaccepted task %d",
				result.TaskID,
			)
		}

		if _, duplicate := seen[result.TaskID]; duplicate {
			t.Errorf(
				"duplicate result for task %d",
				result.TaskID,
			)
		}

		seen[result.TaskID] = struct{}{}

		if result.State != StateSucceeded {
			t.Errorf(
				"task %d state=%s",
				result.TaskID,
				result.State,
			)
		}
	}
}
