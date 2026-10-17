package distributed

import (
	"errors"
	"testing"
	"time"
)

func TestExpiredLeaseRejectsRenewAfterSuccessfulHeartbeat(t *testing.T) {
	const (
		leaseTTL     = 5 * time.Second
		heartbeatTTL = 30 * time.Second
		workerID     = "worker-A"
	)

	// 注入可控时钟，不依赖 time.Sleep。
	fakeNow := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	c, err := NewCoordinator(leaseTTL, heartbeatTTL)
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	c.now = func() time.Time { return fakeNow }

	// Step 1: 注册 Worker A
	if err := c.Register(workerID); err != nil {
		t.Fatalf("register worker: %v", err)
	}

	// Step 2: 提交 Job 1
	if _, err := c.Enqueue(1, "uppercase", "hello agent"); err != nil {
		t.Fatalf("submit job: %v", err)
	}

	// Step 3: Worker A Claim Job 1
	claimed, ok, err := c.Claim(workerID)

	if err != nil {
		t.Fatalf("claim job: %v", err)
	}

	if !ok {
		t.Fatal("expected Job 1 to be claimed")
	}

	if claimed.ID != 1 ||
		claimed.WorkerID != workerID ||
		claimed.Epoch != 1 {
		t.Fatalf(
			"unexpected claim: id=%d worker=%q epoch=%d",
			claimed.ID,
			claimed.WorkerID,
			claimed.Epoch,
		)
	}

	// Step 4: 推进时钟，使 Job Lease 过期
	fakeNow = claimed.LeaseUntil.Add(time.Nanosecond)

	// Step 5: Worker A Heartbeat 仍然成功
	if err := c.Heartbeat(workerID); err != nil {
		t.Fatalf(
			"heartbeat should succeed: %v",
			err,
		)
	}

	// Step 6: 使用旧 Epoch=1 尝试 Renew
	_, err = c.Renew(
		claimed.ID,
		workerID,
		claimed.Epoch,
	)

	// Step 7: 必须返回 ErrLeaseLost
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf(
			"want ErrLeaseLost, got %v",
			err,
		)
	}
}
