package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
)

func main() {
	action := flag.String(
		"action", "",
		"register|prepare|propose|pending|apply|verify|summary",
	)

	url := flag.String(
		"db", "",
		"PostgreSQL DSN (prefer env)",
	)

	queue := flag.String(
		"queue-schema", "day12",
		"existing queue schema",
	)

	schema := flag.String(
		"patch-schema", "day19",
		"patch tables schema",
	)

	id := flag.String(
		"id", "",
		"logical RunID",
	)

	owner := flag.String(
		"owner", "",
		"exact lease owner token",
	)

	epoch := flag.Int64(
		"epoch", 0,
		"claimed lease epoch",
	)

	worktree := flag.String(
		"worktree", "",
		"managed worktree",
	)

	file := flag.String(
		"file", "math.go",
		"one allowed Go source file",
	)

	patchID := flag.String(
		"patch", "p1",
		"stable logical patch ID",
	)

	old := flag.String(
		"old", "",
		"exact old source fragment",
	)

	replacement := flag.String(
		"new", "",
		"replacement source fragment",
	)

	maxRounds := flag.Int(
		"max-rounds", 2,
		"durable max repair rounds",
	)

	model := flag.String(
		"model", "mock",
		"mock|deepseek (propose only)",
	)

	timeout := flag.Duration(
		"test-timeout",
		30*time.Second,
		"go test timeout",
	)

	inject := flag.Bool(
		"inject-sigkill",
		false,
		"LAB ONLY: SIGKILL after successful Rename",
	)

	flag.Parse()

	if *url == "" {
		*url = os.Getenv("DAY12_TEST_DATABASE_URL")
	}

	if *url == "" {
		*url = os.Getenv("DAY12_DATABASE_URL")
	}

	if *url == "" {
		fatal(errors.New(
			"-db or DAY12_TEST_DATABASE_URL required",
		))
	}

	// Never expose deliberate SIGKILL injection
	// on the normal day12/day19 path.
	if *inject &&
		(*queue != "day19_lab" ||
			*schema != "day19_lab" ||
			*action != "apply") {

		fatal(errors.New(
			"SIGKILL injection only allowed for day19_lab apply",
		))
	}

	ctx := context.Background()

	pool, err := pgxpool.New(ctx, *url)
	if err != nil {
		fatal(err)
	}
	defer pool.Close()

	store, err := repair19.NewStore(
		pool,
		*queue,
		*schema,
	)

	if err != nil {
		fatal(err)
	}

	r := repair19.Ref{
		ID:    *id,
		Owner: *owner,
		Epoch: *epoch,
	}

	var result any

	switch *action {

	case "register":
		err = store.Register(
			ctx,
			r,
			*worktree,
			*maxRounds,
		)

		result = map[string]any{
			"registered": err == nil,
			"run_id":     *id,
		}

	case "prepare", "propose":

		if *action == "propose" {
			existing, recoverErr := store.Recoverable(ctx, r)
			if recoverErr != nil {
				fatal(recoverErr)
			}
			// Return durable work before any proposer (including DeepSeek) is created.
			if len(existing) > 0 && existing[len(existing)-1].State != "verified_fail" {
				result = existing
				break
			}
			used, max, budgetErr := store.Summary(ctx, r.ID)
			if budgetErr != nil {
				fatal(budgetErr)
			}
			if used >= max {
				fatal(repair19.ErrBudget)
			}

			if *worktree == "" {
				fatal(errors.New(
					"-worktree required for propose",
				))
			}

			bound, boundErr := store.BoundWorktree(ctx, r)
			if boundErr != nil {
				fatal(boundErr)
			}
			requested, pathErr := repair17.OpenManaged(ctx, *worktree)
			if pathErr != nil {
				fatal(pathErr)
			}
			if requested.Path != bound.Path || requested.Identity != bound.Identity {
				fatal(repair19.ErrConflict)
			}
			currentSHA, inspectErr := repair17.Inspect(ctx, bound.Path, *file)
			if inspectErr != nil {
				fatal(inspectErr)
			}
			for i := len(existing) - 1; i >= 0; i-- {
				if existing[i].File == *file {
					if currentSHA != existing[i].After {
						fatal(repair19.ErrConflict)
					}
					break
				}
			}

			src, readErr := os.ReadFile(
				filepath.Join(
					*worktree, *file,
				),
			)

			if readErr != nil {
				fatal(readErr)
			}

			test, testErr := repair17.VerifyGo(
				ctx,
				*worktree,
				*timeout,
			)

			if testErr != nil {
				fatal(testErr)
			}

			if test.Passed {
				fatal(errors.New(
					"baseline already passing; no patch required",
				))
			}

			var proposer repair18.Proposer = repair18.ScriptedFix{}

			if *model == "deepseek" {
				proposer = repair18.DeepSeek{
					APIKey: os.Getenv(
						"DEEPSEEK_API_KEY",
					),
				}

			} else if *model != "mock" {
				fatal(errors.New(
					"invalid model",
				))
			}

			proposal, pErr := proposer.Suggest(
				ctx,
				repair18.Request{
					Round:      1,
					File:       *file,
					Source:     string(src),
					TestOutput: test.Output,
					Feedback:   "Durable patch proposal",
				},
			)

			if pErr != nil {
				fatal(pErr)
			}

			*old = proposal.Old
			*replacement = proposal.New
		}

		sha, shaErr := repair17.Inspect(
			ctx,
			*worktree,
			*file,
		)

		if shaErr != nil {
			fatal(shaErr)
		}

		result, err = store.Prepare(
			ctx,
			r,
			*patchID,
			repair17.Patch{
				Path:           *file,
				ExpectedSHA256: sha,
				Old:            *old,
				New:            *replacement,
			},
		)

	case "pending":
		result, err = store.Pending(ctx, r)

	case "apply":

		var hook func()

		if *inject {
			hook = func() {
				fmt.Fprintln(
					os.Stderr,
					"FAULT INJECTION: SIGKILL after successful Rename",
				)

				_ = syscall.Kill(
					os.Getpid(),
					syscall.SIGKILL,
				)
			}
		}

		result, err = store.ApplyOrRecover(
			ctx,
			r,
			*patchID,
			hook,
		)

	case "verify":
		result, err = store.Verify(
			ctx,
			r,
			*patchID,
			*timeout,
		)

	case "summary":
		var used, max int

		used, max, err = store.Summary(
			ctx, *id,
		)

		result = map[string]int{
			"rounds_used": used,
			"max_rounds":  max,
		}

	default:
		fatal(errors.New(
			"-action must be register|prepare|propose|pending|apply|verify|summary",
		))
	}

	if err != nil {
		fatal(err)
	}

	b, err := json.MarshalIndent(
		result,
		"",
		"  ",
	)

	if err != nil {
		fatal(err)
	}

	fmt.Println(string(b))
}

func fatal(err error) {
	fmt.Fprintln(
		os.Stderr,
		"day19:",
		err,
	)
	os.Exit(1)
}
