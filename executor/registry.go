package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"runtime-app/task"
)

var (
	ErrToolNotFound  = errors.New("unknown tool")
	ErrDuplicateTool = errors.New("duplicate tool")
)

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Executor
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]Executor),
	}
}

func (r *Registry) Register(
	name string,
	e Executor,
) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("empty tool name")
	}

	if e == nil {
		return errors.New("nil executor")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.tools[name]; exists {
		return fmt.Errorf(
			"%w: %s",
			ErrDuplicateTool,
			name,
		)
	}

	r.tools[name] = e
	return nil
}

func (r *Registry) Lookup(
	name string,
) (Executor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	e, ok := r.tools[name]
	return e, ok
}

func (r *Registry) Execute(
	ctx context.Context,
	t task.Task,
) (Output, error) {
	e, ok := r.Lookup(t.ToolName)

	if !ok {
		return Output{
			ExitCode: -1,
		}, fmt.Errorf(
			"%w: %s",
			ErrToolNotFound,
			t.ToolName,
		)
	}

	return e.Execute(ctx, t)
}

// Compile-time interface check.
var _ Executor = (*Registry)(nil)
