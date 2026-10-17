package durable

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"runtime-app/repodiag"
)

// memoryStore 仅用于测试。
// 不具备跨进程持久化能力。
type memoryStore struct {
	mu   sync.Mutex
	data map[string]Snapshot
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		data: make(map[string]Snapshot),
	}
}

func (m *memoryStore) Create(
	ctx context.Context,
	s Snapshot,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := s.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.data[s.RunID]; exists {
		return ErrExists
	}

	m.data[s.RunID] = s
	return nil
}

func (m *memoryStore) Load(
	ctx context.Context,
	id string,
) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	s, exists := m.data[id]
	if !exists {
		return Snapshot{}, ErrNotFound
	}

	return s, nil
}

func (m *memoryStore) Save(
	ctx context.Context,
	s Snapshot,
	expected int64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := s.Validate(); err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	current, exists := m.data[s.RunID]

	if !exists ||
		current.Revision != expected ||
		current.Status != Running ||
		s.Revision != expected+1 {
		return ErrConflict
	}

	m.data[s.RunID] = s
	return nil
}

// countingModel 统计 Model.Next 的实际调用次数。
type countingModel struct {
	mu    sync.Mutex
	count int
	base  repodiag.Model
}

func (m *countingModel) Next(
	ctx context.Context,
	history []json.RawMessage,
) (repodiag.Turn, error) {
	m.mu.Lock()
	m.count++
	m.mu.Unlock()

	return m.base.Next(ctx, history)
}

func (m *countingModel) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.count
}

