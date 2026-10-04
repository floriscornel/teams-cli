package fakegraph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers the fault injector: 429 with Retry-After, 503, a 401 with
// the CAE claims challenge, malformed JSON, a slow response, and the
// deterministic per-call index that replaces sleeping.

func TestThrottleRetryHonorsRetryAfter(t *testing.T) {
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{Throttle(http.MethodGet, "/me", 1, 3)},
	})
	c := newClient(t, srv, func(o *graph.Options) { o.MaxRetries = 2 })

	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatalf("the retry after a 429 did not succeed: %v", err)
	}
	attempts := srv.RequestsFor(http.MethodGet, "/me")
	if len(attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 (one 429 and one success)", len(attempts))
	}
	if attempts[0].Status != http.StatusTooManyRequests {
		t.Fatalf("first attempt status = %d, want 429", attempts[0].Status)
	}
	if attempts[1].Status != http.StatusOK {
		t.Fatalf("second attempt status = %d, want 200", attempts[1].Status)
	}
}

func TestThrottleWithoutRetryPropagates(t *testing.T) {
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{Throttle(http.MethodGet, "/me", 1, 7)},
	})
	c := newClient(t, srv)

	_, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"})
	if !graph.IsThrottled(err) {
		t.Fatalf("err = %v, want a throttling error", err)
	}
	var apiErr *graph.APIError
	if !asError(err, &apiErr) {
		t.Fatal("error is not an APIError")
	}
	if apiErr.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %s, want 7s (parsed from the header)", apiErr.RetryAfter)
	}
	if apiErr.ExitCode() != 5 {
		t.Fatalf("ExitCode = %d, want 5 (throttled)", apiErr.ExitCode())
	}
	if apiErr.Code != "TooManyRequests" {
		t.Fatalf("code = %q, want TooManyRequests", apiErr.Code)
	}
}

func TestRetryAfterAboveTheCapStopsRetrying(t *testing.T) {
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{Throttle(http.MethodGet, "/me", 1, 600)},
	})
	c := newClient(t, srv, func(o *graph.Options) {
		o.MaxRetries = 3
		o.MaxRetryAfter = time.Second
		// The sleeper fails the test if the client ever waits: the cap must
		// stop the loop before the sleep.
		o.Sleeper = func(context.Context, time.Duration) error {
			t.Error("the client slept despite Retry-After exceeding MaxRetryAfter")
			return nil
		}
	})
	_, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"})
	if !graph.IsThrottled(err) {
		t.Fatalf("err = %v, want a 429", err)
	}
	if got := len(srv.RequestsFor(http.MethodGet, "/me")); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
}

func TestServiceUnavailableRetried(t *testing.T) {
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{Unavailable(http.MethodGet, "/me", 1)},
	})
	c := newClient(t, srv, func(o *graph.Options) { o.MaxRetries = 2 })
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatalf("503 was not retried: %v", err)
	}
	attempts := srv.RequestsFor(http.MethodGet, "/me")
	if len(attempts) != 2 || attempts[0].Status != http.StatusServiceUnavailable {
		t.Fatalf("attempts = %+v", attempts)
	}
}

