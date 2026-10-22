package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"runtime-app/observability"
	"runtime-app/repaireval"
	"time"
)

func run() error {
	manifest := flag.String("manifest", "evaldata/repair-extension-20261021/cases.json", "frozen trusted synthetic manifest")
	output := flag.String("output", "", "required new private output directory")
	trusted := flag.Bool("trusted-fixtures", false, "acknowledge these tests execute as current local user; this is not a sandbox")
	traceFile := flag.String("trace-file", "", "optional new local OpenTelemetry NDJSON file")
	otlpEndpoint := flag.String("otlp-endpoint", "", "optional loopback OTLP base URL; default disabled")
	flag.Parse()
	if !*trusted || *output == "" {
		return fmt.Errorf("--trusted-fixtures and a new --output directory required")
	}
	m, sha, e := repaireval.Load(*manifest)
	if e != nil {
		return fmt.Errorf("invalid frozen manifest")
	}
	dsn := os.Getenv("DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		return fmt.Errorf("dedicated DAY12_TEST_DATABASE_URL required")
	}
	if e = os.Mkdir(*output, 0700); e != nil {
		return fmt.Errorf("output directory must not exist")
	}
	closeTrace, e := observability.ConfigureTelemetry(*traceFile, *otlpEndpoint, "repair-evaluation")
	if e != nil {
		return e
	}
	defer closeTrace()
	os.Unsetenv("DEEPSEEK_API_KEY")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		return fmt.Errorf("test database configuration rejected")
	}
	defer pool.Close()
	observations, e := os.OpenFile(filepath.Join(*output, "observations.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer observations.Close()
	out, runErr := repaireval.Run(ctx, pool, m, sha, func(o repaireval.Observation) error {
		fmt.Printf("case=%s kind=%s expected=%t visible_status=%s oracle_pass=%t\n", o.CaseID, o.Kind, o.ExpectedBehavior, o.RuntimeStatus, o.OraclePassed)
		return json.NewEncoder(observations).Encode(o)
	})
	raw, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*output, "report.json"), append(raw, '\n'), 0600); e != nil {
		return e
	}
	if runErr != nil {
		return fmt.Errorf("evaluation failed; partial observations preserved")
	}
	fmt.Printf("expected=%d/%d visible_positive=%d oracle_positive=%d visible_positive_oracle_negative=%d paid_calls=0\n", out.Expected, out.Total, out.VisiblePositive, out.OraclePositive, out.OracleRejectedPositive)
	return nil
}
func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
