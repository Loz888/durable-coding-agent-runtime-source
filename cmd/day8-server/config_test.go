package main

import (
	"os"
	"testing"
)

func TestDatabaseURLConfiguration(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("DATABASE_URL", "")
	if _, err := databaseURL(); err == nil {
		t.Fatal("missing configuration should fail")
	}
	if err := os.WriteFile(".env", []byte("DATABASE_URL='postgres://local-test'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := databaseURL()
	if err != nil || got != "postgres://local-test" {
		t.Fatalf("file config: %q %v", got, err)
	}
	t.Setenv("DATABASE_URL", "postgres://environment-test")
	got, err = databaseURL()
	if err != nil || got != "postgres://environment-test" {
		t.Fatalf("environment override: %q %v", got, err)
	}
}
