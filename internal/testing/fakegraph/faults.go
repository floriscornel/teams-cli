package fakegraph

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// This file is the fault injector. Faults are deterministic: each one counts
// the requests that match its route selector and fires on the configured call
// index, so a test never sleeps to reach a retry path (PLAN.md Layer 2, "Fault
// injection", and the "no time.Sleep in tests" rule).

// Fault injects one failure into the fake.
//
// Selector: Method and Path (both optional) match a request; Path supports a
// trailing "*" for a prefix match. Call selects which matching request the
// fault fires on, counting from 1; 0 means every matching request.
//
// Effect: Status writes the status with a Graph error envelope; a Delay-only
// fault (Status 0, MalformedJSON false) waits and then serves the real
// response; MalformedJSON returns Body (or a truncated JSON fragment) as the
// response body.
type Fault struct {
	// Method matches the HTTP method; "" matches every method.
	Method string
	// Path matches the Graph path relative to the service root (for example
	// "/me/chats"), with an optional trailing "*" prefix match; "" matches
	// every path.
	Path string
	// Call is the 1-based index among matching requests, or 0 for all.
	Call int
	// Status is the response status; 0 means "do not fail, only delay".
	Status int
	// Code overrides the error envelope's code.
	Code string
	// Message overrides the error envelope's message.
	Message string
	// RetryAfter sets the Retry-After header in seconds. It is what the
	// throttling doc tells clients to honour
	// (refs/graph/concepts/throttling.md:53-72).
	RetryAfter int
	// ClaimChallenge adds the CAE WWW-Authenticate claims challenge to a 401,
	// which is how Graph reports a token that CAE revoked
	// (refs/entra/docs/identity-platform/app-resilience-continuous-access-evaluation.md:35-41).
	ClaimChallenge bool
	// MalformedJSON returns Body as-is (or a truncated fragment) instead of a
	// valid response.
	MalformedJSON bool
	// Body overrides the raw response body.
	Body string
	// Delay delays the response without failing (a slow response).
	Delay time.Duration
}

// Throttle returns a 429 fault with a Retry-After header.
func Throttle(method, path string, call, retryAfter int) Fault {
	return Fault{Method: method, Path: path, Call: call, Status: http.StatusTooManyRequests, RetryAfter: retryAfter}
}

// Unavailable returns a 503 fault. Graph does not document 503 as retryable
// (refs/INDEX.md:180), so the CLI treats it as transport-level safety only.
func Unavailable(method, path string, call int) Fault {
	return Fault{Method: method, Path: path, Call: call, Status: http.StatusServiceUnavailable}
}

// ExpiredToken returns a 401 fault for an expired access token. With challenge
// set it also carries the CAE WWW-Authenticate claims challenge.
func ExpiredToken(method, path string, call int, challenge bool) Fault {
	return Fault{
		Method:         method,
		Path:           path,
		Call:           call,
		Status:         http.StatusUnauthorized,
		ClaimChallenge: challenge,
	}
}

// ForbiddenScope returns a 403 fault naming the scope the CLI is missing.
func ForbiddenScope(method, path string, call int, scope string) Fault {
	return Fault{
		Method:  method,
		Path:    path,
		Call:    call,
		Status:  http.StatusForbidden,
		Code:    "Authorization_RequestDenied",
		Message: fmt.Sprintf("API requires one of '%s'.", strings.Join(scopeAlternativesOf(scope), ", ")),
	}
}

// Malformed returns a response whose body is not valid JSON, so the client's
// decoder fails.
func Malformed(method, path string, call int, body string) Fault {
	return Fault{Method: method, Path: path, Call: call, MalformedJSON: true, Body: body}
}

// Slow returns a fault that delays the matching response.
func Slow(method, path string, call int, delay time.Duration) Fault {
	return Fault{Method: method, Path: path, Call: call, Delay: delay}
}

func (f Fault) validate() error {
	if f.Call < 0 {
		return fmt.Errorf("call must not be negative, got %d", f.Call)
	}
	if f.Delay < 0 {
		return fmt.Errorf("delay must not be negative, got %s", f.Delay)
	}
	if f.RetryAfter < 0 {
		return fmt.Errorf("retryAfter must not be negative, got %d", f.RetryAfter)
	}
	if f.Status != 0 && (f.Status < 100 || f.Status > 599) {
		return fmt.Errorf("status %d is not a valid HTTP status", f.Status)
	}
	if f.Status == 0 && !f.MalformedJSON && f.Body != "" {
		return fmt.Errorf("body needs a status or MalformedJSON")
	}
	return nil
}

