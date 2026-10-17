package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime-app/distributed"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type client struct {
	base string
	http *http.Client
}

func (c client) post(ctx context.Context, path string, req any, out any) (int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	r, err := http.NewRequestWithContext(ctx, "POST", c.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	r.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(r)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil
	}
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, fmt.Errorf("%s: %s", path, strings.TrimSpace(string(data)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

func main() {
	id := os.Getenv("WORKER_ID")
	if id == "" {
		id = "worker-a"
	}
	base := os.Getenv("COORDINATOR_URL")
	if base == "" {
		base = "http://127.0.0.1:8090"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c := client{base: base, http: &http.Client{Timeout: 2 * time.Second}}
	// Optional fault-injection delay: do not use it for production workloads.
	execDelay := time.Duration(0)
	if raw := os.Getenv("EXEC_DELAY_MS"); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms < 0 {
			log.Fatal("EXEC_DELAY_MS must be a nonnegative integer")
		}
		execDelay = time.Duration(ms) * time.Millisecond
	}
	// Coordinator may still be starting. Retry registration for a short window.
	registerCtx, registerCancel := context.WithTimeout(ctx, 5*time.Second)
	defer registerCancel()
	for {
		_, err := c.post(registerCtx, "/workers/register", map[string]string{"worker_id": id}, nil)
		if err == nil {
			break
		}
		if registerCtx.Err() != nil {
			log.Fatalf("registration failed: %v", err)
		}
		log.Printf("register retry: %v", err)
		select {
		case <-registerCtx.Done():
			log.Fatal(registerCtx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	log.Printf("%s registered", id)
	// Heartbeat is liveness only; it does not extend an individual job lease.
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := c.post(ctx, "/workers/heartbeat", map[string]string{"worker_id": id}, nil); err != nil && ctx.Err() == nil {
					log.Printf("heartbeat: %v", err)
				}
			}
		}
	}()
	poll := time.NewTicker(200 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			var job distributed.Job
			status, err := c.post(ctx, "/jobs/claim", map[string]string{"worker_id": id}, &job)
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("claim: %v", err)
				}
				continue
			}
			if status == http.StatusNoContent {
				continue
			}
			// A long executor may outlive its lease. This basic worker does not auto-renew.
			if execDelay > 0 {
				timer := time.NewTimer(execDelay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			// Only uppercase is supported; no untrusted shell commands.
			output := strings.ToUpper(job.Payload)
			req := map[string]any{"worker_id": id, "job_id": job.ID, "epoch": job.Epoch, "output": output}
			if _, err := c.post(ctx, "/jobs/complete", req, nil); err != nil {
				if ctx.Err() == nil {
					log.Printf("complete task %d: %v", job.ID, err)
				}
				continue
			}
			log.Printf("%s completed job=%d epoch=%d output=%q", id, job.ID, job.Epoch, output)
		}
	}
}
