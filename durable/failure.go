package durable

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/jackc/pgx/v5/pgconn"
	"runtime-app/reliability15"
)

type terminalRunError struct {
	kind  reliability15.FailureKind
	cause error
}

func (e *terminalRunError) Error() string                          { return string(e.kind) }
func (e *terminalRunError) Unwrap() error                          { return e.cause }
func (e *terminalRunError) FailureKind() reliability15.FailureKind { return e.kind }

// Translate typed failures, never API response text, into the existing policy.
func schedulerFailure(err error) error {
	switch {
	case errors.Is(err, ErrLeaseLost):
		return reliability15.ErrLeaseLost
	case errors.Is(err, ErrConflict):
		return reliability15.ErrConflict
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return err
	}
	var coded interface{ FailureCode() string }
	if errors.As(err, &coded) {
		if coded.FailureCode() == "lease_lost" {
			return reliability15.ErrLeaseLost
		}
		return &terminalRunError{kind: reliability15.FailureKind(coded.FailureCode()), cause: err}
	}
	var transient interface{ Temporary() bool }
	if errors.As(err, &transient) && transient.Temporary() {
		return reliability15.Retryable(err)
	}
	var network net.Error
	if errors.As(err, &network) {
		return reliability15.Retryable(err)
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (len(pg.Code) >= 2 && (pg.Code[:2] == "08" || pg.Code[:2] == "53") ||
		pg.Code == "57P01" || pg.Code == "40001" || pg.Code == "40P01") {
		return reliability15.Retryable(err)
	}
	if pgconn.SafeToRetry(err) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return reliability15.Retryable(err)
	}
	return err
}
