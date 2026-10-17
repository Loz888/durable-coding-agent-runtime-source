package runtimecore

import (
	"context"
	"errors"
	"os/exec"

	"runtime-app/executor"
)

type ErrorKind string

const (
	KindNone              ErrorKind = "none"
	KindToolNotFound      ErrorKind = "tool_not_found"
	KindCommandNotAllowed ErrorKind = "command_not_allowed"
	KindNonZeroExit       ErrorKind = "non_zero_exit"
	KindTimeout           ErrorKind = "timeout"
	KindCancelled         ErrorKind = "cancelled"
	KindInternal          ErrorKind = "internal"
	KindTransient         ErrorKind = "transient"
)

func ClassifyError(err error) ErrorKind {
	if err == nil {
		return KindNone
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return KindTimeout
	}

	if errors.Is(err, context.Canceled) {
		return KindCancelled
	}

	if errors.Is(err, executor.ErrToolNotFound) {
		return KindToolNotFound
	}

	if errors.Is(err, executor.ErrCommandNotAllowed) {
		return KindCommandNotAllowed
	}

	if errors.Is(err, ErrTransient) {
		return KindTransient
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return KindNonZeroExit
	}

	return KindInternal
}

func TerminalState(kind ErrorKind) State {
	switch kind {
	case KindNone:
		return StateSucceeded

	case KindTimeout, KindCancelled:
		return StateCancelled

	default:
		return StateFailed
	}
}
