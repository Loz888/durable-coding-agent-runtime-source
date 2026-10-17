package executor

import (
	"context"

	"runtime-app/task"
)

type Output struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

type Executor interface {
	Execute(
		ctx context.Context,
		t task.Task,
	) (Output, error)
}
