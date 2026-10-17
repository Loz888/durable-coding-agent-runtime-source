package runtimecore

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"testing"

	"runtime-app/executor"
)

// TestClassifyError
// 测试基本错误分类
func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{
			name: "nil error",
			err:  nil,
			want: KindNone,
		},
		{
			name: "context canceled",
			err:  context.Canceled,
			want: KindCancelled,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: KindTimeout,
		},
		{
			name: "tool not found",
			err:  executor.ErrToolNotFound,
			want: KindToolNotFound,
		},
		{
			name: "command not allowed",
			err:  executor.ErrCommandNotAllowed,
			want: KindCommandNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err)

			if got != tt.want {
				t.Fatalf(
					"ClassifyError(%v) = %v, want %v",
					tt.err,
					got,
					tt.want,
				)
			}
		})
	}
}

// TestClassifyWrappedError
// 验证被 fmt.Errorf("%w") 包装的错误
// 是否依然能够正确分类
func TestClassifyWrappedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorKind
	}{
		{
			name: "wrapped canceled",
			err: fmt.Errorf(
				"task failed: %w",
				context.Canceled,
			),
			want: KindCancelled,
		},
		{
			name: "wrapped timeout",
			err: fmt.Errorf(
				"task failed: %w",
				context.DeadlineExceeded,
			),
			want: KindTimeout,
		},
		{
			name: "wrapped tool not found",
			err: fmt.Errorf(
				"lookup failed: %w",
				executor.ErrToolNotFound,
			),
			want: KindToolNotFound,
		},
		{
			name: "wrapped command not allowed",
			err: fmt.Errorf(
				"execute failed: %w",
				executor.ErrCommandNotAllowed,
			),
			want: KindCommandNotAllowed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyError(tt.err)

			if got != tt.want {
				t.Fatalf(
					"wrapped error classified as %v, want %v",
					got,
					tt.want,
				)
			}
		})
	}
}

// TestClassifyNonZeroExit
// 真实执行 false 命令
// 验证非零退出码能够被正确分类
func TestClassifyNonZeroExit(t *testing.T) {
	cmd := exec.Command("false")

	err := cmd.Run()

	// false 必须返回非 nil 错误
	if err == nil {
		t.Fatal(
			"expected non-zero exit error, got nil",
		)
	}

	// 检查底层错误是否为 *exec.ExitError
	var exitErr *exec.ExitError

	if !errors.As(err, &exitErr) {
		t.Fatalf(
			"expected *exec.ExitError, got %T: %v",
			err,
			err,
		)
	}

	// 验证退出码
	if exitErr.ExitCode() != 1 {
		t.Fatalf(
			"expected exit code 1, got %d",
			exitErr.ExitCode(),
		)
	}

	// 验证错误分类
	got := ClassifyError(err)

	if got != KindNonZeroExit {
		t.Fatalf(
			"expected KindNonZeroExit, got %v",
			got,
		)
	}

	// 验证包装后的 ExitError 仍能正确分类
	wrappedErr := fmt.Errorf(
		"shell execution failed: %w",
		err,
	)

	got = ClassifyError(wrappedErr)

	if got != KindNonZeroExit {
		t.Fatalf(
			"wrapped ExitError: expected %v, got %v",
			KindNonZeroExit,
			got,
		)
	}
}
