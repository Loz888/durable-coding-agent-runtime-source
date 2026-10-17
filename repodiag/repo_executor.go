package repodiag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"runtime-app/executor"
	"runtime-app/task"
)

type RepoExecutor struct {
	Agent *Agent
}

func (e RepoExecutor) Execute(
	ctx context.Context,
	t task.Task,
) (executor.Output, error) {

	if e.Agent == nil {
		return executor.Output{
			ExitCode: -1,
		}, errors.New("repo agent is nil")
	}

	if t.ToolName != "repo_diagnose" {
		return executor.Output{
			ExitCode: -1,
		}, fmt.Errorf(
			"unsupported tool: %s",
			t.ToolName,
		)
	}

	report, err := e.Agent.Run(
		ctx,
		t.Payload,
	)

	if err != nil {
		return executor.Output{
			ExitCode: 1,
			Stderr:   err.Error(),
		}, err
	}

	data, err := json.MarshalIndent(
		report,
		"",
		"  ",
	)

	if err != nil {
		return executor.Output{
			ExitCode: 1,
		}, err
	}

	return executor.Output{
		ExitCode: 0,
		Stdout:   string(data),
	}, nil
}
