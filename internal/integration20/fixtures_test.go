package integration20

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"runtime-app/internal/testdb"
	"runtime-app/repair17"
	"runtime-app/repair19"
)

// patchLab owns its schema metadata. It uses only public production APIs.
type patchLab struct {
	*repair19.Store
	schema string
	dsn    string
}

func (s *patchLab) table(name string) string {
	return pgx.Identifier{s.schema, name}.Sanitize()
}

func newPatchLab(t *testing.T) *patchLab {
	t.Helper()
	dsn := testdb.DatabaseURL(t, "DAY12_TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("DAY12_TEST_DATABASE_URL is required; integration tests must not SKIP")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var database string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	if database != "agent_runtime_day14_test" {
		t.Fatalf("refusing non-test database %q", database)
	}
	var queueTable *string
	if err := pool.QueryRow(ctx, "SELECT to_regclass('day12.agent_runs')::text").Scan(&queueTable); err != nil || queueTable == nil {
		t.Fatalf("run migrations 001-004 in the dedicated test database: %v", err)
	}

	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	schema := "integration20_" + hex.EncodeToString(token[:])
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if _, err := pool.Exec(cleanup, "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Errorf("cleanup integration schema: %v", err)
		}
	})
	// Exercise the existing schema rather than duplicating Patch Store DDL.
	// Only the isolated schema name changes; day12 and formal day19 stay intact.
	for _, name := range []string{"005_durable_patch.sql", "006_repair_binding.sql", "010_verification_input.sql", "013_multifile_patch.sql", "014_model_response_cache.sql", "015_portable_worktree.sql", "016_worktree_retention.sql"} {
		migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		sql := strings.ReplaceAll(string(migration), "day19", schema)
		if _, err := pool.Exec(ctx, sql); err != nil {
			t.Fatalf("apply isolated %s: %v", name, err)
		}
	}
	store, err := repair19.NewStore(pool, "day12", schema)
	if err != nil {
		t.Fatal(err)
	}
	// The production queue has a fixed day12 schema. Serialize independent
	// fixtures across packages, while retaining real worker concurrency.
	testdb.LockQueue(t, pool)
	return &patchLab{Store: store, schema: schema, dsn: dsn}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	argv := append([]string{"-C", root}, args...)
	if b, err := exec.Command("git", argv...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, b)
	}
}

func makeWorktree(t *testing.T, runID string) (repair17.Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "source")
	if err := os.Mkdir(source, 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":       "module example.com/day19fixture\n\ngo 1.23\n",
		"math.go":      "package example\nfunc Divide(a,b int) int { return a * b }\n",
		"math_test.go": "package example\nimport \"testing\"\nfunc TestDivide(t *testing.T) {if Divide(12,3)!=4 {t.Fatal(\"bad divide\")}}\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(source, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, source, "init", "-q")
	runGit(t, source, "add", ".")
	runGit(t, source, "-c", "user.name=Integration20 Test", "-c", "user.email=integration20@example.invalid", "commit", "-qm", "initial")
	w, err := repair17.Prepare(context.Background(), source, filepath.Join(dir, "worktrees"), runID)
	if err != nil {
		t.Fatal(err)
	}
	return w, source
}

func patchFor(t *testing.T, w repair17.Workspace) repair17.Patch {
	t.Helper()
	sha, err := repair17.Inspect(context.Background(), w.Path, "math.go")
	if err != nil {
		t.Fatal(err)
	}
	return repair17.Patch{Path: "math.go", ExpectedSHA256: sha, Old: "return a * b", New: "return a / b"}
}
