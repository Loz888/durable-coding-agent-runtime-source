package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryCommandRequiresExplicitScopeAndNeverProductionFallback(t *testing.T) {
	t.Setenv("DAY12_TEST_DATABASE_URL", "")
	t.Setenv("TEST_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "postgres://private-password@production/agent_runtime")
	root := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][]string{
		{},
		{"-root", root, "-reference-schemas", "day19", "-database-env", "DATABASE_URL"},
		{"-root", root, "-reference-schemas", "day19", "-database-env", "DAY12_TEST_DATABASE_URL"},
		{"-root", root, "-reference-schemas", "day19", "-database-env", "TEST_DATABASE_URL", "-apply"},
		{"-root", root, "-reference-schemas", "day19; DELETE FROM audit", "-database-env", "TEST_DATABASE_URL"},
	} {
		var output bytes.Buffer
		err := run(args, &output)
		if err == nil || output.Len() != 0 || strings.Contains(err.Error(), "private-password") || strings.Contains(err.Error(), root) {
			t.Fatal("unsafe command failure")
		}
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("command created missing root")
	}
}

func TestInventoryDedicatedDatabaseNameBoundary(t *testing.T) {
	for _, name := range []string{"agent_runtime_day14_test", "agent_runtime_test", "test", "test_runtime"} {
		if !testDatabaseName.MatchString(name) {
			t.Fatalf("test database refused: %s", name)
		}
	}
	for _, name := range []string{"agent_runtime", "contest", "testing_production", "mytest", "agent_runtime_testprod"} {
		if testDatabaseName.MatchString(name) {
			t.Fatalf("non-test database accepted: %s", name)
		}
	}
}