// 创建独立、可信的临时 Go 仓库。
func day12TestWorkspace(
	t *testing.T,
) (string, *repodiag.Workspace) {
	t.Helper()

	root := t.TempDir()

	files := map[string]string{
		"go.mod": `module example.com/day12-fixture

go 1.22
`,
		"math.go": `package math

func Add(a, b int) int {
	return a + b
}
`,
		"math_test.go": `package math

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("unexpected result")
	}
}
`,
	}

	for name, content := range files {
		err := os.WriteFile(
			filepath.Join(root, name),
			[]byte(content),
			0600,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	workspace, err := repodiag.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	return root, workspace
}

// Test A：ToolCall Intent 已提交后中断。
func TestDay12ResumeAfterCommittedToolIntent(
	t *testing.T,
) {
	root, workspace := day12TestWorkspace(t)

	store := newMemoryStore()

	model := &countingModel{
		base: repodiag.DemoModel{},
	}

	injected := errors.New(
		"injected failure after revision 1",
	)

	runner := &Runner{
		Store:     store,
		Model:     model,
		Workspace: workspace,
		Provider:  "demo",

		AfterSave: func(s Snapshot) error {
			if s.Revision == 1 {
				return injected
			}
			return nil
		},
	}

	_, err := runner.Run(
		context.Background(),
		"day12-test-a",
		root,
		"diagnose Go repository",
	)

	if !errors.Is(err, injected) {
		t.Fatalf(
			"expected injected error, got %v",
			err,
		)
	}

	saved, err := store.Load(
		context.Background(),
		"day12-test-a",
	)
	if err != nil {
		t.Fatal(err)
	}

	if saved.Revision != 1 ||
		saved.Turns != 1 ||
		saved.Calls != 1 {
		t.Fatalf(
			"incorrect saved checkpoint: %+v",
			saved,
		)
	}

	if model.Count() != 1 {
		t.Fatalf(
			"model count=%d, want 1",
			model.Count(),
		)
	}

	// 创建新的 Runner，使用已有 Store 恢复。
	recovered := &Runner{
		Store:     store,
		Model:     model,
		Workspace: workspace,
		Provider:  "demo",
	}

	final, err := recovered.Run(
		context.Background(),
		"day12-test-a",
		root,
		"diagnose Go repository",
	)
	if err != nil {
		t.Fatal(err)
	}

	if final.Status != Succeeded ||
		final.Revision != 7 ||
		final.Turns != 4 ||
		final.Calls != 3 {
		t.Fatalf(
			"unexpected final checkpoint: %+v",
			final,
		)
	}

	// 正确恢复后，不应重新调用第一次模型决策。
	if model.Count() != 4 {
		t.Fatalf(
			"model calls=%d, want 4",
			model.Count(),
		)
	}

	t.Log(
		"PASS: resumed persisted tool intent",
	)
}

// Test B：ToolResult 已保存后中断。
func TestDay12ResumeAfterSavedToolResult(
	t *testing.T,
) {
	root, workspace := day12TestWorkspace(t)
	store := newMemoryStore()

	model := &countingModel{
		base: repodiag.DemoModel{},
	}

	injected := errors.New(
		"injected failure after revision 2",
	)

	runner := &Runner{
		Store:     store,
		Model:     model,
		Workspace: workspace,
		Provider:  "demo",

		AfterSave: func(s Snapshot) error {
			if s.Revision == 2 {
				return injected
			}
			return nil
		},
	}

	_, err := runner.Run(
		context.Background(),
		"day12-test-b",
		root,
		"diagnose Go repository",
	)

	if !errors.Is(err, injected) {
		t.Fatalf(
			"expected injected error, got %v",
			err,
		)
	}

	saved, err := store.Load(
		context.Background(),
		"day12-test-b",
	)
	if err != nil {
		t.Fatal(err)
	}

	if saved.Revision != 2 ||
		len(saved.History) != 3 {
		t.Fatalf(
			"unexpected checkpoint: %+v",
			saved,
		)
	}

	recovered := &Runner{
		Store:     store,
		Model:     model,
		Workspace: workspace,
		Provider:  "demo",
	}

	final, err := recovered.Run(
		context.Background(),
		"day12-test-b",
		root,
		"diagnose Go repository",
	)
	if err != nil {
		t.Fatal(err)
	}

	if final.Status != Succeeded ||
		final.Revision != 7 ||
		final.Calls != 3 {
		t.Fatalf(
			"unexpected final checkpoint: %+v",
			final,
		)
	}

	// 第一次模型调用在故障前已经完成。
	// 整体仍只应该调用四次模型。
	if model.Count() != 4 {
		t.Fatalf(
			"model calls=%d, want 4",
			model.Count(),
		)
	}
}

// Test C：旧 Revision 不得覆盖新 Revision。
func TestDay12RevisionConflict(t *testing.T) {
	ctx := context.Background()

	store := newMemoryStore()

	initial := Snapshot{
		RunID:    "day12-cas",
		Repo:     "/tmp/trusted",
		Provider: "demo",
		Goal:     "test",
		Status:   Running,
		History: []json.RawMessage{
			json.RawMessage(
				`{"role":"user","content":"test"}`,
			),
		},
	}

	if err := store.Create(ctx, initial); err != nil {
		t.Fatal(err)
	}

	writerA := initial
	writerA.Revision = 1

	writerB := initial
	writerB.Revision = 1

	if err := store.Save(
		ctx, writerA, 0,
	); err != nil {
		t.Fatal(err)
	}

	err := store.Save(ctx, writerB, 0)

	if !errors.Is(err, ErrConflict) {
		t.Fatalf(
			"expected ErrConflict, got %v",
			err,
		)
	}
}

// Test D：同一 RunID 不得关联另一任务。
func TestDay12RunIdentity(t *testing.T) {
	root, workspace := day12TestWorkspace(t)

	store := newMemoryStore()

	runner := &Runner{
		Store:     store,
		Model:     repodiag.DemoModel{},
		Workspace: workspace,
		Provider:  "demo",
	}

	_, err := runner.Run(
		context.Background(),
		"day12-same-id",
		root,
		"first goal",
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = runner.Run(
		context.Background(),
		"day12-same-id",
		root,
		"different goal",
	)

	if err == nil {
		t.Fatal(
			"expected RunID identity mismatch",
		)
	}
}

// Test E：已完成的 Run 再启动，不重复调用模型。
func TestDay12CompletedRunDoesNotRestart(
	t *testing.T,
) {
	root, workspace := day12TestWorkspace(t)
	store := newMemoryStore()

	model := &countingModel{
		base: repodiag.DemoModel{},
	}

	runner := &Runner{
		Store:     store,
		Model:     model,
		Workspace: workspace,
		Provider:  "demo",
	}

	first, err := runner.Run(
		context.Background(),
		"day12-completed",
		root,
		"diagnose",
	)
	if err != nil {
		t.Fatal(err)
	}

	countBefore := model.Count()

	second, err := runner.Run(
		context.Background(),
		"day12-completed",
		root,
		"diagnose",
	)
	if err != nil {
		t.Fatal(err)
	}

	if second.Revision != first.Revision ||
		second.Status != Succeeded {
		t.Fatalf(
			"completed run unexpectedly changed: %+v",
			second,
		)
	}

	if model.Count() != countBefore {
		t.Fatal(
			"completed run called model again",
		)
	}
}
