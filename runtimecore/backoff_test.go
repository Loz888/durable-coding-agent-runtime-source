package runtimecore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBackoffCancellation(t *testing.T) {
	// 1. 创建外层超时 Context
	// 最多允许测试等待 1 秒
	outerCtx, stop := context.WithTimeout(
		context.Background(),
		1*time.Second,
	)
	defer stop()

	// 2. 创建可主动取消的子 Context
	ctx, cancel := context.WithCancel(outerCtx)
	defer cancel()

	// 3. 创建带缓冲的结果 Channel
	resultCh := make(chan error, 1)

	// 4. 记录开始时间
	start := time.Now()

	// 5. 启动 Goroutine 等待 5 秒
	go func() {
		err := waitBackoff(
			ctx,
			5*time.Second,
		)

		resultCh <- err
	}()

	// 6. 另一个 Goroutine 在 100ms 后取消
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	// 7. 等待返回结果或外层超时
	select {
	case err := <-resultCh:
		elapsed := time.Since(start)

		// 必须返回 context.Canceled
		if !errors.Is(err, context.Canceled) {
			t.Fatalf(
				"expected context.Canceled, got %v",
				err,
			)
		}

		// 不允许等待完整的 5 秒
		if elapsed >= time.Second {
			t.Fatalf(
				"backoff did not stop early: elapsed=%v",
				elapsed,
			)
		}

		t.Logf(
			"Backoff cancelled successfully",
		)
		t.Logf("Elapsed: %v", elapsed)
		t.Logf("Error: %v", err)

	case <-outerCtx.Done():
		t.Fatal(
			"waitBackoff did not return within 1 second",
		)
	}
}
