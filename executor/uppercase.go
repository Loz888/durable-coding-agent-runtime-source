package executor

import (
	"context"
	"errors"
	"strings"

	"runtime-app/task"
)

type UppercaseExecutor struct{}

func (u UppercaseExecutor) Execute(
	ctx context.Context,
	t task.Task,
) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{
			ExitCode: -1,
		}, err
	}

	if t.Payload == "" {
		return Output{
			ExitCode: -1,
		}, errors.New("empty payload")
	}

	return Output{
		Stdout:   strings.ToUpper(t.Payload),
		ExitCode: 0,
	}, nil
}
