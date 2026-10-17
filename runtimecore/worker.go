package runtimecore

import (
	"context"
	"fmt"

	"runtime-app/executor"
)

func (r *Runtime) worker(
	ctx context.Context,
	workerID int,
) {
	defer r.wg.Done()

	// 继续处理已接受的 Task，直到输入 Channel 关闭。
	for t := range r.tasks {

		// Step 1：Pending -> Running
		if err := r.states.Transition(
			t.ID,
			StateRunning,
		); err != nil {
			r.results <- Result{
				TaskID:    t.ID,
				TaskName:  t.Name,
				WorkerID:  workerID,
				State:     StateFailed,
				ErrorKind: KindInternal,
				ExitCode:  -1,
				Err: fmt.Errorf(
					"start task %d: %w",
					t.ID,
					err,
				),
			}
			continue
		}

		// Step 2：准备执行结果。
		output := executor.Output{
			ExitCode: -1,
		}

		var err error
		var attempts []Attempt

		// Step 3：区分任务取消与正常执行。
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		} else {
			output, err, attempts = r.executeWithRetry(
				ctx,
				t,
			)
		}

		// Step 4：只根据最终错误决定 Task 终态。
		kind := ClassifyError(err)
		state := TerminalState(kind)

		// Step 5：更新 Task State。
		if stateErr := r.states.Transition(
			t.ID,
			state,
		); stateErr != nil {
			err = fmt.Errorf(
				"finish task %d: %w",
				t.ID,
				stateErr,
			)
			kind = KindInternal
			state = StateFailed
		}

		// Step 6：发送一个终态 Result。
		r.results <- Result{
			TaskID:   t.ID,
			TaskName: t.Name,
			WorkerID: workerID,

			State:     state,
			ErrorKind: kind,

			Stdout:   output.Stdout,
			Stderr:   output.Stderr,
			ExitCode: output.ExitCode,

			Attempts: attempts,
			Err:      err,
		}
	}
}