func TestExpiredTokenWithCAEChallenge(t *testing.T) {
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{ExpiredToken(http.MethodGet, "/me", 1, true)},
	})
	req, err := http.NewRequest(http.MethodGet, srv.URL()+"/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	challenge := resp.Header.Get("WWW-Authenticate")
	// The documented challenge shape: an insufficient_claims error plus a
	// base64 claims request (refs/entra/docs/identity-platform/claims-challenge.md:32,44-45).
	if !strings.Contains(challenge, `error="insufficient_claims"`) {
		t.Fatalf("WWW-Authenticate = %q, want error=insufficient_claims", challenge)
	}
	if !strings.Contains(challenge, `authorization_uri="https://login.microsoftonline.com/common/oauth2/authorize"`) {
		t.Fatalf("WWW-Authenticate = %q, want the authorization_uri", challenge)
	}
	claims := extractClaim(challenge)
	if claims == "" {
		t.Fatalf("WWW-Authenticate = %q, want a claims parameter", challenge)
	}
	raw, err := base64.StdEncoding.DecodeString(claims)
	if err != nil {
		t.Fatalf("claims value is not base64: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("claims value is not JSON: %v", err)
	}
	if _, ok := decoded["access_token"]; !ok {
		t.Fatalf("claims = %s, want an access_token claim request", raw)
	}
}

// extractClaim pulls the claims="…" value out of a WWW-Authenticate header.
func extractClaim(header string) string {
	const marker = `claims="`
	i := strings.Index(header, marker)
	if i < 0 {
		return ""
	}
	rest := header[i+len(marker):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func TestMalformedJSONFault(t *testing.T) {
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{Malformed(http.MethodGet, "/me", 1, "")},
	})
	c := newClient(t, srv)
	// GetPage decodes the body, which is where a truncated response must fail.
	if _, err := graph.GetPage[map[string]any](context.Background(), c, "/me", nil); err == nil {
		t.Fatal("the client accepted a malformed JSON response")
	} else if !strings.Contains(err.Error(), "decode") && !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("err = %v, want a decode failure", err)
	}
	if got := srv.RequestsFor(http.MethodGet, "/me"); len(got) != 1 || got[0].Status != http.StatusOK {
		t.Fatalf("recorded = %+v", got)
	}
}

func TestSlowFaultDelaysWithoutFailing(t *testing.T) {
	const delay = 60 * time.Millisecond
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{Slow(http.MethodGet, "/me", 1, delay)},
	})
	c := newClient(t, srv)

	start := time.Now()
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatalf("slow response failed: %v", err)
	}
	// The test measures the delay; it never sleeps itself.
	if elapsed := time.Since(start); elapsed < delay/2 {
		t.Fatalf("elapsed = %s, want at least %s", elapsed, delay/2)
	}
}

func TestFaultCallIndexIsDeterministic(t *testing.T) {
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{Throttle(http.MethodGet, "/me", 2, 1)},
	})
	c := newClient(t, srv)

	for i := 1; i <= 3; i++ {
		_, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"})
		if i == 2 {
			if !graph.IsThrottled(err) {
				t.Fatalf("call %d err = %v, want the injected 429", i, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("call %d err = %v, want success", i, err)
		}
	}
}

func TestFaultPathPrefixAndMethodMatching(t *testing.T) {
	srv := New(t, Options{
		Model: testModel(),
		Clock: newFakeClock(),
		Faults: []Fault{
			{Method: http.MethodGet, Path: "/me/chats*", Status: http.StatusForbidden, Code: "Authorization_RequestDenied", Message: "prefix match"},
		},
	})
	c := newClient(t, srv)
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatalf("GET /me must not match a /me/chats prefix fault: %v", err)
	}
	_, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats", nil)
	if !graph.IsForbidden(err) {
		t.Fatalf("err = %v, want 403", err)
	}
}

func TestForbiddenScopeFault(t *testing.T) {
	srv := New(t, Options{
		Model:  testModel(),
		Clock:  newFakeClock(),
		Faults: []Fault{ForbiddenScope(http.MethodGet, "/me", 1, "ChannelMessage.ReadWrite")},
	})
	c := newClient(t, srv)
	_, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"})
	var apiErr *graph.APIError
	if !asError(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("err = %v, want 403", err)
	}
	if !strings.Contains(apiErr.Message, "ChannelMessage.ReadWrite") {
		t.Fatalf("message = %q", apiErr.Message)
	}
}

func TestFaultValidation(t *testing.T) {
	cases := []struct {
		name  string
		fault Fault
	}{
		{"negative call", Fault{Call: -1}},
		{"negative delay", Fault{Delay: -time.Second}},
		{"negative retry after", Fault{RetryAfter: -1}},
		{"bad status", Fault{Status: 999}},
		{"body without status", Fault{Body: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fault.validate(); err == nil {
				t.Fatal("validate accepted an invalid fault")
			}
		})
	}
	if err := (Fault{Status: 429, RetryAfter: 1}).validate(); err != nil {
		t.Fatalf("a valid fault was rejected: %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("NewServer did not panic on an invalid fault")
		}
	}()
	NewServer(Options{Model: testModel(), Faults: []Fault{{Call: -1}}})
}
