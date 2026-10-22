package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"regexp"
	"runtime-app/artifactstore"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var testDatabaseName = regexp.MustCompile(`(^test($|_)|_test($|_))`)

// referenceReader opens a separate read-only committed snapshot per call. It
// never discovers schemas or claims that the caller's list is complete.
func referenceReader(pool *pgxpool.Pool) artifactstore.ReferenceSnapshot {
	return func(ctx context.Context, schemas []string) (map[string]struct{}, error) {
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return nil, errors.New("read-only test database unavailable")
		}
		defer tx.Rollback(ctx)
		var database string
		if err = tx.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil || !testDatabaseName.MatchString(strings.ToLower(database)) {
			return nil, errors.New("dedicated test database required")
		}
		refs := map[string]struct{}{}
		for _, schema := range schemas {
			rows, e := tx.Query(ctx, "SELECT artifact_sha FROM "+pgx.Identifier{schema, "repair_runs"}.Sanitize()+" WHERE artifact_sha<>''")
			if e != nil {
				return nil, errors.New("declared reference schema unavailable")
			}
			for rows.Next() {
				var key string
				if e = rows.Scan(&key); e != nil {
					rows.Close()
					return nil, errors.New("reference observation failed")
				}
				refs[key] = struct{}{}
				if len(refs) > 100000 {
					rows.Close()
					return nil, errors.New("reference observation too large")
				}
			}
			e = rows.Err()
			rows.Close()
			if e != nil {
				return nil, errors.New("reference observation failed")
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, errors.New("read-only reference observation failed")
		}
		return refs, nil
	}
}

func run(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("artifact-inventory", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := flags.String("root", "", "existing canonical private artifact directory (never created)")
	schemaList := flags.String("reference-schemas", "", "explicit comma-separated list of all known reference schemas; no completeness guarantee")
	envName := flags.String("database-env", "", "TEST_DATABASE_URL or DAY12_TEST_DATABASE_URL; no production fallback")
	if err := flags.Parse(args); err != nil {
		return errors.New("invalid inventory arguments; require root, reference-schemas and database-env; no apply mode exists")
	}
	if flags.NArg() != 0 || *root == "" || *schemaList == "" || (*envName != "TEST_DATABASE_URL" && *envName != "DAY12_TEST_DATABASE_URL") {
		return errors.New("explicit root, reference-schemas and test database environment name required")
	}
	schemas := strings.Split(*schemaList, ",")
	if err := artifactstore.InventorySchemas(schemas); err != nil {
		return err
	}
	dsn := os.Getenv(*envName)
	if strings.TrimSpace(dsn) == "" {
		return errors.New("selected dedicated test database environment variable is empty")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return errors.New("test database configuration rejected")
	}
	config.MaxConns = 1
	config.MinConns = 0
	config.MinIdleConns = 0
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	config.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return errors.New("read-only test database unavailable")
	}
	defer pool.Close()
	report, err := artifactstore.ScanInventory(ctx, *root, schemas, referenceReader(pool))
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(report)
}
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
