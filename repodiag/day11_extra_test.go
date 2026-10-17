package repodiag

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 创建最小可信 Go 模块。
func extraTestWorkspace(t *testing.T) *Workspace {
	t.Helper()

	dir := t.TempDir()

	err := os.WriteFile(
		filepath.Join(dir, "go.mod"),
		[]byte("module example.com/test\n\ngo 1.22\n"),
		0600,
	)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(
		filepath.Join(dir, "main.go"),
		[]byte("package test\n"),
		0600,
	)
	if err != nil {
		t.Fatal(err)
	}

	w, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}

	return w
}

// 测试：不存在的文件应返回 Tool Error。
func TestDay11MissingFile(t *testing.T) {
	w := extraTestWorkspace(t)

	result := w.Execute(
		context.Background(),
		"read_file",
		json.RawMessage(
			`{"path":"does_not_exist.go"}`,
		),
	)

	if !result.IsError {
		t.Fatal("missing file must return error")
	}
}

// 测试：未注册工具必须被拒绝。
func TestDay11UnknownTool(t *testing.T) {
	w := extraTestWorkspace(t)

	result := w.Execute(
		context.Background(),
		"delete_all_files",
		json.RawMessage(`{}`),
	)

	if !result.IsError {
		t.Fatal("unknown tool must be rejected")
	}

	// 验证原有文件仍然存在。
	path := filepath.Join(w.root, "main.go")

	if _, err := os.Stat(path); err != nil {
		t.Fatal("source file should still exist")
	}
}

// 测试：read_file 不允许多余参数。
func TestDay11UnknownArguments(t *testing.T) {
	w := extraTestWorkspace(t)

	result := w.Execute(
		context.Background(),
		"read_file",
		json.RawMessage(
			`{"path":"main.go","dangerous":true}`,
		),
	)

	if !result.IsError {
		t.Fatal("unknown argument must be rejected")
	}
}

// 测试：超过 MaxCalls 时拒绝继续执行。
func TestDay11MaxCalls(t *testing.T) {
	w := extraTestWorkspace(t)

	agent := Agent{
		Model:     DemoModel{},
		Workspace: w,
		MaxTurns:  8,
		MaxCalls:  2,
	}

	report, err := agent.Run(
		context.Background(),
		"检查仓库",
	)

	if err == nil {
		t.Fatal("expected max calls error")
	}

	if !strings.Contains(
		err.Error(),
		"tool call limit",
	) {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.Status != "failed" {
		t.Fatalf(
			"status=%s, want failed",
			report.Status,
		)
	}

	if len(report.Steps) != 2 {
		t.Fatalf(
			"steps=%d, want 2",
			len(report.Steps),
		)
	}
}

// 测试：已取消的 Context 不得继续执行 Agent。
func TestDay11ExtraCancelled(t *testing.T) {
	w := extraTestWorkspace(t)

	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	cancel()

	agent := Agent{
		Model:     DemoModel{},
		Workspace: w,
	}

	report, err := agent.Run(ctx, "检查仓库")

	if !errors.Is(err, context.Canceled) {
		t.Fatalf(
			"expected context.Canceled, got %v",
			err,
		)
	}

	if report.Status != "cancelled" {
		t.Fatalf(
			"status=%s, want cancelled",
			report.Status,
		)
	}
}
