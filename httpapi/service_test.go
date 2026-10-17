package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"runtime-app/executor"
	"runtime-app/runtimecore"
)

func TestSubmitAndQueryTask(t *testing.T) {

	registry := executor.NewRegistry()

	err := registry.Register(
		"uppercase",
		executor.UppercaseExecutor{},
	)

	if err != nil {
		t.Fatal(err)
	}

	r := runtimecore.NewRuntime(
		registry,
		2,
		4,
	)

	ctx, cancel := context.WithCancel(
		context.Background(),
	)

	defer cancel()

	r.Start(ctx)

	service := NewService(
		r,
		nil,
	)

	service.StartCollector()

	server := httptest.NewServer(
		service.Handler(),
	)

	defer server.Close()

	body := map[string]any{
		"id":        1001,
		"name":      "test-uppercase",
		"tool_name": "uppercase",
		"payload":   "hello agent",
	}

	data, _ := json.Marshal(body)

	resp, err := http.Post(
		server.URL+"/tasks",
		"application/json",
		bytes.NewReader(data),
	)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf(
			"status=%d",
			resp.StatusCode,
		)
	}

	time.Sleep(
		50 * time.Millisecond,
	)

	resp, err = http.Get(
		server.URL + "/tasks/1001",
	)

	if err != nil {
		t.Fatal(err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf(
			"status=%d",
			resp.StatusCode,
		)
	}

	var result TaskRecord

	err = json.NewDecoder(resp.Body).
		Decode(&result)

	if err != nil {
		t.Fatal(err)
	}

	if result.State != "succeeded" {
		t.Fatalf(
			"state=%s",
			result.State,
		)
	}

	if result.Stdout != "HELLO AGENT" {
		t.Fatalf(
			"stdout=%s",
			result.Stdout,
		)
	}
}
