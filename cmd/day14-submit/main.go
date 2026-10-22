package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime-app/observability"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/durable"
	"runtime-app/repodiag"
)

func main() {
	traceFile := flag.String("trace-file", "", "new private OpenTelemetry NDJSON file; empty disables export")
	otlpEndpoint := flag.String("otlp-endpoint", "", "optional loopback OTLP base URL; default disabled")
	id := flag.String(
		"id", "",
		"new unique run ID (required)",
	)

	repo := flag.String(
		"repo", "",
		"trusted local Go repository (required)",
	)

	goal := flag.String(
		"goal",
		"检查 Go 仓库的测试结果",
		"diagnosis goal",
	)

	mode := flag.String(
		"mode",
		"demo",
		"demo or deepseek",
	)

	priority := flag.Int(
		"priority",
		0,
		"queue priority: -100..100 (larger first)",
	)

	flag.Parse()
	closeTrace, traceErr := observability.ConfigureTelemetry(*traceFile, *otlpEndpoint, "runtime-submit")
	if traceErr != nil {
		log.Fatal(traceErr)
	}
	defer func() {
		if e := closeTrace(); e != nil {
			log.Print("trace shutdown failed")
		}
	}()

	if *id == "" || *repo == "" {
		log.Fatal("-id and -repo required")
	}

	root, err := filepath.Abs(*repo)
	if err != nil {
		log.Fatal(err)
	}

	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		log.Fatal(err)
	}

	if _, err := repodiag.NewWorkspace(root); err != nil {
		log.Fatal(err)
	}

	provider := *mode

	switch *mode {
	case "demo":

	case "deepseek":
		model := os.Getenv("DEEPSEEK_MODEL")
		if model == "" {
			model = "deepseek-flash"
		}

		provider = "deepseek:" + model

	default:
		log.Fatal("unsupported -mode")
	}

	dsn := os.Getenv("DAY12_DATABASE_URL")
	if dsn == "" {
		log.Fatal(
			"DAY12_DATABASE_URL not set",
		)
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatal(err)
	}

	userMessage, err := json.Marshal(
		map[string]string{
			"role":    "user",
			"content": *goal,
		},
	)
	if err != nil {
		log.Fatal(err)
	}

	initial := durable.Snapshot{
		RunID:    *id,
		Repo:     root,
		Provider: provider,
		Goal:     *goal,
		Status:   durable.Running,

		History: []json.RawMessage{
			userMessage,
		},
	}

	if err := (durable.Queue{
		Pool: pool,
	}).Submit(
		ctx,
		initial,
		*priority,
	); err != nil {
		log.Fatal("submit: ", err)
	}

	fmt.Printf(
		"SUBMITTED run=%s priority=%d provider=%s repo=%s\n",
		*id,
		*priority,
		provider,
		root,
	)
}
