package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"runtime-app/repodiag"
	"runtime-app/runtimecore"
	"runtime-app/task"
)

func main() {

	repoPath := flag.String(
		"repo",
		"",
		"path to an authorized local Go repository",
	)

	mode := flag.String(
		"mode",
		"demo",
		"demo or deepseek",
	)

	goal := flag.String(
		"goal",
		"检查这个Go仓库的测试失败原因",
		"agent diagnosis goal",
	)

	flag.Parse()

	if *repoPath == "" {
		log.Fatal(
			"please provide -repo",
		)
	}

	workspace, err := repodiag.NewWorkspace(
		*repoPath,
	)
	if err != nil {
		log.Fatal(
			"create workspace: ",
			err,
		)
	}

	var model repodiag.Model

	switch *mode {

	case "demo":

		model = repodiag.DemoModel{}

	case "deepseek":

		apiKey := os.Getenv(
			"DEEPSEEK_API_KEY",
		)

		if apiKey == "" {
			log.Fatal(
				"DEEPSEEK_API_KEY is not set",
			)
		}

		modelID := os.Getenv(
			"DEEPSEEK_MODEL",
		)

		if modelID == "" {
			modelID = "deepseek-flash"
		}

		model = repodiag.DeepSeekModel{
			APIKey: apiKey,
			Model:  modelID,
		}

	default:

		log.Fatalf(
			"unsupported mode %q",
			*mode,
		)
	}

	agent := &repodiag.Agent{
		Model:     model,
		Workspace: workspace,
		MaxTurns:  8,
		MaxCalls:  12,
	}

	// 复用 Day 1～Day 7 的 Runtime。
	r := runtimecore.NewRuntime(
		repodiag.RepoExecutor{
			Agent: agent,
		},
		1,
		2,
	)

	// 限制整个 Agent Run 的持续时间。
	ctx, cancel := context.WithTimeout(
		context.Background(),
		180*time.Second,
	)
	defer cancel()

	r.Start(ctx)

	t := task.Task{
		ID:       11001,
		Name:     "repo-diagnosis",
		ToolName: "repo_diagnose",
		Payload:  *goal,
	}

	if err := r.Submit(
		ctx,
		t,
	); err != nil {
		log.Fatal(
			"submit failed: ",
			err,
		)
	}

	r.Close()

	failed := false

	for result := range r.Results() {

		fmt.Println(
			"========== AGENT RESULT ==========",
		)

		fmt.Println(result.Stdout)

		if result.Err != nil {
			failed = true

			log.Printf(
				"agent failed: %v",
				result.Err,
			)
		}
	}

	if failed || ctx.Err() != nil {
		os.Exit(1)
	}

	fmt.Println(
		"========== RUN COMPLETED ==========",
	)
}
