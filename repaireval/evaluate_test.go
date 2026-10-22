package repaireval

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"runtime-app/internal/testdb"
	"testing"
	"time"
)

func TestOracleRejectsVisiblePass(t *testing.T) {
	o := Observation{Kind: "repair", RuntimeStatus: "succeeded", RuntimeVerdict: "tests_passed", OriginalClean: true, ClaimEpoch: 1, Rounds: 1, ProposerCalls: 1}
	if expected(o) {
		t.Fatal("visible test pass treated as oracle success")
	}
	o.Kind = "oracle_rejects"
	if !expected(o) {
		t.Fatal("negative control not recognized")
	}
}
func TestFrozenEvaluationRealQueueAndOracle(t *testing.T) {
	m, sha, e := Load(filepath.Join("..", "evaldata", "repair-extension-20261021", "cases.json"))
	if e != nil {
		t.Fatal(e)
	}
	selected := []Case{}
	for _, c := range m.Cases {
		if c.ID == "divide" || c.ID == "already-add" || c.ID == "budget-divide" || c.ID == "invalid-fragment" || c.ID == "overfit-control" {
			selected = append(selected, c)
		}
	}
	m.Cases = selected
	if len(selected) != 5 {
		t.Fatal("missing category fixtures")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("dedicated test DB required; no SKIP")
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	out, e := Run(ctx, pool, m, sha, nil)
	if e != nil {
		t.Fatal(e)
	}
	if out.Expected != 5 || out.OracleRejectedPositive != 1 || out.VisiblePositive != 3 || out.OraclePositive != 2 || !out.Cleanup {
		t.Fatal(out)
	}
	if os.Getenv("DEEPSEEK_API_KEY") != "" {
		t.Log("No provider request is performed; caller environment is unrelated to scripted results")
	}
}
