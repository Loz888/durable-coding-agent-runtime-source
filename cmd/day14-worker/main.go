package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"path/filepath"
	"runtime-app/durable"
	"runtime-app/executor"
	"runtime-app/modelbudget"
	"runtime-app/repair17"
	"runtime-app/repair18"
	"runtime-app/repair19"
	"runtime-app/repodiag"
	"runtime-app/task"
)

func main() {
	liveBudget := flag.Int64("live-budget-nanousd", 0, "explicit experiment cap in USD nanounits, 0 disables paid pilot requests; maximum 2000000000")
	profile := flag.String("profile", "trusted-local", "trusted-local or internal-pilot; pilot requires Docker and a pinned image")
	sandboxImage := flag.String("sandbox-image", "", "operator pinned image@sha256:digest")
	sandboxRoot := flag.String("sandbox-root", "", "operator canonical private snapshot directory")
	name := flag.String(
		"name",
		"worker-a",
		"human-readable process label",
	)

	workers := flag.Int(
		"workers",
		2,
		"max concurrent Agent Runs in THIS process",
	)

	poll := flag.Duration(
		"poll",
		300*time.Millisecond,
		"queue poll interval",
	)

	ttl := flag.Duration(
		"ttl",
		8*time.Second,
		"Lease TTL",
	)

	renew := flag.Duration(
		"renew",
		time.Second,
		"Lease renewal interval",
	)

	timeout := flag.Duration(
		"timeout",
		3*time.Minute,
		"max time of a single Agent Run",
	)

	pauseAfter := flag.Int64(
		"pause-after",
		-1,
		"fault test: pause after this revision",
	)

	pause := flag.Duration(
		"pause",
		180*time.Second,
		"fault test: pause duration",
	)

	base := os.Getenv("WORKTREE_BASE")
	if base == "" {
		base = "worktrees"
	}
	worktreeBase := flag.String("worktree-base", base, "base directory for managed coding Worktrees")

	flag.Parse()

	// 每个进程启动时创建唯一 Session Token。
	var token [16]byte

	if _, err := rand.Read(token[:]); err != nil {
		log.Fatal(err)
	}

	session := *name + ":" +
		hex.EncodeToString(token[:])

	dsn := os.Getenv(
		"DAY12_DATABASE_URL",
	)
	if dsn == "" {
		log.Fatal(
			"DAY12_DATABASE_URL required",
		)
	}

	// SIGINT / SIGTERM 触发协作式退出。
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	pool, err := pgxpool.New(
		ctx,
		dsn,
	)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatal(err)
	}

	scheduler := &durable.Scheduler{
		Queue: durable.Queue{
			Pool: pool, Pilot: *profile == "internal-pilot",
		},

		Owner:      session,
		Workers:    *workers,
		PollEvery:  *poll,
		TTL:        *ttl,
		RenewEvery: *renew,
		RunTimeout: *timeout,

		Models: func(
			provider string,
		) (repodiag.Model, error) {
			if provider == "demo" {
				return repodiag.DemoModel{}, nil
			}

			modelID, ok := strings.CutPrefix(
				provider,
				"deepseek:",
			)

			if !ok || modelID == "" {
				return nil, fmt.Errorf(
					"unknown provider %q",
					provider,
				)
			}

			key := os.Getenv(
				"DEEPSEEK_API_KEY",
			)

			if key == "" {
				return nil, errors.New(
					"DEEPSEEK_API_KEY not configured",
				)
			}

			return repodiag.DeepSeekModel{
				APIKey: key,
				Model:  modelID,
			}, nil
		},

		Logf: func(
			format string,
			args ...any,
		) {
			log.Printf(format, args...)
		},
	}

	patchStore, err := repair19.NewStore(
		pool,
		"day12",
		"day19",
	)
	if err != nil {
		log.Fatal(err)
	}

	handler := durable.CodingHandler{
		Pool:        pool,
		PatchStore:  patchStore,
		PatchSchema: "day19",

		WorktreeBase: *worktreeBase,

		File: "math.go",

		MaxRounds:   2,
		TestTimeout: 30 * time.Second,
	}
	if *profile == "internal-pilot" {
		canonical, err := filepath.Abs(*sandboxRoot)
		if err != nil || *sandboxRoot == "" {
			log.Fatal("canonical sandbox root required")
		}
		if err = os.MkdirAll(canonical, 0700); err != nil {
			log.Fatal("sandbox root unavailable")
		}
		policy := executor.GoSandbox{Image: *sandboxImage, Root: canonical, Timeout: *timeout, MaxBytes: 64 << 20, MaxFiles: 10000}
		check, done := context.WithTimeout(ctx, 5*time.Second)
		err = policy.Probe(check)
		done()
		if err != nil {
			log.Fatal("pinned sandbox engine/image unavailable; refusing host fallback")
		}
		verify := func(c context.Context, path string, limit time.Duration) (repair17.TestResult, error) {
			execution := policy
			execution.Timeout = limit
			started := time.Now()
			output, e := execution.Execute(c, task.Task{Command: "go-test", Payload: path})
			return repair17.TestResult{Passed: e == nil && output.ExitCode == 0, ExitCode: output.ExitCode, DurationMS: time.Since(started).Milliseconds(), Output: output.Stdout + "\n" + output.Stderr}, e
		}
		patchStore.VerifyGo = verify
		handler.VerifyGo = verify
		handler.ExpectedCommit = func(c context.Context, job durable.ClaimedRun) (string, error) {
			if job.Provider != "coding:mock" && !(job.Provider == "coding:deepseek" && *liveBudget > 0 && *liveBudget <= 2000000000) {
				return "", errors.New("pilot paid providers disabled until durable governance acceptance")
			}
			var commit string
			e := pool.QueryRow(c, "SELECT source_commit FROM pilot.runs WHERE run_id=$1", job.RunID).Scan(&commit)
			if e != nil {
				return "", errors.New("pilot requires authenticated registered submission")
			}
			return commit, nil
		}
		handler.WrapProposer = func(c context.Context, job durable.ClaimedRun, next repair18.Proposer) (repair18.Proposer, error) {
			if job.Provider != "coding:deepseek" {
				return next, nil
			}
			var team string
			if e := pool.QueryRow(c, "SELECT team FROM pilot.runs WHERE run_id=$1", job.RunID).Scan(&team); e != nil {
				return nil, errors.New("missing trusted team")
			}
			return modelbudget.Proposer{Pool: pool, Next: next, RunID: job.RunID, Owner: job.Lease.Owner, Epoch: job.Lease.Epoch, Policy: modelbudget.Policy{Experiment: "pilot-runtime-20261019", Team: team, BudgetNano: *liveBudget, GlobalLimit: 2, TeamLimit: 1, MaxRunCalls: 4, MaxProjectCalls: 200, Timeout: 75 * time.Second, UncertainFor: 120 * time.Second}}, nil
		}
		scheduler.Models = func(string) (repodiag.Model, error) { return nil, errors.New("pilot diagnosis host tools disabled") }
	} else if *profile != "trusted-local" {
		log.Fatal("unknown deployment profile")
	}
	scheduler.CodingRun = handler.Execute

	// 只有显式指定 -pause-after 才启用故障注入。
	if *pauseAfter >= 0 {
		scheduler.AfterSave = func(
			ss durable.Snapshot,
		) error {
			if ss.Revision != *pauseAfter {
				return nil
			}

			log.Printf(
				"PAUSED run=%s pid=%d after revision=%d",
				ss.RunID,
				os.Getpid(),
				ss.Revision,
			)

			timer := time.NewTimer(*pause)
			defer timer.Stop()

			select {
			case <-ctx.Done():
				return ctx.Err()

			case <-timer.C:
				return nil
			}
		}
	}

	log.Printf(
		"WORKER_READY name=%s concurrency=%d",
		*name,
		*workers,
	)

	if err := scheduler.Run(ctx); err != nil {
		log.Fatal(err)
	}

	log.Print("WORKER_STOPPED")
}
