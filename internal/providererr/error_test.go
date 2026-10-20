package providererr

import (
	"context"
	"testing"
	"time"
)

func TestRetryAfterAndUnknownChargeContract(t *testing.T) {
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	if ParseRetryAfter("3", now) != 3*time.Second || ParseRetryAfter("-1", now) != 0 || ParseRetryAfter("nonsense", now) != 0 {
		t.Fatal("invalid retry delay")
	}
	if !HTTP(503, 2*time.Hour).Temporary() || HTTP(401, 0).Temporary() || !HTTP(429, 0).MayHaveCharged() {
		t.Fatal("HTTP policy diverged")
	}
	if Transport(context.Canceled).Temporary() || !Transport(context.DeadlineExceeded).MayHaveCharged() {
		t.Fatal("lost uncertainty")
	}
}
