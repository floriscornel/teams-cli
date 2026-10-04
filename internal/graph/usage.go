package graph

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// UsageError is an error caused by the arguments the caller passed rather than
// by Graph itself. It carries exit code 2, the CLI's usage code (AGENTS.md
// "Exit codes"), which makes the layer's own validation consistent with the
// command layer's.
type UsageError struct {
	Msg string
}

// Error implements error.
func (e *UsageError) Error() string { return e.Msg }

// ExitCode is the documented usage exit code.
func (e *UsageError) ExitCode() int { return 2 }

// usageError builds a UsageError.
func usageError(msg string) error { return &UsageError{Msg: msg} }

// TimeoutError reports a request that exceeded the per-attempt deadline. It
// exists so a stall reads as a network problem with a next step, not as a CLI
// bug: before this, a stalled connection simply never returned.
type TimeoutError struct {
	// Err is the underlying transport error.
	Err error
	// After is the deadline that fired.
	After time.Duration
}

// Error implements error.
func (e *TimeoutError) Error() string {
	return fmt.Sprintf("the request did not complete within %s: %v", e.After, e.Err)
}

// Unwrap exposes the transport error, so errors.Is(err, context.DeadlineExceeded)
// and the exit-code mapping keep working.
func (e *TimeoutError) Unwrap() error { return e.Err }

// Hint is the suggested fix shown under the error.
func (e *TimeoutError) Hint() string {
	return "check the network or VPN for this machine; a throttled tenant answers 429, which the CLI already retries"
}

// isTimeout reports whether an error is a deadline or a network timeout.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
