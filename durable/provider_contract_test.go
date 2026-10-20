package durable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime-app/internal/providererr"
	"runtime-app/reliability15"
	"runtime-app/repair18"
	"runtime-app/repodiag"
	"strings"
	"testing"
	"time"
)

func TestReviewBothAdaptersHaveTheSameFailureAndSchedulerContract(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		kind   providererr.Category
		retry  bool
	}{
		{"auth", 401, "private upstream details", providererr.Auth, false},
		{"rate", 429, "private upstream details", providererr.RateLimit, true},
		{"upstream", 503, "private upstream details", providererr.Upstream, true},
		{"invalid", 200, "not JSON", providererr.InvalidOutput, false},
		{"truncated", 200, `{"choices":[{"finish_reason":"length","message":{"content":"{}"}}]}`, providererr.Truncated, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Retry-After", "3")
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer server.Close()
			_, diagnosis := (repodiag.DeepSeekModel{APIKey: "synthetic", Endpoint: server.URL}).Next(context.Background(), []json.RawMessage{json.RawMessage(`{"role":"user","content":"fixture"}`)})
			_, coding := (repair18.DeepSeek{APIKey: "synthetic", Endpoint: server.URL}).Suggest(context.Background(), repair18.Request{Source: "a*b"})
			for _, err := range []error{diagnosis, coding} {
				var failure providererr.Failure
				if !errors.As(err, &failure) || failure.Category() != c.kind || failure.Temporary() != c.retry || !failure.MayHaveCharged() {
					t.Fatalf("contract: %v", err)
				}
				if strings.Contains(err.Error(), "private upstream details") {
					t.Fatal("body exposed")
				}
				if c.status != 200 && failure.RetryAfter() != 3*time.Second {
					t.Fatal("retry-after lost")
				}
				want := reliability15.FailurePermanent
				if c.retry {
					want = reliability15.FailureTransient
				}
				if got := reliability15.ClassifyFailure(schedulerFailure(err)); got != want {
					t.Fatalf("scheduler=%s want=%s", got, want)
				}
			}
		})
	}
}
func TestReviewBothAdapterTimeoutsRetainUncertainty(t *testing.T) {
	for _, headersSent := range []bool{false, true} {
		t.Run(fmt.Sprint(headersSent), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if headersSent {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
				}
				time.Sleep(50 * time.Millisecond)
			}))
			defer server.Close()
			client := &http.Client{Timeout: 5 * time.Millisecond}
			_, a := (repodiag.DeepSeekModel{APIKey: "synthetic", Endpoint: server.URL, Client: client}).Next(context.Background(), []json.RawMessage{json.RawMessage(`{"role":"user","content":"fixture"}`)})
			_, b := (repair18.DeepSeek{APIKey: "synthetic", Endpoint: server.URL, Client: client}).Suggest(context.Background(), repair18.Request{})
			for _, err := range []error{a, b} {
				var failure providererr.Failure
				if !errors.As(err, &failure) || failure.Category() != providererr.Timeout || !failure.MayHaveCharged() {
					t.Fatalf("uncertainty lost: %v", err)
				}
			}
			if reliability15.ClassifyFailure(schedulerFailure(a)) != reliability15.ClassifyFailure(schedulerFailure(b)) {
				t.Fatal("timeout policy differs")
			}
		})
	}
}
