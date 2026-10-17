package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"runtime-app/internal/testdb"
)

func TestDay8ServerProcess(t *testing.T) {
	if os.Getenv("DAY8_SERVER_TEST_PROCESS") != "1" {
		return
	}
	main()
	os.Exit(0)
}

// This runs the actual main entry point, stops it, and starts a fresh process.
func TestDay8ServerPersistenceAfterRestart(t *testing.T) {
	db := testdb.Open(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	config := db.Pool.Config()
	dsn := config.ConnString()
	schema := config.ConnConfig.RuntimeParams["search_path"]
	if strings.Contains(dsn, "://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=" + schema
	}
	start := func() (string, func()) {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		listener.Close()
		command := exec.Command(binary, "-test.run=^TestDay8ServerProcess$")
		command.Env = append(os.Environ(), "DAY8_SERVER_TEST_PROCESS=1", "DATABASE_URL="+dsn, "HTTP_ADDR="+address)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = command.Process.Signal(syscall.SIGTERM)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("server exit: %v", err)
				}
			case <-time.After(10 * time.Second):
				_ = command.Process.Kill()
				<-done
				t.Error("server shutdown timed out")
			}
		}
		t.Cleanup(stop)
		url := "http://" + address
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			response, err := client.Get(url + "/metrics")
			if err == nil {
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return url, stop
				}
			}
			select {
			case err := <-done:
				stopped = true
				t.Fatalf("server exited before listening: %v", err)
			case <-ctx.Done():
				t.Fatal("server startup timed out")
			case <-ticker.C:
			}
		}
	}
	url, stop := start()
	resp, err := client.Post(url+"/tasks", "application/json", bytes.NewBufferString(
		`{"id":1,"name":"restart-test","tool_name":"uppercase","payload":"persistent result"}`))
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("POST status=%d", resp.StatusCode)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state string
		if err := db.Pool.QueryRow(ctx, "SELECT state FROM tasks WHERE id=1").Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "succeeded" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("task completion timed out")
		case <-ticker.C:
		}
	}
	stop()
	url, _ = start()
	resp, err = client.Get(url + "/tasks/1")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET after restart status=%d", resp.StatusCode)
	}
	var record struct {
		State  string `json:"state"`
		Stdout string `json:"stdout"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		t.Fatal(err)
	}
	if record.State != "succeeded" || record.Stdout != "PERSISTENT RESULT" {
		t.Fatalf("record after restart: %+v", record)
	}
}
