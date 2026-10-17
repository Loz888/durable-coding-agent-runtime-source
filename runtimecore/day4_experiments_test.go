package runtimecore

import (
	"context"
	"errors"
	"testing"
	"time"

	"runtime-app/executor"
	"runtime-app/task"
)

// collectDay4Results 持续消费 Result，直到 Channel 关闭。
// 超时保护用于发现 Runtime 未关闭或结果丢失的问题。
func collectDay4Results(
	t *testing.T,
	results <-chan Result,
) []Result {
	t.Helper()

	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()

	collected := make([]Result, 0)

	for {
		select {
		case result, ok := <-results:
			if !ok {
				return collected
			}

			collected = append(collected, result)

		case <-timer.C:
			t.Fatalf(
				"timed out waiting for Results channel to close; received=%d",
				len(collected),
			)
			return nil
		}
	}
}

// runDay4SingleTask 启动 Runtime，提交一个任务，
// 等待所有结果，然后检查最终状态。
func runDay4SingleTask(
	t *testing.T,
	e executor.Executor,
	input task.Task,
) Result {
	t.Helper()

	ctx := context.Background()

	r := NewRuntime(e, 1, 4)
	r.Start(ctx)

	if err := r.Submit(ctx, input); err != nil {
		go r.Close()
		t.Fatalf("Submit failed: %v", err)
	}

	// Close 可能等待 Worker 退出。
	// 为避免 Worker 发送 Result 时阻塞，
	// 使用独立 goroutine 关闭队列，
	// 当前测试 goroutine 同时消费 Results。
	go r.Close()

	results := collectDay4Results(
		t,
		r.Results(),
	)

	if len(results) != 1 {
		t.Fatalf(
			"received=%d, want=1",
			len(results),
		)
	}

	result := results[0]

	if result.TaskID != input.ID {
		t.Fatalf(
			"TaskID=%d, want=%d",
			result.TaskID,
			input.ID,
		)
	}

	state, ok := r.State(input.ID)

	if !ok {
		t.Fatalf(
			"State(%d): task not found",
			input.ID,
		)
	}

	if state != result.State {
		t.Fatalf(
			"stored state=%v, result state=%v",
			state,
			result.State,
		)
	}

	return result
}

// ==================================================
// 实验 A：正常状态转换
// Pending -> Running -> Succeeded
// ==================================================

func TestDay4AStateLifecycle(t *testing.T) {
	registry := executor.NewRegistry()

	registry.Register(
		"uppercase",
		executor.UppercaseExecutor{},
	)

	input := task.Task{
		ID:       1,
		Name:     "uppercase-task",
		ToolName: "uppercase",
		Payload:  "hello agent",
	}

	result := runDay4SingleTask(
		t,
		registry,
		input,
	)

	if result.State != StateSucceeded {
		t.Fatalf(
			"State=%v, want=%v",
			result.State,
			StateSucceeded,
		)
	}

	if result.ErrorKind != KindNone {
		t.Fatalf(
			"ErrorKind=%v, want=%v",
			result.ErrorKind,
			KindNone,
		)
	}

	if result.ExitCode != 0 {
		t.Fatalf(
			"ExitCode=%d, want=0",
			result.ExitCode,
		)
	}

	t.Logf(
		"Task %d: state=%v, errorKind=%v",
		result.TaskID,
		result.State,
		result.ErrorKind,
	)
}

// ==================================================
// 实验 B：非零退出
// shell -> false -> exit code 1
// ==================================================

func TestDay4BNonZeroExit(t *testing.T) {
	registry := executor.NewRegistry()

	registry.Register(
		"shell",
		executor.ShellExecutor{
			Timeout: 2 * time.Second,
		},
	)

	input := task.Task{
		ID:       2,
		Name:     "nonzero-exit-task",
		ToolName: "shell",
		Command:  "false",
	}

	result := runDay4SingleTask(
		t,
		registry,
		input,
	)

	if result.State != StateFailed {
		t.Fatalf(
			"State=%v, want=%v",
			result.State,
			StateFailed,
		)
	}

	if result.ErrorKind != KindNonZeroExit {
		t.Fatalf(
			"ErrorKind=%v, want=%v",
			result.ErrorKind,
			KindNonZeroExit,
		)
	}

	if result.ExitCode != 1 {
		t.Fatalf(
			"ExitCode=%d, want=1",
			result.ExitCode,
		)
	}

	t.Logf(
		"Task %d: state=%v, kind=%v, exitCode=%d",
		result.TaskID,
		result.State,
		result.ErrorKind,
		result.ExitCode,
	)
}

// ==================================================
// 实验 C：未知 Tool
// missing-tool -> ToolNotFound
// ==================================================

