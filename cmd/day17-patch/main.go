package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"runtime-app/repair17"
)

func main() {
	action := flag.String(
		"action", "",
		"prepare | inspect | apply | verify | diff",
	)

	source := flag.String(
		"source", "",
		"clean independent Git repository root",
	)

	base := flag.String(
		"base", "",
		"directory outside source for linked worktrees",
	)

	run := flag.String(
		"run", "",
		"unique run ID",
	)

	worktree := flag.String(
		"worktree", "",
		"managed worktree path",
	)

	path := flag.String(
		"path", "",
		"relative Go source file",
	)

	patchFile := flag.String(
		"patch", "",
		"JSON patch file",
	)

	timeout := flag.Duration(
		"timeout",
		45*time.Second,
		"go test timeout, max 5m",
	)

	flag.Parse()

	ctx := context.Background()

	var v any
	var err error

	switch *action {
	case "prepare":
		if *source == "" ||
			*base == "" ||
			*run == "" {
			fatal(fmt.Errorf(
				"-source, -base and -run required",
			))
		}

		v, err = repair17.Prepare(
			ctx, *source, *base, *run,
		)

	case "inspect":
		if *worktree == "" || *path == "" {
			fatal(fmt.Errorf(
				"-worktree and -path required",
			))
		}

		var sha string

		sha, err = repair17.Inspect(
			ctx, *worktree, *path,
		)

		v = map[string]string{
			"path":   *path,
			"sha256": sha,
		}

	case "apply":
		if *worktree == "" || *patchFile == "" {
			fatal(fmt.Errorf(
				"-worktree and -patch required",
			))
		}

		b, readErr := os.ReadFile(*patchFile)
		if readErr != nil {
			fatal(readErr)
		}

		var p repair17.Patch

		dec := json.NewDecoder(
			strings.NewReader(string(b)),
		)
		dec.DisallowUnknownFields()

		if err = dec.Decode(&p); err != nil {
			fatal(err)
		}

		v, err = repair17.Apply(
			ctx, *worktree, p,
		)

	case "verify":
		if *worktree == "" {
			fatal(fmt.Errorf(
				"-worktree required",
			))
		}

		v, err = repair17.VerifyGo(
			ctx, *worktree, *timeout,
		)

	case "diff":
		if *worktree == "" {
			fatal(fmt.Errorf(
				"-worktree required",
			))
		}

		v, err = repair17.Diff(
			ctx, *worktree,
		)

	default:
		fatal(fmt.Errorf(
			"-action must be prepare, inspect, apply, verify or diff",
		))
	}

	if err != nil {
		fatal(err)
	}

	if s, ok := v.(string); ok {
		fmt.Println(s)
		return
	}

	output, err := json.MarshalIndent(
		v, "", "  ",
	)
	if err != nil {
		fatal(err)
	}

	fmt.Println(string(output))
}

func fatal(err error) {
	fmt.Fprintln(
		os.Stderr,
		"day17:",
		err,
	)
	os.Exit(1)
}
