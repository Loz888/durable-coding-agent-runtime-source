package runtimecore

import (
	"context"
	"fmt"
	"time"
)

type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 1 {
		return fmt.Errorf(
			"MaxAttempts must be >= 1",
		)
	}

	if p.BaseDelay <= 0 {
		return fmt.Errorf(
			"BaseDelay must be positive",
		)
	}

	if p.MaxDelay < p.BaseDelay {
		return fmt.Errorf(
			"MaxDelay must be >= BaseDelay",
		)
	}

	return nil
}

func (p RetryPolicy) delayAfter(
	failedAttempt int,
) time.Duration {
	if failedAttempt < 1 {
		return 0
	}

	delay := p.BaseDelay

	for i := 1; i < failedAttempt; i++ {
		if delay > p.MaxDelay/2 {
			return p.MaxDelay
		}

		delay *= 2
	}

	if delay > p.MaxDelay {
		return p.MaxDelay
	}

	return delay
}
func waitBackoff(
	ctx context.Context,
	delay time.Duration,
) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()

	case <-timer.C:
		return ctx.Err()
	}
}

type Attempt struct {
	Number    int
	Duration  time.Duration
	ErrorKind ErrorKind
	Err       error
}