// matches reports whether the fault's selector covers a request.
func (f Fault) matches(method, path string) bool {
	if f.Method != "" && !strings.EqualFold(f.Method, method) {
		return false
	}
	switch {
	case f.Path == "":
		return true
	case strings.HasSuffix(f.Path, "*"):
		return strings.HasPrefix(path, strings.TrimSuffix(f.Path, "*"))
	default:
		return f.Path == path
	}
}

// applyFault runs the first matching fault. It reports whether the request was
// answered by the fault (so serving stops); a delay-only fault returns false
// after waiting.
func (s *Server) applyFault(w http.ResponseWriter, r *http.Request, rel string) bool {
	s.faultMu.Lock()
	var matched *Fault
	for i := range s.opts.Faults {
		f := &s.opts.Faults[i]
		if !f.matches(r.Method, rel) {
			continue
		}
		s.faultCalls[i]++
		if f.Call == 0 || f.Call == s.faultCalls[i] {
			matched = f
			break
		}
	}
	s.faultMu.Unlock()
	if matched == nil {
		return false
	}

	if matched.Delay > 0 {
		// The wait is context-aware, and it only happens when a test asked for
		// a slow response: the happy path has no timers.
		timer := time.NewTimer(matched.Delay)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return true
		case <-timer.C:
		}
	}
	if matched.MalformedJSON && matched.Status == 0 {
		body := matched.Body
		if body == "" {
			body = `{"value": [`
		}
		writeRaw(w, http.StatusOK, []byte(body))
		return true
	}
	if matched.Status == 0 {
		return false
	}
	writeFault(w, r, *matched)
	return true
}

// writeFault renders a fault's response.
func writeFault(w http.ResponseWriter, r *http.Request, f Fault) {
	if f.RetryAfter > 0 {
		w.Header().Set("Retry-After", fmt.Sprint(f.RetryAfter))
	}
	if f.ClaimChallenge {
		w.Header().Set("WWW-Authenticate", claimsChallengeHeader())
	}
	if f.Body != "" {
		writeRaw(w, f.Status, []byte(f.Body))
		return
	}
	code, message := faultDefaults(f.Status)
	if f.Code != "" {
		code = f.Code
	}
	if f.Message != "" {
		message = f.Message
	}
	env := errorEnvelopeWire{Error: errorBodyWire{
		Code:    code,
		Message: message,
		InnerError: &innerErrorWire{
			Code:            fmt.Sprint(f.Status),
			Date:            time.Now().UTC().Format(graphTimeFormat),
			Message:         message,
			RequestID:       newRequestID(),
			ClientRequestID: r.Header.Get("client-request-id"),
			Status:          fmt.Sprint(f.Status),
		},
	}}
	if f.RetryAfter > 0 {
		env.Error.InnerError.RetryAfter = fmt.Sprint(f.RetryAfter)
	}
	writeJSON(w, f.Status, env)
}

// faultDefaults returns the code and message Graph documents for a status. The
// 429 body is the documented sample (refs/graph/concepts/throttling.md:53-71);
// the 401 message is the CAE wording; 503 has no documented envelope, so it is
// explicitly labelled as our own choice.
func faultDefaults(status int) (code, message string) {
	switch status {
	case http.StatusTooManyRequests:
		return "TooManyRequests", "Please retry again later."
	case http.StatusServiceUnavailable:
		return "ServiceUnavailable", "Service is temporarily unavailable."
	case http.StatusUnauthorized:
		return "InvalidAuthenticationToken", "Lifetime validation failed, the token is expired."
	case http.StatusForbidden:
		return "Authorization_RequestDenied", "Insufficient privileges to complete the operation."
	default:
		return "Request_BadRequest", "The request was rejected."
	}
}

// claimsChallengeHeader renders the CAE challenge exactly as the doc's example
// does: a Bearer scheme with authorization_uri, error="insufficient_claims" and
// a base64url-ish claims value
// (refs/entra/docs/identity-platform/claims-challenge.md:32,44-45;
// refs/entra/docs/identity-platform/app-resilience-continuous-access-evaluation.md:35-41).
func claimsChallengeHeader() string {
	claims := base64.StdEncoding.EncodeToString([]byte(`{"access_token":{"acrs":{"essential":true,"value":"cp1"}}}`))
	return `Bearer realm="", authorization_uri="https://login.microsoftonline.com/common/oauth2/authorize",` +
		` error="insufficient_claims", claims="` + claims + `"`
}
