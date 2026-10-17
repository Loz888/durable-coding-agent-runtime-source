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

	"runtime-app/durable"
	"runtime-app/repair19"
	"runtime-app/repodiag"
)

func main() {
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
			Pool: pool,
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

	scheduler.CodingRun = (durable.CodingHandler{
		Pool:        pool,
		PatchStore:  patchStore,
		PatchSchema: "day19",

		WorktreeBase: *worktreeBase,

		File: "math.go",

		MaxRounds:   2,
		TestTimeout: 30 * time.Second,
	}).Execute

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
