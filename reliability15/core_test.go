package reliability15

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDay15FailureClassification(t *testing.T) {
	cases := []struct {
		err  error
		want FailureKind
	}{
		{nil, ""},
		{
			Retryable(errors.New("HTTP 503")),
			FailureTransient,
		},
		{
			errors.New("invalid tool args"),
			FailurePermanent,
		},
		{context.Canceled, FailureInterrupted},
		{
			context.DeadlineExceeded,
			FailurePermanent,
		},
		{ErrLeaseLost, FailureLeaseLost},
		{ErrConflict, FailureConflict},
		{
			errors.Join(
				ErrLeaseLost,
				errors.New("wrapped"),
			),
			FailureLeaseLost,
		},
	}

	for _, tc := range cases {
		got := ClassifyFailure(tc.err)
		if got != tc.want {
			t.Errorf(
				"err=%v: got %q want %q",
				tc.err, got, tc.want,
			)
		}
	}
}

func TestDay15RetryBudget(t *testing.T) {
	transient := Retryable(
		errors.New("service unavailable"),
	)

	cases := []struct {
		epoch int64
		max   int64
		want  Action
		kind  FailureKind
	}{
		{1, 3, ActionRetry, FailureTransient},
		{2, 3, ActionRetry, FailureTransient},
		{3, 3, ActionFail, FailureExhausted},
	}

	for _, tc := range cases {
		d, err := DecideFailure(
			transient,
			tc.epoch,
			tc.max,
		)
		if err != nil ||
			d.Action != tc.want ||
			d.Kind != tc.kind {
			t.Fatalf(
				"epoch=%d: decision=%+v err=%v",
				tc.epoch, d, err,
			)
		}
	}

	if CanClaim(3, 3) ||
		!CanClaim(2, 3) ||
		CanClaim(-1, 3) {
		t.Fatal("invalid claim budget guard")
	}

	got := Backoff(
		4,
		100*time.Millisecond,
		400*time.Millisecond,
	)
	if got != 400*time.Millisecond {
		t.Fatalf("backoff=%v", got)
	}
}

func TestDay15ShutdownIsNotPermanentFailure(
	t *testing.T,
) {
	d, err := DecideFailure(
		context.Canceled, 1, 2,
	)
	if err != nil || d.Action != ActionIgnore {
		t.Fatalf(
			"shutdown must leave lease to expire: %+v, %v",
			d, err,
		)
	}
}
