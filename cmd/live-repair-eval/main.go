package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"runtime-app/repaireval"
	"time"
)

func run() error {
	child := flag.String("child", "", "private crash-helper input")
	manifest := flag.String("manifest", "evaldata/runtime-remaining-20261021/cases.json", "authorized frozen manifest")
	output := flag.String("output", "", "new private output directory")
	budget := flag.Int64("authorize-budget-nanousd", 0, "explicit fresh authorization; exactly 2000000000 for this frozen run")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	dsn := os.Getenv("DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("dedicated DAY12_TEST_DATABASE_URL required")
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		return fmt.Errorf("test database configuration rejected")
	}
	defer pool.Close()
	if *child != "" {
		return repaireval.LiveChild(ctx, pool, *child)
	}
	if *budget != 2000000000 || *output == "" {
		return fmt.Errorf("explicit frozen-run budget authorization and new output directory required")
	}
	m, e := repaireval.LoadLive(*manifest)
	if e != nil {
		return e
	}
	if e = os.Mkdir(*output, 0700); e != nil {
		return fmt.Errorf("output must be new")
	}
	log, e := os.OpenFile(filepath.Join(*output, "observations.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer log.Close()
	report, runErr := repaireval.RunLive(ctx, pool, m, func(o repaireval.LiveObservation) error {
		fmt.Printf("case=%s status=%s oracle=%t calls=%d usage_known=%d recovery=%t\n", o.CaseID, o.RuntimeStatus, o.OraclePassed, o.PhysicalCalls, o.KnownCalls, o.ResponseReused)
		return json.NewEncoder(log).Encode(o)
	})
	raw, e := json.MarshalIndent(report, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*output, "report.json"), append(raw, '\n'), 0600); e != nil {
		return e
	}
	return runErr
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
