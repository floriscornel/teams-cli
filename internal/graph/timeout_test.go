package graph

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The timeout and the log lines exist because a stalled connection used to look
// exactly like a hang: nothing on stdout, nothing on stderr, no exit. These
// tests pin both halves of the fix.

func TestDefaultTimeoutIsApplied(t *testing.T) {
	c, err := New(Options{BaseURL: "https://graph.microsoft.com/v1.0", Token: TokenSourceFunc(func(context.Context) (string, error) { return "t", nil })})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.timeout != DefaultTimeout {
		t.Errorf("timeout = %s, want %s", c.timeout, DefaultTimeout)
	}
	if c.logf == nil {
		t.Error("logf is nil; every call site logs unconditionally")
	}

	// A negative timeout disables the bound, which the tests that drive a fake
	// server with an injected sleeper rely on.
	c, err = New(Options{BaseURL: "https://graph.microsoft.com/v1.0", Token: TokenSourceFunc(func(context.Context) (string, error) { return "t", nil }), Timeout: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.timeout != 0 {
		t.Errorf("timeout = %s, want 0 (disabled)", c.timeout)
	}
}

func TestRequestTimesOutInsteadOfHanging(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	c, err := New(Options{
		BaseURL: srv.URL,
		Token:   TokenSourceFunc(func(context.Context) (string, error) { return "t", nil }),
		Timeout: 200 * time.Millisecond,
		// One attempt: the retry path would otherwise multiply the wait.
		MaxRetries: -1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	started := time.Now()
	var msg me
	err = c.Get(context.Background(), "/me", &msg)
	if err == nil {
		t.Fatal("Get returned nil, want a timeout error")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("Get took %s; the timeout did not fire", elapsed)
	}
}

func TestLoggerReceivesRequestAndResponseLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)

	var mu sync.Mutex
	var lines []string
	c, err := New(Options{
		BaseURL: srv.URL,
		Token:   TokenSourceFunc(func(context.Context) (string, error) { return "t", nil }),
		Logger: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var msg me
	if err := c.Get(context.Background(), "/me", &msg); err != nil {
		t.Fatalf("Get: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "GET /me -> 200") {
		t.Errorf("no completion line in %q", joined)
	}
	if !strings.Contains(joined, "in ") {
		t.Errorf("the completion line carries no duration: %q", joined)
	}
}

func TestRetryIsLogged(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)

	var mu2 sync.Mutex
	var lines []string
	c, err := New(Options{
		BaseURL: srv.URL,
		Token:   TokenSourceFunc(func(context.Context) (string, error) { return "t", nil }),
		Logger: func(format string, args ...any) {
			mu2.Lock()
			defer mu2.Unlock()
			lines = append(lines, fmt.Sprintf(format, args...))
		},
		Sleeper: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	var msg me
	if err := c.Get(context.Background(), "/me", &msg); err != nil {
		t.Fatalf("Get: %v", err)
	}
	mu2.Lock()
	defer mu2.Unlock()
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "retrying GET /me") {
		t.Errorf("the throttle retry was not logged: %q", joined)
	}
}

// me is the smallest decodable response shape.
type me struct {
	ID string `json:"id"`
}
