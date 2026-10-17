package reliability15

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type FailureKind string

const (
	FailureTransient   FailureKind = "transient"
	FailurePermanent   FailureKind = "permanent"
	FailureInterrupted FailureKind = "interrupted"
	FailureLeaseLost   FailureKind = "lease_lost"
	FailureConflict    FailureKind = "conflict"
	FailureExhausted   FailureKind = "retry_exhausted"
)

var (
	ErrLeaseLost = errors.New("lease lost")
	ErrConflict  = errors.New("checkpoint revision conflict")
	ErrNoAction  = errors.New("failure needs no state mutation")
)

// Only wrap an error when replay has been assessed as safe.
type TransientError struct {
	Err error
}

func (e *TransientError) Error() string {
	return fmt.Sprintf("transient: %v", e.Err)
}

func (e *TransientError) Unwrap() error {
	return e.Err
}

func Retryable(err error) error {
	if err == nil {
		return nil
	}
	return &TransientError{Err: err}
}

func ClassifyFailure(err error) FailureKind {
	var terminal interface{ FailureKind() FailureKind }
	if errors.As(err, &terminal) {
		return terminal.FailureKind()
	}
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrLeaseLost):
		return FailureLeaseLost
	case errors.Is(err, ErrConflict):
		return FailureConflict
	case errors.Is(err, context.Canceled):
		return FailureInterrupted
	case errors.Is(err, context.DeadlineExceeded):
		return FailurePermanent
	default:
		var temporary *TransientError
		if errors.As(err, &temporary) {
			return FailureTransient
		}
		return FailurePermanent
	}
}

type Action string

const (
	ActionIgnore Action = "ignore"
	ActionRetry  Action = "retry"
	ActionFail   Action = "fail"
)

type Decision struct {
	Action Action
	Kind   FailureKind
}

// Assumption:
// epoch starts at 0 and increases once per successful Claim.
func DecideFailure(
	err error,
	epoch int64,
	maxAttempts int64,
) (Decision, error) {
	if epoch < 1 || maxAttempts < 1 {
		return Decision{}, fmt.Errorf(
			"invalid budget: epoch=%d max=%d",
			epoch, maxAttempts,
		)
	}

	kind := ClassifyFailure(err)

	switch kind {
	case "",
		FailureInterrupted,
		FailureLeaseLost,
		FailureConflict:
		return Decision{
			Action: ActionIgnore,
			Kind:   kind,
		}, nil

	case FailureTransient:
		if epoch < maxAttempts {
			return Decision{
				Action: ActionRetry,
				Kind:   kind,
			}, nil
		}
		return Decision{
			Action: ActionFail,
			Kind:   FailureExhausted,
		}, nil

	default:
		return Decision{
			Action: ActionFail,
			Kind:   kind,
		}, nil
	}
}

func CanClaim(
	epoch int64,
	maxAttempts int64,
) bool {
	return maxAttempts >= 1 &&
		epoch >= 0 &&
		epoch < maxAttempts
}

// Deterministic capped exponential backoff.
func Backoff(
	attempt int64,
	base time.Duration,
	capDelay time.Duration,
) time.Duration {
	if attempt < 1 ||
		base <= 0 ||
		capDelay < base {
		return 0
	}

	d := base
	for i := int64(1); i < attempt; i++ {
		if d > capDelay/2 {
			return capDelay
		}
		d *= 2
	}

	if d > capDelay {
		return capDelay
	}
	return d
}

// Caller must not pass unredacted API responses or secrets.
func SafeReason(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unspecified failure"
	}

	r := []rune(s)
	if len(r) > 500 {
		r = r[:500]
	}
	return string(r)
}
