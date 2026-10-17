package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"runtime-app/reliability15"
)

func main() {
	runID := flag.String(
		"run", "",
		"inspect one Agent Run",
	)
	flag.Parse()

	dsn := os.Getenv("DAY12_DATABASE_URL")
	if dsn == "" {
		log.Fatal("DAY12_DATABASE_URL is empty")
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		8*time.Second,
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

	store, err := reliability15.NewStore(
		pool, "day12",
	)
	if err != nil {
		log.Fatal(err)
	}

	summary, err := store.QueueSummary(ctx)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf(
		"queue: total=%d pending=%d running=%d success=%d failed=%d retries=%d granted=%d mean_run_sec=%.3f\n",
		summary.Total,
		summary.Pending,
		summary.Running,
		summary.Succeeded,
		summary.Failed,
		summary.ExplicitRetries,
		summary.GrantedAttempts,
		summary.MeanRunSeconds,
	)

	if *runID == "" {
		return
	}

	r, err := store.InspectRun(ctx, *runID)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf(
		"run=%s status=%s epoch=%d/%d explicit_retries=%d revision=%d\n",
		r.ID,
		r.Status,
		r.Epoch,
		r.MaxAttempts,
		r.Retries,
		r.Revision,
	)

	fmt.Printf(
		"failure_kind=%q reason=%q first_claimed=%v finished=%v\n",
		r.FailureKind,
		r.FailureReason,
		r.FirstClaimed,
		r.Finished,
	)
}
