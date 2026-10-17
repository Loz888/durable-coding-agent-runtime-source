package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"runtime-app/executor"
	"runtime-app/runtimecore"
	"runtime-app/task"
)

func main() {
	// 1. Runtime 父 Context：8 秒
	ctx, cancel := context.WithTimeout(
		context.Background(),
		8*time.Second,
	)
	defer cancel()

	// 2. 创建 Executor Registry
	tools := executor.NewRegistry()

	err := tools.Register(
		"shell",
		executor.ShellExecutor{
			Timeout: 500 * time.Millisecond,
		},
	)
	if err != nil {
		panic(err)
	}

	err = tools.Register(
		"uppercase",
		executor.UppercaseExecutor{},
	)
	if err != nil {
		panic(err)
	}

	err = tools.Register(
		"delay",
		executor.DelayExecutor{
			Delay: 100 * time.Millisecond,
		},
	)
	if err != nil {
		panic(err)
	}

	// 3. 创建 Runtime
	// Registry 需要实现 Executor 接口
	r := runtimecore.NewRuntime(
		tools,
		3, // Worker Count
		2, // Queue Size
	)

	r.Start(ctx)

	// 4. 创建六个 Task
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
			Name:     "uppercase-task",
			ToolName: "uppercase",
			Payload:  "hello agent",
		},
		{
			ID:       3,
			Name:     "delay-task",
			ToolName: "delay",
		},
		{
			ID:       4,
			Name:     "unknown-task",
			ToolName: "missing-tool",
		},
		{
			ID:       5,
			Name:     "fail-task",
			ToolName: "shell",
			Command:  "false",
		},
		{
			ID:       6,
			Name:     "timeout-task",
			ToolName: "shell",
			Command:  "sleep",
			Args:     []string{"3"},
		},
	}

	// 5. Producer Goroutine
	submittedCh := make(chan int, 1)

	go func() {
		submitted := 0

		defer func() {
			r.Close()
			submittedCh <- submitted
		}()

		for _, t := range tasks {
			err := r.Submit(ctx, t)

			if err != nil {
				fmt.Println(
					"submit failed:",
					err,
				)
				break
			}

			fmt.Printf(
				"submitted task=%d tool=%s\n",
				t.ID,
				t.ToolName,
			)

			submitted++
		}
	}()

	// 6. Result Collector
	received := 0
	failed := 0
	duplicates := 0
	passedChecks := 0

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

		fmt.Println("\n===== TASK RESULT =====")

		fmt.Printf(
			"worker=%d task=%d name=%s\n",
			result.WorkerID,
			result.TaskID,
			result.TaskName,
		)

		fmt.Printf("stdout: %q\n", result.Stdout)
		fmt.Printf("stderr: %q\n", result.Stderr)
		fmt.Printf("exit code: %d\n", result.ExitCode)
		fmt.Printf("error: %v\n", result.Err)

		// 7. 按 TaskID 检查实验结果
		valid := false

		switch result.TaskID {
		case 1:
			valid = result.Err == nil &&
				result.ExitCode == 0 &&
				strings.TrimSpace(result.Stdout) == "hello agent"

		case 2:
			valid = result.Err == nil &&
				strings.TrimSpace(result.Stdout) == "HELLO AGENT"

		case 3:
			valid = result.Err == nil

		case 4:
			// 不存在的 Tool 应返回错误
			valid = result.Err != nil

		case 5:
			valid = result.Err != nil &&
				result.ExitCode == 1

		case 6:
			valid = errors.Is(
				result.Err,
				context.DeadlineExceeded,
			) && result.ExitCode == -1
		}

		if valid {
			passedChecks++
			fmt.Println("CHECK PASSED")
		} else {
			fmt.Println("CHECK FAILED")
		}
	}

	// 8. 等待 Producer 完成
	submitted := <-submittedCh

	// 9. 最终统计
	fmt.Println("\n===== SUMMARY =====")

	fmt.Println("submitted:", submitted)
	fmt.Println("received:", received)
	fmt.Println("failed:", failed)
	fmt.Println("duplicates:", duplicates)
	fmt.Println("passed checks:", passedChecks)

	if submitted == 6 &&
		received == 6 &&
		failed == 3 &&
		duplicates == 0 &&
		passedChecks == 6 {

		fmt.Println("MULTI TOOL RUN PASSED")
	} else {
		fmt.Println("MULTI TOOL RUN INCOMPLETE")
	}
}
