package testdb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDatabaseURLFromModuleConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/testfixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.test"), []byte(
		"# test-only settings\nDAY12_TEST_DATABASE_URL='postgres://file-test'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "nested", "package")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)
	t.Setenv("DAY12_TEST_DATABASE_URL", "")
	t.Setenv("TEST_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "postgres://production-must-not-be-used")
	if got := DatabaseURL(t, "DAY12_TEST_DATABASE_URL"); got != "postgres://file-test" {
		t.Fatalf("nested module config: %q", got)
	}
	if got := DatabaseURL(t, "TEST_DATABASE_URL"); got != "" {
		t.Fatalf("missing test config must not fall back to production: %q", got)
	}
	t.Setenv("DAY12_TEST_DATABASE_URL", "postgres://environment-test")
	if got := DatabaseURL(t, "DAY12_TEST_DATABASE_URL"); got != "postgres://environment-test" {
		t.Fatalf("environment override: %q", got)
	}
}
