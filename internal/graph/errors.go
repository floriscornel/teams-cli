package graph

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/floriscornel/teams-cli/internal/output"
)

// APIError is a non-2xx Graph response, decoded from the documented envelope
//
//	{"error":{"code":…,"message":…,"target":…,"details":[…],
//	          "innerError":{"code":…,"message":…,"date":…,
//	                        "request-id":…,"client-request-id":…}}}
//
// (refs/graph/concepts/json-batching.md:165-175, refs/graph/concepts/throttling.md:59-71,
// refs/openapi/openapi/v1.0/openapi.yaml:941229-941276). innerError is optional
// and the body may not be JSON at all, so every field is best-effort.
type APIError struct {
	Method string
	URL    string
	Status int
	// Code is error.code, for example Authorization_RequestDenied or
	// TooManyRequests. It is empty for the batch sample where Graph sends "".
	Code    string
	Message string
	Target  string
	Details []ErrorDetail
	// InnerError fields, when present.
	InnerCode       string
	InnerMessage    string
	InnerStatusCode string
	RequestID       string
	ClientRequestID string
	// RetryAfter is the parsed Retry-After header (or the innerError value).
	RetryAfter time.Duration
	Header     http.Header
	// Body is the raw response body for `-v` diagnostics and tests. It may
	// contain message content, so it is never printed by default.
	Body []byte
}

// ErrorDetail mirrors error.details[].
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Target  string `json:"target"`
}

// Error implements error.
func (e *APIError) Error() string {
	parts := []string{fmt.Sprintf("graph: %s %s: HTTP %d", e.Method, e.URL, e.Status)}
	if e.Code != "" {
		parts = append(parts, e.Code)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	msg := strings.Join(parts, ": ")
	if e.RequestID != "" {
		msg += " (request-id " + e.RequestID + ")"
	}
	return msg
}

// ExitCode maps the status to the CLI contract (AGENTS.md: 0 ok, 1 error,
// 2 usage, 3 auth required, 4 not found, 5 throttled).
func (e *APIError) ExitCode() int {
	switch e.Status {
	case http.StatusBadRequest:
		// A 400 means we built a request Graph rejects: an unsupported
		// parameter is a usage problem, not an outage. The spike hit this with
		// $top=51 and with $filter on channel messages (docs/spike/phase1.md:52-53).
		return output.CodeUsage
	case http.StatusUnauthorized, http.StatusForbidden:
		return output.CodeAuth
	case http.StatusNotFound:
		return output.CodeNotFound
	case http.StatusTooManyRequests:
		return output.CodeThrottled
	default:
		return output.CodeError
	}
}

// Hint returns the one-line fix shown under the error, or "".
func (e *APIError) Hint() string {
	switch e.Status {
	case http.StatusUnauthorized:
		return "the access token was rejected; run `teams auth login`"
	case http.StatusForbidden:
		if scopes := requiredScopesFromMessage(e.Message); len(scopes) > 0 {
			return "Graph requires one of " + strings.Join(scopes, ", ") + "; an admin may need to consent (`teams auth status --admin-request`)"
		}
		return "the app registration is missing consent for this call; run `teams doctor` to see which scope is missing"
	case http.StatusNotFound:
		return "Graph returns 404 both for a missing object and for one you cannot see; check the reference"
	case http.StatusTooManyRequests:
		return "Graph throttled the call and the retries were exhausted; try again in a moment"
	case http.StatusBadRequest:
		return "the request was rejected as invalid; if it passed --limit or --since, the endpoint may not support that combination"
	default:
		return ""
	}
}

// requiredScopesFromMessage extracts the scope list Graph puts in a 403 body,
// for example "API requires one of 'ChannelMessage.ReadWrite,
// Group.ReadWrite.All'" (docs/spike/phase1.md:27).
func requiredScopesFromMessage(message string) []string {
	_, rest, ok := strings.Cut(message, "requires one of")
	if !ok {
		return nil
	}
	rest = strings.TrimSpace(rest)
	rest = strings.Trim(rest, "'\"")
	parts := strings.Split(rest, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.Trim(p, "'\""))
		if p != "" && !strings.ContainsAny(p, " ") {
			out = append(out, p)
		}
	}
	return out
}

