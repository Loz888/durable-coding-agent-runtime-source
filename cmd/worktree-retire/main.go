package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"runtime-app/repair19"
	"time"
)

func run() error {
	id := flag.String("run-id", "", "one exact terminal Run ID")
	base := flag.String("base", "", "canonical managed worktree base")
	retention := flag.Duration("retention", 24*time.Hour, "minimum terminal age, >=1h")
	apply := flag.Bool("apply", false, "actually retire eligible worktree; default dry-run")
	flag.Parse()
	if *id == "" || *base == "" {
		return fmt.Errorf("exact run-id and canonical base required")
	}
	dsn := os.Getenv("DAY12_DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("DAY12_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		return fmt.Errorf("database configuration rejected")
	}
	defer pool.Close()
	store, e := repair19.NewStore(pool, "day12", "day19")
	if e != nil {
		return e
	}
	out, e := store.RetireWorktree(ctx, *id, *base, *retention, !*apply)
	if e != nil {
		return fmt.Errorf("retirement refused; inspect identity, state and migration 016")
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
