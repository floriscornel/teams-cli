package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
)

// Part names the piece of a request or response a contract failure is about, so
// a failing test says "the body" instead of dumping a nested error tree.
type Part string

// The four parts a call is checked in.
const (
	PartPath     Part = "path"
	PartQuery    Part = "query"
	PartBody     Part = "body"
	PartResponse Part = "response"
)

// Error is a contract failure. It always names the route, the part and the
// offending value, because a contract failure has to be actionable without
// re-running the test under a debugger.
type Error struct {
	Method string
	Path   string
	Part   Part
	// Value is the offending value, echoed back and truncated so a large
	// response body cannot flood the test log.
	Value string
	Err   error
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "contract violation: %s %s: %s: %s", e.Method, e.Path, e.Part, e.Err)
	if e.Value != "" {
		fmt.Fprintf(&b, "\n  offending value: %s", e.Value)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// Is makes an *Error classify as any sentinel it wraps, so callers can write
// errors.Is(err, ErrBatchExempt) on a wrapped error too.
func (e *Error) Is(target error) bool { return errors.Is(e.Err, target) }

// wrapRequestError turns a kin-openapi error into a targeted *Error.
func wrapRequestError(method, path string, query url.Values, body []byte, err error) error {
	part, value, detail := classify(err, query, body)
	return &Error{Method: method, Path: path, Part: part, Value: value, Err: detail}
}

// wrapResponseError turns a kin-openapi error into a targeted *Error, phrased
// for the response side.
func wrapResponseError(method, path string, status int, body []byte, err error) error {
	value := fmt.Sprintf("status %d", status)
	if s := schemaErrorValue(err); s != "" {
		value = fmt.Sprintf("status %d, %s", status, s)
	}
	return &Error{Method: method, Path: path, Part: PartResponse, Value: value, Err: summarise(err)}
}

// classify inspects a kin-openapi error and reports which part failed plus the
// offending value.
func classify(err error, query url.Values, body []byte) (Part, string, error) {
	var reqErr *openapi3filter.RequestError
	if errors.As(err, &reqErr) {
		switch {
		case reqErr.Parameter != nil:
			part := PartQuery
			if reqErr.Parameter.In == openapi3.ParameterInPath {
				part = PartPath
			}
			value := reqErr.Parameter.Name
			if vs, ok := query[reqErr.Parameter.Name]; ok && len(vs) > 0 {
				value = fmt.Sprintf("%s=%s", reqErr.Parameter.Name, strings.Join(vs, ","))
			}
			return part, value, summarise(reqErr)
		case reqErr.RequestBody != nil:
			if v := schemaErrorValue(err); v != "" {
				return PartBody, v, summarise(reqErr)
			}
			return PartBody, truncate(body, 300), summarise(reqErr)
		default:
			return PartPath, reqErr.Reason, summarise(reqErr)
		}
	}
	var respErr *openapi3filter.ResponseError
	if errors.As(err, &respErr) {
		return PartResponse, "", summarise(respErr)
	}
	if v := schemaErrorValue(err); v != "" {
		return PartBody, v, summarise(err)
	}
	return PartBody, truncate(body, 300), summarise(err)
}

// schemaErrorValue describes the offending JSON location and value for a schema
// error, e.g. `at /body/content: 42`.
//
// kin-openapi nests schema errors: validating a chatMessage body yields an
// outer "doesn't match all schemas from allOf" error whose JSONPointer is empty,
// wrapping the inner error that actually names /body/content. Choosing the
// deepest pointer is what makes the message actionable instead of confusing.
func schemaErrorValue(err error) string {
	best := deepestSchemaError(err)
	if best == nil {
		return ""
	}
	loc := "the body"
	if ptr := best.JSONPointer(); len(ptr) > 0 {
		loc = "/" + strings.Join(ptr, "/")
	}
	return fmt.Sprintf("at %s: %s", loc, truncate([]byte(jsonSnippet(best.Value)), 200))
}

// deepestSchemaError walks the whole error tree and returns the *SchemaError
// with the most specific JSON pointer.
//
// openapi3.MultiError implements neither Unwrap() error nor Unwrap() []error, so
// it has to be special-cased; without that the walk stops at the first
// multi-error and never sees the inner SchemaError.
func deepestSchemaError(err error) *openapi3.SchemaError {
	var best *openapi3.SchemaError
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if se, ok := e.(*openapi3.SchemaError); ok {
			if best == nil || len(se.JSONPointer()) > len(best.JSONPointer()) {
				best = se
			}
		}
		if multi, ok := e.(openapi3.MultiError); ok {
			for _, c := range multi {
				walk(c)
			}
			return
		}
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			walk(u.Unwrap())
		case interface{ Unwrap() []error }:
			for _, c := range u.Unwrap() {
				walk(c)
			}
		}
	}
	walk(err)
	return best
}

// summarise unwraps a kin-openapi error down to its most specific message.
func summarise(err error) error {
	if err == nil {
		return errors.New("validation failed")
	}
	if se := deepestSchemaError(err); se != nil {
		// Re-render the deepest schema error on its own, without kin-openapi's
		// outer "doesn't match schema" wrapper.
		return errors.New(se.Error())
	}
	var reqErr *openapi3filter.RequestError
	if errors.As(err, &reqErr) && reqErr.Err != nil {
		return summarise(reqErr.Err)
	}
	var respErr *openapi3filter.ResponseError
	if errors.As(err, &respErr) && respErr.Err != nil {
		return summarise(respErr.Err)
	}
	var multi openapi3.MultiError
	if errors.As(err, &multi) && len(multi) > 0 {
		return summarise(multi[0])
	}
	return errors.New(err.Error())
}

// jsonSnippet renders a value compactly for an error message.
func jsonSnippet(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func truncate(b []byte, max int) string {
	s := strings.TrimSpace(string(b))
	if s == "" {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
