package executor

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"runtime-app/task"
)

// Test 1: echo 成功执行
func TestShellEcho(t *testing.T) {
	e := ShellExecutor{
		Timeout: 3 * time.Second,
	}

	task := task.Task{
		ID:      1,
		Name:    "echo-task",
		Command: "echo",
		Args:    []string{"hello agent"},
	}

	output, err := e.Execute(
		context.Background(),
		task,
	)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if output.Stdout != "hello agent\n" {
		t.Errorf(
			"expected stdout %q, got %q",
			"hello agent\n",
			output.Stdout,
		)
	}

	if output.ExitCode != 0 {
		t.Errorf(
			"expected exit code 0, got %d",
			output.ExitCode,
		)
	}

	if output.Stderr != "" {
		t.Errorf(
			"expected empty stderr, got %q",
			output.Stderr,
		)
	}
}

// Test 2: false 返回非零退出码
func TestShellNonZeroExit(t *testing.T) {
	e := ShellExecutor{
		Timeout: 3 * time.Second,
	}

	task := task.Task{
		ID:      2,
		Name:    "fail-task",
		Command: "false",
	}

	output, err := e.Execute(
		context.Background(),
		task,
	)

	if err == nil {
		t.Fatal("expected an error, got nil")
	}

	var exitErr *exec.ExitError

	if !errors.As(err, &exitErr) {
		t.Errorf(
			"expected ExitError, got %T: %v",
			err,
			err,
		)
	}

	if output.ExitCode != 1 {
		t.Errorf(
			"expected exit code 1, got %d",
			output.ExitCode,
		)
	}
}

// Test 3: sleep 3 在 100ms 后超时
func TestShellTimeout(t *testing.T) {
	e := ShellExecutor{
		Timeout: 100 * time.Millisecond,
	}

	task := task.Task{
		ID:      3,
		Name:    "timeout-task",
		Command: "sleep",
		Args:    []string{"3"},
	}

	start := time.Now()

	output, err := e.Execute(
		context.Background(),
		task,
	)

	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf(
			"expected deadline exceeded, got %v",
			err,
		)
	}

	if output.ExitCode != -1 {
		t.Errorf(
			"expected exit code -1, got %d",
			output.ExitCode,
		)
	}

	// 不应该真的等待 sleep 3 秒完成
	if elapsed >= 2*time.Second {
		t.Errorf(
			"process took too long: %v",
			elapsed,
		)
	}
}

// Test 4: 拒绝不在白名单中的命令
func TestShellDisallowedCommand(t *testing.T) {
	e := ShellExecutor{
		Timeout: time.Second,
	}

	task := task.Task{
		ID:      4,
		Name:    "disallowed-task",
		Command: "uname",
	}

	output, err := e.Execute(
		context.Background(),
		task,
	)

	if err == nil {
		t.Fatal("expected command rejection, got nil")
	}

	if !strings.Contains(err.Error(), "command not allowed") {
		t.Errorf(
			"expected command not allowed error, got %v",
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
