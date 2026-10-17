package day15fault

import (
	"context"
	"errors"
	"fmt"
	"os"
)

var (
	ErrPermanent = errors.New("injected permanent failure")
	ErrTransient = errors.New("injected transient failure")
)

// BeforeRun 必须在成功 Claim 之后、执行 Agent 之前调用。
//
// DAY15_FAULT_RUN_ID：只影响指定 Run
// DAY15_FAULT_KIND：permanent / transient / hold
func BeforeRun(ctx context.Context, runID string) error {
	target := os.Getenv("DAY15_FAULT_RUN_ID")

	if target == "" || target != runID {
		return nil
	}

	switch os.Getenv("DAY15_FAULT_KIND") {

	case "permanent":
		return fmt.Errorf(
			"%w: invalid tool arguments",
			ErrPermanent,
		)

	case "transient":
		return fmt.Errorf(
			"%w: temporary upstream failure",
			ErrTransient,
		)

	case "hold":
		// 模拟执行过程中持续阻塞。
		// 真实实验将通过 SIGKILL 终止进程。
		<-ctx.Done()
		return ctx.Err()

	case "", "off":
		return nil

	default:
		return fmt.Errorf(
			"%w: unknown fault kind",
			ErrPermanent,
		)
	}
}
