package executor

import (
	"context"
	"time"

	"runtime-app/task"
)

type DelayExecutor struct {
	Delay time.Duration
}

func (d DelayExecutor) Execute(
	ctx context.Context,
	t task.Task,
) (Output, error) {
	select {
	case <-time.After(d.Delay):
		return Output{
			ExitCode: 0,
		}, nil

	case <-ctx.Done():
		return Output{
			ExitCode: -1,
		}, ctx.Err()
	}
}
