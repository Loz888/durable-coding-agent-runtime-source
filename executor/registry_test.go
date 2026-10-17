package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"runtime-app/task"
)

// Test 1: 检查 Registry 能否正确路由到 UppercaseExecutor
func TestRegistryRoutesUppercase(t *testing.T) {
	registry := NewRegistry()

	err := registry.Register(
		"uppercase",
		UppercaseExecutor{},
	)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	testTask := task.Task{
		ID:       1,
		Name:     "uppercase-test",
		ToolName: "uppercase",
		Payload:  "hello agent",
	}

	output, err := registry.Execute(
		context.Background(),
		testTask,
	)
	if err != nil {
		t.Fatalf("execute failed: %v", err)
	}

	if output.Stdout != "HELLO AGENT" {
		t.Errorf(
			"expected HELLO AGENT, got %q",
			output.Stdout,
		)
	}

	if output.ExitCode != 0 {
		t.Errorf(
			"expected exit code 0, got %d",
			output.ExitCode,
		)
	}
}

// Test 2: 未注册的工具必须返回错误
func TestRegistryUnknownTool(t *testing.T) {
	registry := NewRegistry()

	testTask := task.Task{
		ID:       2,
		Name:     "unknown-test",
		ToolName: "missing-tool",
	}

	output, err := registry.Execute(
		context.Background(),
		testTask,
	)

	if err == nil {
		t.Fatal("expected error for unknown tool")
	}

	if !errors.Is(err, ErrToolNotFound) {
		t.Errorf(
			"expected ErrToolNotFound, got %v",
			err,
		)
	}

	if output.ExitCode != -1 {
		t.Errorf(
			"expected exit code -1, got %d",
			output.ExitCode,
		)
	}
}

// Test 3: 同一个工具名称不能重复注册
func TestRegistryDuplicate(t *testing.T) {
	registry := NewRegistry()

	err := registry.Register(
		"uppercase",
		UppercaseExecutor{},
	)
	if err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	err = registry.Register(
		"uppercase",
		&UppercaseExecutor{},
	)

	if err == nil {
		t.Fatal("expected duplicate registration error")
	}

	if !errors.Is(err, ErrDuplicateTool) {
		t.Errorf(
			"expected ErrDuplicateTool, got %v",
			err,
		)
	}
	registered, ok := registry.Lookup("uppercase")
	if !ok {
		t.Fatal("original executor missing after duplicate registration")
	}
	if _, ok := registered.(UppercaseExecutor); !ok {
		t.Fatalf("duplicate registration replaced original executor: %T", registered)
	}
}

// Test 4: 多个 goroutine 并发调用 Registry
func TestRegistryConcurrentDispatch(t *testing.T) {
	registry := NewRegistry()

	err := registry.Register(
		"uppercase",
		UppercaseExecutor{},
	)
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	const taskCount = 100

	var wg sync.WaitGroup

	errCh := make(chan error, taskCount)
	start := make(chan struct{})

	for i := 0; i < taskCount; i++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()
			<-start

			payload := fmt.Sprintf("hello-%d", id)

			testTask := task.Task{
				ID:       id,
				Name:     "concurrent-test",
				ToolName: "uppercase",
				Payload:  payload,
			}

			output, err := registry.Execute(
				context.Background(),
				testTask,
			)

			if err != nil {
				errCh <- fmt.Errorf(
					"task %d failed: %w",
					id,
					err,
				)
				return
			}

			expected := strings.ToUpper(payload)

			if output.Stdout != expected {
				errCh <- fmt.Errorf(
					"task %d: expected %q, got %q",
					id,
					expected,
					output.Stdout,
				)
				return
			}

			if output.ExitCode != 0 {
				errCh <- fmt.Errorf(
					"task %d: expected exit code 0, got %d",
					id,
					output.ExitCode,
				)
			}
		}(i)
	}

	close(start)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}
