package main

import (
	"context"
	"fmt"
	"runtime-app/executor"
	"runtime-app/runtimecore"
	"runtime-app/task"
	"time"
)

func main() {
	// Runtime 总超时：10 秒
	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	// 注册 shell 和 uppercase 工具，由 Registry 路由执行

	registry := executor.NewRegistry()

	if err := registry.Register(
		"shell",
		executor.ShellExecutor{
			Timeout: time.Second,
		},
	); err != nil {
		panic(err)
	}

	if err := registry.Register(
		"uppercase",
		executor.UppercaseExecutor{},
	); err != nil {
		panic(err)
	}

	r := runtimecore.NewRuntime(
		registry,
		3,
		2,
	)
	r.Start(ctx)

	submittedCh := make(chan int, 1)

	// Producer
	go func() {
		defer r.Close()

		submitted := 0

		tasks := []task.Task{
			{
				ID:       1,
				Name:     "echo-task",
				ToolName: "shell",
				Command:  "echo",
				Args:     []string{"hello agent"},
			},
			{
				ID:       2,
				Name:     "pwd-task",
				ToolName: "shell",
				Command:  "pwd",
			},
			{
				ID:       3,
				Name:     "fail-task",
				ToolName: "shell",
				Command:  "false",
			},
			{
				ID:       4,
				Name:     "sleep-task",
				ToolName: "shell",
				Command:  "sleep",
				Args:     []string{"3"},
			},
			{
				ID:       5,
				Name:     "uppercase-task",
				ToolName: "uppercase",
				Payload:  "hello agent",
			},
			{
				ID:       6,
				Name:     "unknown-task",
				ToolName: "missing-tool",
			},
		}
		for _, t := range tasks {
			err := r.Submit(ctx, t)

			if err != nil {
				fmt.Println(
					"submit failed:",
					err,
				)
				break
			}

			submitted++
		}

		submittedCh <- submitted
	}()

	// Result collector
	received := 0
	failed := 0
	duplicates := 0

	seen := make(map[int]bool)

	for result := range r.Results() {
		received++

		if seen[result.TaskID] {
			duplicates++
		}

		seen[result.TaskID] = true

		if result.Err != nil {
			failed++
		}

		fmt.Printf(
			"\nworker=%d task=%d name=%s\n",
			result.WorkerID,
			result.TaskID,
			result.TaskName,
		)

		fmt.Printf(
			"stdout: %q\n",
			result.Stdout,
		)

		fmt.Printf(
			"stderr: %q\n",
			result.Stderr,
		)

		fmt.Printf(
			"exit code: %d\n",
			result.ExitCode,
		)

		fmt.Printf(
			"error: %v\n",
			result.Err,
		)
	}

	submitted := <-submittedCh

	fmt.Println("\n===== SUMMARY =====")

	fmt.Println("submitted:", submitted)
	fmt.Println("received:", received)
	fmt.Println("failed:", failed)
	fmt.Println("duplicates:", duplicates)

	// false、sleep 超时、未知工具预期各失败一次
	if submitted == 6 &&
		received == 6 &&
		failed == 3 &&
		duplicates == 0 {

		fmt.Println("NORMAL RUN PASSED")
	} else {
		fmt.Println("NORMAL RUN INCOMPLETE")
	}
}
