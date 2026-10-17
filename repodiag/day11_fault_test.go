package repodiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newDay11TestWorkspace 创建最小 Go 仓库，避免修改 broken-math 故障样例。
func newDay11TestWorkspace(t *testing.T) *Workspace {
	t.Helper()
	root := t.TempDir()

	files := map[string]string{
		"go.mod":  "module example.com/day11-fixture\n\ngo 1.23\n",
		"math.go": "package mathfixture\n\nfunc Add(a, b int) int { return a + b }\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	workspace, err := NewWorkspace(root)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	return workspace
}

// A: 不存在的文件必须产生 ToolResult 错误，不能伪造源码内容。
func TestDay11AReadNonexistentFile(t *testing.T) {
	workspace := newDay11TestWorkspace(t)
	result := workspace.Execute(
		context.Background(),
		"read_file",
		json.RawMessage(`{"path":"does_not_exist.go"}`),
	)

	if !result.IsError {
		t.Fatalf("expected tool error, got %+v", result)
	}
	if result.Name != "read_file" {
		t.Fatalf("wrong tool name: %q", result.Name)
	}
	if !strings.Contains(result.Output, "does_not_exist.go") {
		t.Fatalf("missing filename in error: %q", result.Output)
	}
	if strings.Contains(result.Output, "func Add(") {
		t.Fatalf("unexpected fabricated file contents: %q", result.Output)
	}
}

// B: 即使模型能提出未知工具，Workspace 也必须拒绝执行。
func TestDay11BRejectUnknownTool(t *testing.T) {
	workspace := newDay11TestWorkspace(t)
	result := workspace.Execute(
		context.Background(),
		"delete_all_files",
		json.RawMessage(`{}`),
	)

	if !result.IsError {
		t.Fatalf("unknown tool must be rejected: %+v", result)
	}
	if !strings.Contains(result.Output, `unknown tool "delete_all_files"`) {
		t.Fatalf("unexpected error: %q", result.Output)
	}

	// 不只检查错误，还要检查仓库里的文件没有被改动。
	contents, err := os.ReadFile(filepath.Join(workspace.root, "math.go"))
	if err != nil {
		t.Fatalf("math.go was removed: %v", err)
	}
	if !strings.Contains(string(contents), "func Add(") {
		t.Fatalf("math.go was unexpectedly changed: %q", contents)
	}
}

// C: 模型提供多余字段时，严格 JSON 解析必须拒绝它。
func TestDay11CRejectExtraArguments(t *testing.T) {
	workspace := newDay11TestWorkspace(t)
	result := workspace.Execute(
		context.Background(),
		"read_file",
		json.RawMessage(`{"path":"math.go","dangerous":true}`),
	)

	if !result.IsError {
		t.Fatalf("extra argument must be rejected: %+v", result)
	}
	if !strings.Contains(result.Output, `unknown field "dangerous"`) {
		t.Fatalf("expected strict decoding error, got %q", result.Output)
	}
	if strings.Contains(result.Output, "func Add(") {
		t.Fatalf("read_file unexpectedly returned file contents: %q", result.Output)
	}
}

// loopingDay11Model 不调用网络，持续要求 Agent 执行真实的 list_files 工具。
// 这是一种确定性故障注入，用于验证资源预算。
type loopingDay11Model struct {
	requests atomic.Int32
	perTurn  int
}

func (m *loopingDay11Model) Next(ctx context.Context, _ []json.RawMessage) (Turn, error) {
	if err := ctx.Err(); err != nil {
		return Turn{}, err
	}

	turnNumber := m.requests.Add(1)
	if turnNumber > 100 {
		return Turn{}, errors.New("test safety guard: agent requested too many turns")
	}

	calls := make([]ToolCall, m.perTurn)
	for i := range calls {
		calls[i] = ToolCall{
			CallID:    fmt.Sprintf("test-%d-%d", turnNumber, i),
			Name:      "list_files",
			Arguments: json.RawMessage(`{}`),
		}
	}
	return Turn{Calls: calls}, nil
}

// D1: 每轮始终要求调用工具，Agent 不得超过 MaxTurns。
func TestDay11DMaxTurns(t *testing.T) {
	workspace := newDay11TestWorkspace(t)
	model := &loopingDay11Model{perTurn: 1}
	agent := &Agent{
		Model: model, Workspace: workspace,
		MaxTurns: 3, MaxCalls: 20,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	report, err := agent.Run(ctx, "重复调用工具，测试 MaxTurns")
	gotTurns := model.requests.Load()
	if gotTurns < 2 || gotTurns > 3 {
		t.Fatalf("MaxTurns=3, model was called %d times; status=%q, err=%v", gotTurns, report.Status, err)
	}
	if len(report.Steps) > 3 {
		t.Fatalf("executed %d tools despite MaxTurns=3", len(report.Steps))
	}
	if err == nil && report.Status == "succeeded" {
		t.Fatal("agent falsely reported success despite endless tool calls")
	}
	t.Logf("turn limit enforced: model turns=%d, executed tools=%d, status=%s, err=%v", gotTurns, len(report.Steps), report.Status, err)
}

// D2: 每轮提出多个工具调用，Agent 不得突破 MaxCalls。
func TestDay11DMaxCalls(t *testing.T) {
	workspace := newDay11TestWorkspace(t)
	model := &loopingDay11Model{perTurn: 3}
	agent := &Agent{
		Model: model, Workspace: workspace,
		MaxTurns: 20, MaxCalls: 4,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	report, err := agent.Run(ctx, "重复批量调用工具，测试 MaxCalls")
	gotTurns := model.requests.Load()
	if gotTurns == 0 || gotTurns > 3 {
		t.Fatalf("MaxCalls=4, model called %d times; status=%q, err=%v", gotTurns, report.Status, err)
	}
	if len(report.Steps) == 0 || len(report.Steps) > 4 {
		t.Fatalf("MaxCalls=4, but executed %d tools; status=%q, err=%v", len(report.Steps), report.Status, err)
	}
	if err == nil && report.Status == "succeeded" {
		t.Fatal("agent falsely reported success despite endless tool calls")
	}
	t.Logf("call limit enforced: model turns=%d, executed tools=%d, status=%s, err=%v", gotTurns, len(report.Steps), report.Status, err)
}