func TestDay4CToolNotFound(t *testing.T) {
	registry := executor.NewRegistry()

	input := task.Task{
		ID:       3,
		Name:     "unknown-tool-task",
		ToolName: "missing-tool",
	}

	// 先验证 Registry 返回的错误可以被 errors.Is 识别。
	_, lookupErr := registry.Execute(
		context.Background(),
		input,
	)

	if !errors.Is(
		lookupErr,
		executor.ErrToolNotFound,
	) {
		t.Fatalf(
			"expected ErrToolNotFound, got %v",
			lookupErr,
		)
	}

	// 再验证 Runtime 能将错误映射为正确状态。
	result := runDay4SingleTask(
		t,
		registry,
		input,
	)

	if result.State != StateFailed {
		t.Fatalf(
			"State=%v, want=%v",
			result.State,
			StateFailed,
		)
	}

	if result.ErrorKind != KindToolNotFound {
		t.Fatalf(
			"ErrorKind=%v, want=%v",
			result.ErrorKind,
			KindToolNotFound,
		)
	}

	if result.ExitCode != -1 {
		t.Fatalf(
			"ExitCode=%d, want=-1",
			result.ExitCode,
		)
	}

	t.Logf(
		"Task %d: state=%v, kind=%v, exitCode=%d",
		result.TaskID,
		result.State,
		result.ErrorKind,
		result.ExitCode,
	)
}

// ==================================================
// 实验 D：工具执行超时
// sleep 3 -> timeout 100ms
// ==================================================

func TestDay4DTimeout(t *testing.T) {
	registry := executor.NewRegistry()

	registry.Register(
		"shell",
		executor.ShellExecutor{
			Timeout: 100 * time.Millisecond,
		},
	)

	input := task.Task{
		ID:       4,
		Name:     "timeout-task",
		ToolName: "shell",
		Command:  "sleep",
		Args:     []string{"3"},
	}

	result := runDay4SingleTask(
		t,
		registry,
		input,
	)

	if result.State != StateCancelled {
		t.Fatalf(
			"State=%v, want=%v",
			result.State,
			StateCancelled,
		)
	}

	if result.ErrorKind != KindTimeout {
		t.Fatalf(
			"ErrorKind=%v, want=%v",
			result.ErrorKind,
			KindTimeout,
		)
	}

	t.Logf(
		"Task %d: state=%v, kind=%v",
		result.TaskID,
		result.State,
		result.ErrorKind,
	)
}

// ==================================================
// 实验 E：Runtime 全局取消
//
// WorkerCount = 1
// QueueSize   = 20
// TaskCount   = 10
// Delay       = 300ms
//
// 核心不变量：
// Accepted Tasks == Terminal Results
// ==================================================

func TestDay4EGlobalCancellation(t *testing.T) {
	const (
		workerCount = 1
		queueSize   = 20
		taskCount   = 10
	)

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	r := NewRuntime(
		executor.DelayExecutor{
			Delay: 300 * time.Millisecond,
		},
		workerCount,
		queueSize,
	)

	r.Start(ctx)

	// 记录所有成功提交的 TaskID。
	accepted := make(map[int]struct{})

	for id := 1; id <= taskCount; id++ {
		input := task.New(
			id,
			"delay-task",
			"hello",
		)

		err := r.Submit(ctx, input)

		if err != nil {
			t.Errorf(
				"Submit(%d) failed: %v",
				id,
				err,
			)
			continue
		}

		accepted[id] = struct{}{}
	}

	// 取消 Runtime 的父 Context。
	cancel()

	// 关闭 Task Queue。
	// 与 Results 消费并发进行，避免阻塞。
	go r.Close()

	// 持续读取，直到结果通道关闭。
	results := collectDay4Results(
		t,
		r.Results(),
	)

	// 检查是否存在重复结果。
	seen := make(map[int]struct{})

	for _, result := range results {
		id := result.TaskID

		// 结果必须对应已接受的任务。
		if _, ok := accepted[id]; !ok {
			t.Errorf(
				"unexpected result for task %d",
				id,
			)
			continue
		}

		// 每个任务只能产生一个终态结果。
		if _, exists := seen[id]; exists {
			t.Errorf(
				"duplicate result for task %d",
				id,
			)
			continue
		}

		seen[id] = struct{}{}

		// 允许取消前已经完成的任务成功。
		switch result.State {
		case StateSucceeded:
			if result.ErrorKind != KindNone {
				t.Errorf(
					"task %d: succeeded with kind %v",
					id,
					result.ErrorKind,
				)
			}

		case StateCancelled:
			if result.ErrorKind != KindCancelled {
				t.Errorf(
					"task %d: cancelled with kind %v",
					id,
					result.ErrorKind,
				)
			}

		default:
			t.Errorf(
				"task %d: unexpected terminal state %v",
				id,
				result.State,
			)
		}

		// Runtime 保存的状态与 Result 必须一致。
		state, ok := r.State(id)

		if !ok {
			t.Errorf(
				"State(%d): task not found",
				id,
			)
			continue
		}

		if state != result.State {
			t.Errorf(
				"task %d: stored=%v, result=%v",
				id,
				state,
				result.State,
			)
		}

		t.Logf(
			"Task %d: state=%v, kind=%v",
			id,
			result.State,
			result.ErrorKind,
		)
	}

	// 先检查是否成功提交了全部 10 个任务。
	if len(accepted) != taskCount {
		t.Errorf(
			"accepted=%d, want=%d",
			len(accepted),
			taskCount,
		)
	}

	// 检查结果数量。
	if len(results) != len(accepted) {
		t.Errorf(
			"terminal results=%d, accepted=%d",
			len(results),
			len(accepted),
		)
	}

	// 检查每个已接受的任务都收到结果。
	for id := range accepted {
		if _, ok := seen[id]; !ok {
			t.Errorf(
				"task %d accepted but no result received",
				id,
			)
		}
	}
}
