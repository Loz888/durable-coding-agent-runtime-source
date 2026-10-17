package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"runtime-app/executor"
	"runtime-app/internal/testdb"
	"runtime-app/persistence"
	"runtime-app/runtimecore"
	"testing"
	"time"
)

func TestPersistenceIntegration(t *testing.T) {
	db := testdb.Open(t)
	store := persistence.NewPostgresTaskStore(db)
	registry := executor.NewRegistry()
	if err := registry.Register("uppercase", executor.UppercaseExecutor{}); err != nil {
		t.Fatal(err)
	}
	runtime := runtimecore.NewRuntime(registry, 2, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runtime.Start(ctx)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewServiceWithStore(runtime, store, logger)
	service.StartCollector()
	t.Cleanup(func() {
		cancel()
		runtime.Close()
		select {
		case <-service.Done():
		case <-time.After(5 * time.Second):
			t.Error("collector did not stop")
		}
	})
	server := httptest.NewServer(service.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post(server.URL+"/tasks", "application/json",
		bytes.NewBufferString(`{"id":1001,"name":"persistence-integration","tool_name":"uppercase","payload":"hello postgres"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status=%d", resp.StatusCode)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state string
		err = db.Pool.QueryRow(ctx, "SELECT state FROM tasks WHERE id=$1", int64(1001)).Scan(&state)
		if err != nil {
			t.Fatal(err)
		}
		if state == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("task was not persisted as succeeded")
		case <-ticker.C:
		}
	}
	record, err := store.Get(ctx, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if record.Stdout != "HELLO POSTGRES" || record.Attempts != 1 || record.Retries != 0 {
		t.Fatalf("database record=%+v", record)
	}
	// A fresh service has no memory records or Runtime state: GET must read PostgreSQL.
	reader := NewServiceWithStore(runtimecore.NewRuntime(registry, 1, 1), persistence.NewPostgresTaskStore(db), logger)
	readServer := httptest.NewServer(reader.Handler())
	defer readServer.Close()
	resp, err = client.Get(readServer.URL + "/tasks/1001")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET status=%d", resp.StatusCode)
	}
	var got TaskRecord
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.TaskID != 1001 || got.Name != "persistence-integration" || got.ToolName != "uppercase" ||
		got.State != "succeeded" || got.Stdout != "HELLO POSTGRES" || got.Attempts != 1 {
		t.Fatalf("GET record=%+v", got)
	}
}
