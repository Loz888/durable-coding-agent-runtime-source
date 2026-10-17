package runtimecore

import (
	"context"
	"errors"
	"time"

	"runtime-app/executor"
	"runtime-app/task"
)

// ErrTransient 表示可考虑重试的暂时性故障。
var ErrTransient = executor.ErrTemporary

func executeWithRetry(
	ctx context.Context,
	t task.Task,
	e executor.Executor,
	policy RetryPolicy,
) (executor.Output, error, []Attempt) {

	output := executor.Output{
		ExitCode: -1,
	}

	// 检查重试策略是否合法。
	if err := policy.Validate(); err != nil {
		return output, err, nil
	}

	attempts := make(
		[]Attempt,
		0,
		policy.MaxAttempts,
	)

	for attemptID := 1; attemptID <= policy.MaxAttempts; attemptID++ {

		// 1. 每次执行前检查 Context。
		if err := ctx.Err(); err != nil {
			return output, err, attempts
		}

		// 2. 记录本次执行时间。
		start := time.Now()

		var err error
		output, err = e.Execute(ctx, t)

		duration := time.Since(start)

		// 3. 分类错误。
		kind := ClassifyError(err)

		// 4. 记录本次 Attempt。
		attempts = append(attempts, Attempt{
			Number:    attemptID,
			Duration:  duration,
			ErrorKind: kind,
			Err:       err,
		})

		// 5. 成功则直接返回。
		if err == nil {
			return output, nil, attempts
		}

		// 6. 父 Context 已取消则不再重试。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return output, ctxErr, attempts
		}

		// 7. 判断是否允许重试。
		if !t.RetrySafe ||
			!errors.Is(err, ErrTransient) ||
			attemptID == policy.MaxAttempts {

			return output, err, attempts
		}

		// 8. 计算指数退避。
		delay := policy.delayAfter(attemptID)

		// 9. 可取消的 Backoff 等待。
		if err := waitBackoff(ctx, delay); err != nil {
			return output, err, attempts
		}
	}

	// 正常情况下，循环内已经返回。
	return output, nil, attempts
}

// executeWithRetry 是 Runtime 的重试执行入口。
func (r *Runtime) executeWithRetry(
	ctx context.Context,
	t task.Task,
) (executor.Output, error, []Attempt) {
	return executeWithRetry(
		ctx,
		t,
		r.executor,
		r.retryPolicy,
	)
}
