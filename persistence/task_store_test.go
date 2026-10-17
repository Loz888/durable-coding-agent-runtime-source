package persistence_test

import (
	"context"
	"runtime-app/internal/testdb"
	"runtime-app/persistence"
	"testing"
	"time"
)

func TestPostgresConnection(t *testing.T) {
	db := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.Pool.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	var one int
	if err := db.Pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1: value=%d error=%v", one, err)
	}
	t.Log("connect success")
}
func TestPostgresTaskStoreCreate(t *testing.T) {
	db := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := persistence.NewPostgresTaskStore(db)
	want := persistence.TaskRecord{ID: 101, Name: "create-test", ToolName: "uppercase", State: "pending",
		ErrorKind: "none", Error: "sample error", Stdout: "out", Stderr: "err", ExitCode: 7, Attempts: 3, Retries: 2}
	if err := store.Create(ctx, want); err != nil {
		t.Fatal(err)
	}
	var got persistence.TaskRecord
	err := db.Pool.QueryRow(ctx, `SELECT id,name,tool_name,state,error_kind,error,stdout,stderr,exit_code,attempts,retries FROM tasks WHERE id=$1`, want.ID).
		Scan(&got.ID, &got.Name, &got.ToolName, &got.State, &got.ErrorKind, &got.Error, &got.Stdout, &got.Stderr, &got.ExitCode, &got.Attempts, &got.Retries)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("SELECT got %+v, want %+v", got, want)
	}
	fetched, err := store.Get(ctx, want.ID)
	if err != nil || fetched != want {
		t.Fatalf("Get got %+v, error=%v", fetched, err)
	}
}
func TestPostgresTaskStoreUpdate(t *testing.T) {
	db := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store := persistence.NewPostgresTaskStore(db)
	record := persistence.TaskRecord{ID: 102, Name: "update-test", ToolName: "uppercase", State: "pending"}
	if err := store.Create(ctx, record); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"pending", "running", "succeeded"} {
		record.State = state
		if state != "pending" {
			if state == "succeeded" {
				record.Stdout = "HELLO"
				record.Attempts = 2
				record.Retries = 1
			}
			if err := store.Update(ctx, record); err != nil {
				t.Fatal(err)
			}
		}
		var got string
		if err := db.Pool.QueryRow(ctx, "SELECT state FROM tasks WHERE id=$1", record.ID).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != state {
			t.Fatalf("state=%s, want %s", got, state)
		}
	}
	got, err := store.Get(ctx, record.ID)
	if err != nil || got != record {
		t.Fatalf("final record=%+v, error=%v", got, err)
	}
}
