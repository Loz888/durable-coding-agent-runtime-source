package observability

import (
	"log/slog"
	"time"

	"runtime-app/runtimecore"
)

func LogResult(
	logger *slog.Logger,
	result runtimecore.Result,
) {
	if logger == nil {
		return
	}

	var totalDuration time.Duration

	for _, attempt := range result.Attempts {
		totalDuration += attempt.Duration
	}

	attemptCount := len(result.Attempts)

	retryCount := 0

	if attemptCount > 1 {
		retryCount = attemptCount - 1
	}

	attrs := []any{
		"task_id", result.TaskID,
		"task_name", result.TaskName,
		"worker_id", result.WorkerID,
		"state", result.State,
		"error_kind", result.ErrorKind,
		"exit_code", result.ExitCode,
		"attempts", attemptCount,
		"retries", retryCount,
		"attempt_duration_ms",
		float64(totalDuration.Microseconds()) / 1000,
	}

	if result.Err != nil {
		attrs = append(
			attrs,
			"error",
			result.Err.Error(),
		)
	}

	switch result.State {
	case runtimecore.StateSucceeded:
		logger.Info("task_completed", attrs...)

	case runtimecore.StateCancelled:
		logger.Warn("task_completed", attrs...)

	default:
		logger.Error("task_completed", attrs...)
	}
}