// errorEnvelope is the documented Graph error body.
type errorEnvelope struct {
	Error struct {
		Code       string              `json:"code"`
		Message    string              `json:"message"`
		Target     string              `json:"target"`
		Details    []ErrorDetail       `json:"details"`
		InnerError errorInnerErrorBody `json:"innerError"`
	} `json:"error"`
}

type errorInnerErrorBody struct {
	Code            string          `json:"code"`
	Message         string          `json:"message"`
	Date            string          `json:"date"`
	RequestID       string          `json:"request-id"`
	ClientRequestID string          `json:"client-request-id"`
	Status          string          `json:"status"`
	RetryAfter      json.RawMessage `json:"retry-after"`
	InnerError      json.RawMessage `json:"innerError"`
}

func parseAPIError(method, url string, resp *http.Response, body []byte) *APIError {
	e := &APIError{
		Method: method,
		URL:    url,
		Status: resp.StatusCode,
		Header: resp.Header,
		Body:   body,
	}
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil {
		e.Code = env.Error.Code
		e.Message = env.Error.Message
		e.Target = env.Error.Target
		e.Details = env.Error.Details
		inner := env.Error.InnerError
		e.InnerCode = inner.Code
		e.InnerMessage = inner.Message
		e.InnerStatusCode = inner.Status
		e.RequestID = inner.RequestID
		e.ClientRequestID = inner.ClientRequestID
		e.RetryAfter = parseRetryAfterValue(inner.RetryAfter)
	}
	if e.Message == "" {
		// Graph sometimes answers with an empty or non-JSON body; keep the raw
		// text short so an error stays readable.
		if text := strings.TrimSpace(string(body)); text != "" && len(text) <= 200 && !strings.HasPrefix(text, "{") {
			e.Message = text
		}
	}
	if e.RequestID == "" {
		e.RequestID = resp.Header.Get("request-id")
	}
	if e.ClientRequestID == "" {
		e.ClientRequestID = resp.Header.Get("client-request-id")
	}
	if e.RetryAfter == 0 {
		e.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return e
}

// parseRetryAfter handles the documented integer-seconds form and, defensively,
// an HTTP-date. Graph documents only the seconds form
// (refs/graph/concepts/throttling.md:53-72).
func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// parseRetryAfterValue reads the innerError "retry-after" field, which some
// Graph services send as a string of seconds inside the 429 body
// (refs/graph/concepts/throttling.md:59-71).
func parseRetryAfterValue(raw json.RawMessage) time.Duration {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return parseRetryAfter(s)
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 0
}

// IsNotFound reports whether err is a Graph 404.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsThrottled reports whether err is a Graph 429.
func IsThrottled(err error) bool { return statusIs(err, http.StatusTooManyRequests) }

// IsUnauthorized reports whether err is a Graph 401.
func IsUnauthorized(err error) bool { return statusIs(err, http.StatusUnauthorized) }

// IsForbidden reports whether err is a Graph 403.
func IsForbidden(err error) bool { return statusIs(err, http.StatusForbidden) }

// IsBadRequest reports whether err is a Graph 400.
func IsBadRequest(err error) bool { return statusIs(err, http.StatusBadRequest) }

// IsConflict reports whether err is a Graph 409.
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

func statusIs(err error, status int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == status
}

// RequestIDOf returns the Graph request id carried by err, for support tickets.
func RequestIDOf(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.RequestID
	}
	return ""
}

// newRequestID returns a random id for the client-request-id header. The header
// itself is not documented, but the error envelope echoes the value back
// (refs/openapi/openapi/v1.0/openapi.yaml:941262-941276).
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "teams-cli"
	}
	return hex.EncodeToString(b[:])
}
