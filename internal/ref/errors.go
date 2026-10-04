package ref

import (
	"errors"
	"fmt"
)

// Exit codes from AGENTS.md, mirrored here so a reference error carries the
// code the CLI contract assigns it without this package importing the output
// layer.
const (
	codeError    = 1
	codeUsage    = 2
	codeNotFound = 4
)

// Error is a reference error: a message, the exit code it maps to, and the
// one-line fix to print under it.
type Error struct {
	Msg  string
	Code int
	Fix  string
}

// Error implements error.
func (e *Error) Error() string { return e.Msg }

// ExitCode is the process exit code for this error.
func (e *Error) ExitCode() int { return e.Code }

// Hint is the suggested fix, printed by the CLI under the error.
func (e *Error) Hint() string { return e.Fix }

// usagef builds an exit-2 error: the reference cannot be interpreted at all.
func usagef(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...), Code: codeUsage}
}

// notFoundf builds an exit-4 error: the reference is well-formed but nothing
// matches it.
func notFoundf(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...), Code: codeNotFound}
}

// errorf builds an exit-1 error.
func errorf(format string, args ...any) error {
	return &Error{Msg: fmt.Sprintf(format, args...), Code: codeError}
}

// withFix attaches a suggested fix to an error, preserving its exit code.
func withFix(err error, format string, args ...any) error {
	if err == nil {
		return nil
	}
	var refErr *Error
	if errors.As(err, &refErr) {
		clone := *refErr
		if clone.Fix == "" {
			clone.Fix = fmt.Sprintf(format, args...)
		}
		return &clone
	}
	return &Error{Msg: err.Error(), Code: codeError, Fix: fmt.Sprintf(format, args...)}
}
