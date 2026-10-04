// Package output centralizes everything the CLI writes to a terminal and the
// process exit codes it uses. It is the only package (besides cmd/) allowed to
// call fmt.Print* — the `forbidigo` linter enforces that, so output formatting
// and exit codes stay in one place.
//
// Exit codes (PLAN.md "Output and UX conventions"):
//
//	0 ok · 1 error · 2 usage · 3 auth required · 4 not found · 5 throttled
package output

import (
	"errors"
	"fmt"
)

// Exit codes. They are part of the CLI contract and are documented in
// docs/commands and the README.
const (
	CodeOK        = 0
	CodeError     = 1
	CodeUsage     = 2
	CodeAuth      = 3
	CodeNotFound  = 4
	CodeThrottled = 5
)

// Error is an error that carries the process exit code and, optionally, the
// one-line fix we want to show the user.
type Error struct {
	Code int
	Msg  string
	Hint string
	Err  error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Err != nil && e.Msg == "" {
		return e.Err.Error()
	}
	if e.Err != nil {
		return e.Msg + ": " + e.Err.Error()
	}
	return e.Msg
}

// Unwrap exposes the wrapped cause.
func (e *Error) Unwrap() error { return e.Err }

func newError(code int, format string, args ...any) *Error {
	return &Error{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// Errorf builds a generic (exit 1) error.
func Errorf(format string, args ...any) error { return newError(CodeError, format, args...) }

// Usagef builds a usage error (exit 2).
func Usagef(format string, args ...any) error { return newError(CodeUsage, format, args...) }

// Authf builds an authentication-required error (exit 3).
func Authf(format string, args ...any) error { return newError(CodeAuth, format, args...) }

// NotFoundf builds a not-found error (exit 4).
func NotFoundf(format string, args ...any) error { return newError(CodeNotFound, format, args...) }

// Throttledf builds a throttled error (exit 5).
func Throttledf(format string, args ...any) error { return newError(CodeThrottled, format, args...) }

// ExitCoder is implemented by errors that know their own exit code, such as the
// Graph APIError (a 404 is exit 4, a 429 is exit 5, and so on).
type ExitCoder interface {
	ExitCode() int
}

// Hinter is implemented by errors that carry a suggested fix.
type Hinter interface {
	Hint() string
}

// WithHint attaches a copy of err that carries a suggested fix. The hint is
// printed under the error, never mixed into the message, so scripts that parse
// the message stay stable. The exit code of the original error is preserved.
func WithHint(err error, hint string) error {
	if err == nil {
		return nil
	}
	var coded *Error
	if errors.As(err, &coded) {
		clone := *coded
		if clone.Hint == "" {
			clone.Hint = hint
		}
		return &clone
	}
	return &Error{Code: CodeOf(err), Err: err, Hint: hint}
}

// WithCode forces an exit code onto an existing error, preserving its text.
func WithCode(err error, code int) error {
	if err == nil {
		return nil
	}
	var coded *Error
	if errors.As(err, &coded) {
		clone := *coded
		clone.Code = code
		return &clone
	}
	return &Error{Code: code, Err: err}
}

// CodeOf returns the exit code for err: 0 for nil, the code carried by an
// *Error, the value from ExitCode for anything implementing ExitCoder, and 1 for
// anything else.
func CodeOf(err error) int {
	if err == nil {
		return CodeOK
	}
	var coded *Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	var coder ExitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return CodeError
}

// HintOf returns the hint attached to err, if any: our own Error first, then any
// error implementing Hinter.
func HintOf(err error) string {
	if err == nil {
		return ""
	}
	var coded *Error
	if errors.As(err, &coded) && coded.Hint != "" {
		return coded.Hint
	}
	var hinter Hinter
	if errors.As(err, &hinter) {
		return hinter.Hint()
	}
	return ""
}
