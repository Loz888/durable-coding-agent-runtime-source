package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"runtime-app/task"
)

var ErrCommandNotAllowed = errors.New("command not allowed")

type ShellExecutor struct {
	Timeout time.Duration
}

func (s ShellExecutor) Execute(
	ctx context.Context,
	t task.Task,
) (Output, error) {
	if s.Timeout <= 0 {
		return Output{ExitCode: -1},
			errors.New("invalid timeout")
	}

	// 教学版：只允许明确列出的命令
	switch t.Command {
	case "echo", "pwd", "sleep", "false":
	default:
		return Output{ExitCode: -1},
			fmt.Errorf(
				"command not allowed: %q",
				t.Command,
			)
	}

	taskCtx, cancel := context.WithTimeout(
		ctx,
		s.Timeout,
	)
	defer cancel()

	cmd := exec.CommandContext(
		taskCtx,
		t.Command,
		t.Args...,
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := Output{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: -1,
	}

	if cmd.ProcessState != nil {
		output.ExitCode =
			cmd.ProcessState.ExitCode()
	}

	if err != nil {
		if taskCtx.Err() != nil {
			return output, taskCtx.Err()
		}

		return output, fmt.Errorf(
			"execute %q: %w",
			t.Command,
			err,
		)
	}

	return output, nil
}
