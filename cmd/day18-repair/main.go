package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"runtime-app/repair17"
	"runtime-app/repair18"
)

func main() {
	source := flag.String(
		"source", "",
		"clean Git repository",
	)
	base := flag.String(
		"base", "",
		"directory for managed worktrees",
	)
	runID := flag.String(
		"run", "",
		"unique RunID",
	)
	wt := flag.String(
		"worktree", "",
		"existing managed worktree",
	)
	file := flag.String(
		"file", "math.go",
		"one non-test Go file to repair",
	)
	mode := flag.String(
		"mode", "mock",
		"mock or deepseek",
	)
	modelName := flag.String(
		"model", "deepseek-flash",
		"DeepSeek model",
	)
	rounds := flag.Int(
		"max-rounds", 2,
		"maximum proposals (1..5)",
	)
	timeout := flag.Duration(
		"test-timeout",
		30*time.Second,
		"per-test timeout",
	)
	output := flag.String(
		"out", "",
		"optional result JSON file",
	)

	flag.Parse()

	ctx := context.Background()
	path := *wt

	if path == "" {
		if *source == "" ||
			*base == "" ||
			*runID == "" {
			die(fmt.Errorf(
				"supply -worktree or -source -base -run",
			))
		}

		w, err := repair17.Prepare(
			ctx, *source, *base, *runID,
		)
		if err != nil {
			die(err)
		}

		path = w.Path
	}

	var proposer repair18.Proposer

	switch *mode {
	case "mock":
		proposer = repair18.ScriptedFix{}

	case "deepseek":
		proposer = repair18.DeepSeek{
			APIKey: os.Getenv("DEEPSEEK_API_KEY"),
			Model:  *modelName,
		}

	default:
		die(fmt.Errorf(
			"-mode must be mock or deepseek",
		))
	}

	result, err := repair18.Run(
		ctx,
		proposer,
		repair18.Options{
			Worktree:    path,
			File:        *file,
			MaxRounds:   *rounds,
			TestTimeout: *timeout,
		},
	)
	if err != nil {
		die(err)
	}

	data, err := json.MarshalIndent(
		result, "", "  ",
	)
	if err != nil {
		die(err)
	}

	fmt.Println(string(data))

	if *output != "" {
		if err := os.MkdirAll(
			filepath.Dir(*output), 0755,
		); err != nil {
			die(err)
		}

		if err := os.WriteFile(
			*output,
			append(data, '\n'),
			0600,
		); err != nil {
			die(err)
		}
	}

	if result.Status != "repaired" &&
		result.Status != "already_passed" {
		os.Exit(2)
	}
}

func die(err error) {
	fmt.Fprintln(
		os.Stderr, "day18:", err,
	)
	os.Exit(1)
}
