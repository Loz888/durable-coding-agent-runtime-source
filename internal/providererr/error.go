// Package providererr defines one sanitized failure contract for both model adapters.
package providererr

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

type Category string

const (
	Auth             Category = "auth"
	RateLimit        Category = "rate_limit"
	Upstream         Category = "upstream"
	Timeout          Category = "timeout"
	TransportFailure Category = "transport"
	InvalidOutput    Category = "invalid_output"
	Truncated        Category = "truncated"
	Cancelled        Category = "cancelled"
	InvalidRequest   Category = "invalid_request"
)

type Failure interface {
	error
	Category() Category
	Temporary() bool
	MayHaveCharged() bool
	RetryAfter() time.Duration
}

type Error struct {
	Kind       Category
	StatusCode int
	Delay      time.Duration
	Retry      bool
	Unknown    bool
	cause      error
}

func (e *Error) Error() string {
	return fmt.Sprintf("provider %s (HTTP %d; response hidden)", e.Kind, e.StatusCode)
}
func (e *Error) Unwrap() error             { return e.cause }
func (e *Error) Category() Category        { return e.Kind }
func (e *Error) Temporary() bool           { return e.Retry }
func (e *Error) MayHaveCharged() bool      { return e.Unknown }
func (e *Error) RetryAfter() time.Duration { return e.Delay }

func HTTP(status int, delay time.Duration) *Error {
	kind, retry := InvalidRequest, false
	switch {
	case status == 401 || status == 403:
		kind = Auth
	case status == 429:
		kind, retry = RateLimit, true
	case status == 408:
		kind, retry = Timeout, true
	case status >= 500 && status <= 599:
		kind, retry = Upstream, true
	}
	return &Error{Kind: kind, StatusCode: status, Delay: delay, Retry: retry, Unknown: true}
}
func Invalid(kind Category) *Error { return &Error{Kind: kind, Unknown: true} }
func Transport(err error) *Error {
	kind, retry := TransportFailure, true
	var network net.Error
	if errors.Is(err, context.Canceled) {
		kind, retry = Cancelled, false
	} else if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) && network.Timeout() {
		kind = Timeout
	}
	return &Error{Kind: kind, Retry: retry, Unknown: true, cause: err}
}
func ParseRetryAfter(value string, now time.Time) time.Duration {
	if n, err := strconv.Atoi(value); err == nil {
		if n > 0 && n <= 86400 {
			return time.Duration(n) * time.Second
		}
		return 0
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := date.Sub(now)
		if delay > 0 && delay <= 24*time.Hour {
			return delay
		}
	}
	return 0
}

// ResponseRead distinguishes a transport timeout from a complete but malformed JSON body.
func ResponseRead(err error) *Error {
	var network net.Error
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &network) {
		return Transport(err)
	}
	return Invalid(InvalidOutput)
}
