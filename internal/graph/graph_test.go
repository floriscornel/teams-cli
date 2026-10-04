package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/output"
)

// fakeGraph is a tiny scriptable server: each handler call pops the next
// response from the queue, so a test can assert multi-attempt behaviour.
type fakeGraph struct {
	t        *testing.T
	mu       sync.Mutex
	requests []*recorded
	next     []func(w http.ResponseWriter, r *http.Request, body []byte)
	last     func(w http.ResponseWriter, r *http.Request, body []byte)
	server   *httptest.Server
}

type recorded struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   []byte
}

func newFakeGraph(t *testing.T, handlers ...func(w http.ResponseWriter, r *http.Request, body []byte)) *fakeGraph {
	f := &fakeGraph{t: t, next: handlers}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, &recorded{
			method: r.Method, path: r.URL.Path, query: r.URL.Query(), header: r.Header.Clone(), body: body,
		})
		var handler func(http.ResponseWriter, *http.Request, []byte)
		switch {
		case len(f.next) > 0:
			handler = f.next[0]
			f.next = f.next[1:]
			f.last = handler
		default:
			// Re-serve the last handler so tests that need N identical
			// responses can register one.
			handler = f.last
		}
		f.mu.Unlock()
		if handler == nil {
			http.Error(w, `{"error":{"code":"NoHandler","message":"no handler left"}}`, http.StatusInternalServerError)
			return
		}
		handler(w, r, body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGraph) calls() []*recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*recorded(nil), f.requests...)
}

func client(t *testing.T, f *fakeGraph, opts ...func(*Options)) *Client {
	t.Helper()
	o := Options{
		BaseURL: f.server.URL + "/v1.0",
		Token:   TokenSourceFunc(func(context.Context) (string, error) { return "tok", nil }),
		Sleeper: func(context.Context, time.Duration) error { return nil },
	}
	for _, fn := range opts {
		fn(&o)
	}
	c, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func jsonHandler(status int, body string) func(http.ResponseWriter, *http.Request, []byte) {
	return func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body) //nolint:gosec // a test fixture response, not user input
	}
}

func TestGetSendsDocumentedHeaders(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusOK, `{"id":"42"}`))
	c := client(t, f, func(o *Options) { o.UserAgent = "teams/0.1.0" })
	var out struct {
		ID string `json:"id"`
	}
	if err := c.Get(context.Background(), "/me", &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != "42" {
		t.Errorf("decoded %+v", out)
	}
	got := f.calls()[0]
	if got.header.Get("Authorization") != "Bearer tok" {
		t.Errorf("Authorization = %q", got.header.Get("Authorization"))
	}
	if got.header.Get("Accept") != "application/json" {
		t.Errorf("Accept = %q", got.header.Get("Accept"))
	}
	if got.header.Get("User-Agent") != "teams/0.1.0" {
		t.Errorf("User-Agent = %q", got.header.Get("User-Agent"))
	}
	if got.header.Get("client-request-id") == "" {
		t.Error("client-request-id header missing")
	}
	if got.path != "/v1.0/me" {
		t.Errorf("path = %q", got.path)
	}
}

func TestPostSetsContentTypeAndBody(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusCreated, `{"id":"1"}`))
	c := client(t, f)
	body := map[string]any{"body": map[string]string{"content": "hi"}}
	if err := c.Post(context.Background(), "/chats/1/messages", body, nil); err != nil {
		t.Fatal(err)
	}
	got := f.calls()[0]
	if got.header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q", got.header.Get("Content-Type"))
	}
	var decoded map[string]map[string]string
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["body"]["content"] != "hi" {
		t.Errorf("body = %s", got.body)
	}
}

func TestQueryAndHeaderOptions(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusOK, `{}`))
	c := client(t, f)
	err := c.Get(context.Background(), "/messages", nil,
		WithQuery(url.Values{"$top": {"50"}, "$filter": {"a eq 'b'"}}),
		WithHeader("Prefer", PreferUnknownEnumMembers))
	if err != nil {
		t.Fatal(err)
	}
	got := f.calls()[0]
	if got.query.Get("$top") != "50" || got.query.Get("$filter") != "a eq 'b'" {
		t.Errorf("query = %v", got.query)
	}
	if got.header.Get("Prefer") != "include-unknown-enum-members" {
		t.Errorf("Prefer = %q", got.header.Get("Prefer"))
	}
}

func TestRetryHonorsRetryAfter(t *testing.T) {
	f := newFakeGraph(t,
		func(w http.ResponseWriter, _ *http.Request, _ []byte) {
			w.Header().Set("Retry-After", "2")
			jsonHandler(http.StatusTooManyRequests, `{"error":{"code":"TooManyRequests","message":"throttled"}}`)(w, nil, nil)
		},
		jsonHandler(http.StatusOK, `{"ok":true}`))
	var delays []time.Duration
	c := client(t, f, func(o *Options) {
		o.Sleeper = func(_ context.Context, d time.Duration) error {
			delays = append(delays, d)
			return nil
		}
	})
	if err := c.Get(context.Background(), "/me", nil); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 1 {
		t.Fatalf("delays = %v, want one retry", delays)
	}
	if delays[0] < time.Second || delays[0] > 2*time.Second {
		t.Errorf("delay = %v, want ~2s with jitter in [1s,2s]", delays[0])
	}
	if len(f.calls()) != 2 {
		t.Errorf("requests = %d, want 2", len(f.calls()))
	}
}

func TestRetryWithoutRetryAfterUsesBackoff(t *testing.T) {
	f := newFakeGraph(t,
		jsonHandler(http.StatusTooManyRequests, `{"error":{"code":"TooManyRequests"}}`),
		jsonHandler(http.StatusTooManyRequests, `{"error":{"code":"TooManyRequests"}}`),
		jsonHandler(http.StatusOK, `{}`))
	var delays []time.Duration
	c := client(t, f, func(o *Options) {
		o.Sleeper = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	})
	if err := c.Get(context.Background(), "/me", nil); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 2 {
		t.Fatalf("delays = %v, want two", delays)
	}
	if delays[0] < DefaultBaseDelay/2 || delays[0] > DefaultBaseDelay {
		t.Errorf("first delay = %v, want [%v,%v]", delays[0], DefaultBaseDelay/2, DefaultBaseDelay)
	}
	if delays[1] <= delays[0]/2 {
		t.Errorf("second delay = %v, want a longer backoff than %v", delays[1], delays[0])
	}
}

func TestRetryGivesUpWhenRetryAfterIsTooLong(t *testing.T) {
	f := newFakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Retry-After", "600")
		jsonHandler(http.StatusTooManyRequests, `{"error":{"code":"TooManyRequests"}}`)(w, nil, nil)
	})
	c := client(t, f)
	err := c.Get(context.Background(), "/me", nil)
	if err == nil {
		t.Fatal("want a throttled error")
	}
	if !IsThrottled(err) {
		t.Errorf("IsThrottled(%v) = false", err)
	}
	if got := output.CodeOf(err); got != output.CodeThrottled {
		t.Errorf("exit code = %d, want %d", got, output.CodeThrottled)
	}
	if len(f.calls()) != 1 {
		t.Errorf("requests = %d, want 1 (no retry for a 10 minute wait)", len(f.calls()))
	}
}

func TestRetryOn503IsTransportSafetyOnly(t *testing.T) {
	newServer := func() *fakeGraph {
		return newFakeGraph(t,
			jsonHandler(http.StatusServiceUnavailable, `<html>bad gateway</html>`),
			jsonHandler(http.StatusOK, `{}`))
	}
	f := newServer()
	c := client(t, f)
	if err := c.Get(context.Background(), "/me", nil); err != nil {
		t.Fatalf("503 should be retried: %v", err)
	}
	if len(f.calls()) != 2 {
		t.Errorf("requests = %d, want 2", len(f.calls()))
	}

	f2 := newServer()
	c2 := client(t, f2, func(o *Options) { o.RetryThrottledOnly = true })
	if err := c2.Get(context.Background(), "/me", nil); err == nil {
		t.Fatal("RetryThrottledOnly should not retry a 503")
	}
}

func TestRetriesAreBounded(t *testing.T) {
	var handlers []func(http.ResponseWriter, *http.Request, []byte)
	for range 10 {
		handlers = append(handlers, jsonHandler(http.StatusTooManyRequests, `{"error":{"code":"TooManyRequests"}}`))
	}
	f := newFakeGraph(t, handlers...)
	c := client(t, f)
	err := c.Get(context.Background(), "/me", nil)
	if err == nil {
		t.Fatal("want an error after exhausting retries")
	}
	if want := DefaultMaxRetries + 1; len(f.calls()) != want {
		t.Errorf("requests = %d, want %d", len(f.calls()), want)
	}
}

func TestErrorsMapToExitCodesAndHints(t *testing.T) {
	cases := []struct {
		status   int
		body     string
		wantCode int
		hintPart string
	}{
		{http.StatusBadRequest, `{"error":{"code":"BadRequest","message":"limit of '50' exceeded"}}`, output.CodeUsage, "--limit"},
		{http.StatusUnauthorized, `{"error":{"code":"InvalidAuthenticationToken","message":"token expired"}}`, output.CodeAuth, "auth login"},
		{http.StatusForbidden, `{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges to complete the operation."}}`, output.CodeAuth, "doctor"},
		{http.StatusForbidden, `{"error":{"code":"Forbidden","message":"API requires one of 'ChannelMessage.ReadWrite, Group.ReadWrite.All'"}}`, output.CodeAuth, "ChannelMessage.ReadWrite"},
		{http.StatusNotFound, `{"error":{"code":"NotFound","message":"not found"}}`, output.CodeNotFound, "404"},
		{http.StatusConflict, `{"error":{"code":"Conflict","message":"already exists"}}`, output.CodeError, ""},
		{http.StatusInternalServerError, `not json at all`, output.CodeError, ""},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			f := newFakeGraph(t, jsonHandler(tc.status, tc.body))
			c := client(t, f, func(o *Options) { o.MaxRetries = -1 })
			err := c.Get(context.Background(), "/me", nil)
			if err == nil {
				t.Fatal("want an error")
			}
			if got := output.CodeOf(err); got != tc.wantCode {
				t.Errorf("exit code = %d, want %d (%v)", got, tc.wantCode, err)
			}
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error is not an *APIError: %T", err)
			}
			if tc.hintPart != "" && !strings.Contains(apiErr.Hint(), tc.hintPart) {
				t.Errorf("hint = %q, want it to mention %q", apiErr.Hint(), tc.hintPart)
			}
		})
	}
}

func TestErrorParsesInnerErrorAndRequestID(t *testing.T) {
	body := `{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges","target":"site",
	  "details":[{"code":"A","message":"detail","target":"x"}],
	  "innerError":{"code":"403","message":"inner","date":"2026-10-04T10:00:00","request-id":"rid-1","client-request-id":"cid-1","status":"403"}}}`
	f := newFakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("request-id", "header-rid")
		jsonHandler(http.StatusForbidden, body)(w, nil, nil)
	})
	c := client(t, f)
	err := c.Get(context.Background(), "/me", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("not an APIError: %v", err)
	}
	if apiErr.Code != "Authorization_RequestDenied" || apiErr.Target != "site" || len(apiErr.Details) != 1 {
		t.Errorf("parsed %+v", apiErr)
	}
	if apiErr.InnerCode != "403" || apiErr.InnerMessage != "inner" || apiErr.InnerStatusCode != "403" {
		t.Errorf("inner error parsed as %+v", apiErr)
	}
	if apiErr.RequestID != "rid-1" || apiErr.ClientRequestID != "cid-1" {
		t.Errorf("request ids = %q / %q", apiErr.RequestID, apiErr.ClientRequestID)
	}
	if RequestIDOf(err) != "rid-1" {
		t.Errorf("RequestIDOf = %q", RequestIDOf(err))
	}
	if !strings.Contains(err.Error(), "request-id rid-1") {
		t.Errorf("error text lacks the request id: %v", err)
	}
}

func TestErrorFallsBackToHeaderRequestID(t *testing.T) {
	f := newFakeGraph(t, func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("request-id", "only-in-header")
		jsonHandler(http.StatusNotFound, `{"error":{"code":"NotFound","message":"nope"}}`)(w, nil, nil)
	})
	c := client(t, f)
	err := c.Get(context.Background(), "/me", nil)
	if RequestIDOf(err) != "only-in-header" {
		t.Errorf("RequestIDOf = %q", RequestIDOf(err))
	}
}

func TestParseRetryAfterForms(t *testing.T) {
	if got := parseRetryAfter("10"); got != 10*time.Second {
		t.Errorf("seconds form = %v", got)
	}
	if got := parseRetryAfter(""); got != 0 {
		t.Errorf("empty = %v", got)
	}
	if got := parseRetryAfter("-5"); got != 0 {
		t.Errorf("negative = %v", got)
	}
	if got := parseRetryAfter("not a number"); got != 0 {
		t.Errorf("garbage = %v", got)
	}
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got <= 0 || got > 31*time.Second {
		t.Errorf("HTTP-date form = %v", got)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Errorf("past HTTP-date = %v", got)
	}
}

func TestErrorEnvelopeRetryAfterInsideBody(t *testing.T) {
	body := `{"error":{"code":"TooManyRequests","message":"throttled","innerError":{"retry-after":"7"}}}`
	f := newFakeGraph(t, jsonHandler(http.StatusTooManyRequests, body),
		jsonHandler(http.StatusOK, `{}`))
	var delays []time.Duration
	c := client(t, f, func(o *Options) {
		o.Sleeper = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	})
	if err := c.Get(context.Background(), "/me", nil); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 1 || delays[0] < 3500*time.Millisecond || delays[0] > 7*time.Second {
		t.Errorf("delays = %v, want one wait of ~7s with jitter", delays)
	}
}

func TestNonJSONErrorKeepsShortBody(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusBadGateway, `upstream is down`))
	c := client(t, f, func(o *Options) { o.MaxRetries = -1 })
	err := c.Get(context.Background(), "/me", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("not an APIError: %v", err)
	}
	if apiErr.Message != "upstream is down" {
		t.Errorf("message = %q", apiErr.Message)
	}
	if IsNotFound(err) || IsThrottled(err) || IsUnauthorized(err) || IsForbidden(err) || IsBadRequest(err) || IsConflict(err) {
		t.Error("classification helpers matched a 502")
	}
	if apiErr.ExitCode() != output.CodeError {
		t.Errorf("exit code = %d", apiErr.ExitCode())
	}
}

func TestTokenErrorsPropagate(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusOK, `{}`))
	want := errors.New("no account")
	c := client(t, f, func(o *Options) {
		o.Token = TokenSourceFunc(func(context.Context) (string, error) { return "", want })
	})
	err := c.Get(context.Background(), "/me", nil)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want the token error", err)
	}
	if len(f.calls()) != 0 {
		t.Error("the request should not be sent when the token fails")
	}

	c2 := client(t, f, func(o *Options) {
		o.Token = TokenSourceFunc(func(context.Context) (string, error) { return "", nil })
	})
	if err := c2.Get(context.Background(), "/me", nil); err == nil || !strings.Contains(err.Error(), "empty token") {
		t.Fatalf("err = %v, want an empty-token error", err)
	}
}

func TestNewValidatesOptions(t *testing.T) {
	if _, err := New(Options{Token: TokenSourceFunc(func(context.Context) (string, error) { return "", nil })}); err == nil {
		t.Error("New accepted an empty BaseURL")
	}
	if _, err := New(Options{BaseURL: "https://graph.microsoft.com/v1.0"}); err == nil {
		t.Error("New accepted a nil token source")
	}
	if _, err := New(Options{BaseURL: "://nope", Token: TokenSourceFunc(func(context.Context) (string, error) { return "", nil })}); err == nil {
		t.Error("New accepted an unparseable BaseURL")
	}
}

func TestDoValidatesRequest(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusOK, `{}`))
	c := client(t, f)
	if _, err := c.Do(context.Background(), Request{Path: "/me"}); err == nil {
		t.Error("Do accepted a missing method")
	}
	if _, err := c.Do(context.Background(), Request{Method: http.MethodGet}); err == nil {
		t.Error("Do accepted a missing path")
	}
}

func TestContextCancellationStopsRetries(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusTooManyRequests, `{"error":{"code":"TooManyRequests"}}`))
	ctx, cancel := context.WithCancel(context.Background())
	c := client(t, f, func(o *Options) {
		o.Sleeper = func(ctx context.Context, _ time.Duration) error {
			cancel()
			return ctx.Err()
		}
	})
	if err := c.Get(ctx, "/me", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestGetPageFollowsNextLinkVerbatim(t *testing.T) {
	var nextLink string
	f := newFakeGraph(t,
		func(w http.ResponseWriter, r *http.Request, _ []byte) {
			nextLink = "http://" + r.Host + "/v1.0/me/chats?$top=50&$skiptoken=abc"
			jsonHandler(http.StatusOK, `{"value":[{"id":"1"}],"@odata.nextLink":"`+nextLink+`"}`)(w, r, nil)
		},
		jsonHandler(http.StatusOK, `{"value":[{"id":"2"}]}`))
	c := client(t, f)
	page, err := GetPage[struct {
		ID string `json:"id"`
	}](context.Background(), c, "/me/chats", url.Values{"$top": {"50"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Value) != 1 || page.Value[0].ID != "1" {
		t.Errorf("first page = %+v", page.Value)
	}
	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	if calls[0].query.Get("$top") != "50" {
		t.Errorf("first request query = %v", calls[0].query)
	}
}

func TestEachPageWalksEveryPage(t *testing.T) {
	f := newFakeGraph(t,
		func(w http.ResponseWriter, r *http.Request, _ []byte) {
			jsonHandler(http.StatusOK, `{"value":[{"id":"1"}],"@odata.nextLink":"http://`+r.Host+`/v1.0/me/chats?$skiptoken=2"}`)(w, r, nil)
		},
		func(w http.ResponseWriter, r *http.Request, _ []byte) {
			jsonHandler(http.StatusOK, `{"value":[{"id":"2"}],"@odata.nextLink":"http://`+r.Host+`/v1.0/me/chats?$skiptoken=3"}`)(w, r, nil)
		},
		jsonHandler(http.StatusOK, `{"value":[{"id":"3"}]}`))
	c := client(t, f)
	var ids []string
	err := EachPage(context.Background(), c, "/me/chats", nil, func(page Page[struct {
		ID string `json:"id"`
	}],
	) (bool, error) {
		for _, item := range page.Value {
			ids = append(ids, item.ID)
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(ids, ",") != "1,2,3" {
		t.Errorf("ids = %v", ids)
	}
	calls := f.calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(calls))
	}
	// The next link carries its own parameters and must not be re-parameterized.
	if calls[1].query.Get("$skiptoken") != "2" {
		t.Errorf("second page query = %v", calls[1].query)
	}
}

func TestItemsIteratorStopsEarly(t *testing.T) {
	f := newFakeGraph(t,
		func(w http.ResponseWriter, r *http.Request, _ []byte) {
			jsonHandler(http.StatusOK, `{"value":[{"id":"1"},{"id":"2"}],"@odata.nextLink":"http://`+r.Host+`/v1.0/next"}`)(w, r, nil)
		},
		jsonHandler(http.StatusOK, `{"value":[{"id":"3"}]}`))
	c := client(t, f)
	var ids []string
	for item, err := range Items[struct {
		ID string `json:"id"`
	}](context.Background(), c, "/me/chats", nil) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, item.ID)
		if len(ids) == 2 {
			break
		}
	}
	if strings.Join(ids, ",") != "1,2" {
		t.Errorf("ids = %v, want the first page only", ids)
	}
	if len(f.calls()) != 1 {
		t.Errorf("calls = %d, want 1 (early stop must not fetch page 2)", len(f.calls()))
	}
}

func TestItemsIteratorYieldsErrors(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusNotFound, `{"error":{"code":"NotFound","message":"gone"}}`))
	c := client(t, f)
	count := 0
	for _, err := range Items[map[string]any](context.Background(), c, "/me/chats", nil) {
		count++
		if !IsNotFound(err) {
			t.Errorf("err = %v, want a not-found error", err)
		}
	}
	if count != 1 {
		t.Errorf("yielded %d results, want exactly one error", count)
	}
}

func TestPagingDetectsLoops(t *testing.T) {
	loop := ""
	f := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if loop == "" {
			loop = "http://" + r.Host + "/v1.0/me/chats?$skiptoken=same"
		}
		jsonHandler(http.StatusOK, `{"value":[],"@odata.nextLink":"`+loop+`"}`)(w, r, nil)
	})
	c := client(t, f)
	err := EachPage(context.Background(), c, "/me/chats", nil, func(Page[map[string]any]) (bool, error) { return true, nil })
	if err == nil || !strings.Contains(err.Error(), "loop") {
		t.Fatalf("err = %v, want a loop error", err)
	}
}

func TestNextLinkToAnotherHostIsRefused(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusOK, `{"value":[],"@odata.nextLink":"https://evil.example.com/v1.0/next"}`))
	c := client(t, f)
	err := EachPage(context.Background(), c, "/me/chats", nil, func(Page[map[string]any]) (bool, error) { return true, nil })
	if err == nil || !strings.Contains(err.Error(), "refusing to follow") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}

func TestRecorderSeesTraffic(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusOK, `{"id":"1"}`))
	rec := &recordingRecorder{}
	c := client(t, f, func(o *Options) { o.Recorder = rec })
	if err := c.Get(context.Background(), "/me", nil); err != nil {
		t.Fatal(err)
	}
	if len(rec.reqs) != 1 || len(rec.resps) != 1 {
		t.Fatalf("recorder saw %d requests and %d responses", len(rec.reqs), len(rec.resps))
	}
	if rec.resps[0].status != http.StatusOK {
		t.Errorf("recorded status = %d", rec.resps[0].status)
	}
}

type recordingRecorder struct {
	reqs  []string
	resps []struct {
		status int
		body   string
	}
}

func (r *recordingRecorder) RecordRequest(req *http.Request, _ []byte) {
	r.reqs = append(r.reqs, req.Method+" "+req.URL.Path)
}

func (r *recordingRecorder) RecordResponse(_ *http.Request, status int, body []byte) {
	r.resps = append(r.resps, struct {
		status int
		body   string
	}{status: status, body: string(body)})
}

func TestBatchSendsDocumentedEnvelope(t *testing.T) {
	var subRequestCounts []int
	f := newFakeGraph(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		var env struct {
			Requests []struct {
				ID      string            `json:"id"`
				Method  string            `json:"method"`
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    json.RawMessage   `json:"body"`
			} `json:"requests"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Errorf("batch body is not JSON: %v", err)
		}
		subRequestCounts = append(subRequestCounts, len(env.Requests))
		for _, item := range env.Requests {
			if len(item.Body) > 0 && item.Headers["Content-Type"] != "application/json" {
				t.Errorf("POST sub-request %q lacks Content-Type: %+v", item.ID, item)
			}
			if strings.HasPrefix(item.URL, "http") {
				t.Errorf("sub-request url must stay relative: %q", item.URL)
			}
		}
		jsonHandler(http.StatusOK, `{"responses":[
			{"id":"1","status":200,"headers":{"Content-Type":"application/json"},"body":{"id":"a"}},
			{"id":"2","status":403,"headers":{},"body":{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges"}}}]}`)(w, r, nil)
	})
	c := client(t, f)
	reqs := []BatchRequest{
		{ID: "1", Method: http.MethodGet, Path: "/me/chats/1"},
		{ID: "2", Method: http.MethodPost, Path: "/me/chats/2/messages", Body: map[string]string{"x": "y"}},
	}
	out, err := c.Batch(context.Background(), reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(subRequestCounts) != 1 || subRequestCounts[0] != 2 {
		t.Errorf("sub-request counts = %v, want [2]", subRequestCounts)
	}
	if len(f.calls()) != 1 || f.calls()[0].path != "/v1.0/$batch" {
		t.Fatalf("batch was not a single POST: %+v", f.calls())
	}
	if f.calls()[0].header.Get("Content-Type") != "application/json" {
		t.Errorf("outer Content-Type = %q", f.calls()[0].header.Get("Content-Type"))
	}
	var msg struct {
		ID string `json:"id"`
	}
	if err := out["1"].Decode(&msg); err != nil || msg.ID != "a" {
		t.Errorf("decode = %v, %+v", err, msg)
	}
	if err := out["1"].Err(reqs[0]); err != nil {
		t.Errorf("sub-response 1 should be a success: %v", err)
	}
	if err := out["2"].Err(reqs[1]); !IsForbidden(err) {
		t.Errorf("sub-response 2 error = %v, want a 403 APIError", err)
	}
	// BatchOrdered keeps the request order and returns a slice.
	single, err := c.BatchOrdered(context.Background(), reqs[:1])
	if err != nil || len(single) != 1 || single[0].ID != "1" {
		t.Errorf("BatchOrdered = %+v, %v", single, err)
	}
}

func TestBatchValidatesItsInput(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusOK, `{"responses":[]}`))
	c := client(t, f)
	if _, err := c.Batch(context.Background(), nil); err == nil {
		t.Error("empty batch accepted")
	}
	tooMany := make([]BatchRequest, MaxBatchRequests+1)
	for i := range tooMany {
		tooMany[i] = BatchRequest{ID: fmt.Sprint(i), Method: http.MethodGet, Path: "/me"}
	}
	if _, err := c.Batch(context.Background(), tooMany); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Errorf("oversized batch = %v", err)
	}
	if _, err := c.Batch(context.Background(), []BatchRequest{{Method: http.MethodGet, Path: "/me"}}); err == nil {
		t.Error("batch accepted a missing id")
	}
	dup := []BatchRequest{
		{ID: "a", Method: http.MethodGet, Path: "/me"},
		{ID: "a", Method: http.MethodGet, Path: "/me"},
	}
	if _, err := c.Batch(context.Background(), dup); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate id = %v", err)
	}
	if _, err := c.Batch(context.Background(), []BatchRequest{{ID: "a"}}); err == nil {
		t.Error("batch accepted a request without method/url")
	}
	if len(f.calls()) != 0 {
		t.Error("invalid batches must not reach the network")
	}
}

func TestBatchMissingSubResponseIsReported(t *testing.T) {
	f := newFakeGraph(t, jsonHandler(http.StatusOK, `{"responses":[{"id":"1","status":200,"body":{}}]}`))
	c := client(t, f)
	_, err := c.Batch(context.Background(), []BatchRequest{
		{ID: "1", Method: http.MethodGet, Path: "/me"},
		{ID: "2", Method: http.MethodGet, Path: "/me/chats"},
	})
	if err == nil || !strings.Contains(err.Error(), "missing sub-response") {
		t.Fatalf("err = %v", err)
	}
}

func TestWithQueryOnBatchItem(t *testing.T) {
	if got := withQuery("/me/chats", map[string]string{"$top": "1"}); got != "/me/chats?$top=1" {
		t.Errorf("withQuery = %q", got)
	}
	if got := withQuery("/me/chats?$skip=1", map[string]string{"$top": "1"}); got != "/me/chats?$skip=1&$top=1" {
		t.Errorf("withQuery = %q", got)
	}
	if got := withQuery("/me", nil); got != "/me" {
		t.Errorf("withQuery = %q", got)
	}
}

func TestTopHelpers(t *testing.T) {
	if got := Top(50).Get("$top"); got != "50" {
		t.Errorf("Top = %q", got)
	}
	q := TopWith(25, url.Values{"$filter": {"a eq 'b'"}})
	if q.Get("$top") != "25" || q.Get("$filter") != "a eq 'b'" {
		t.Errorf("TopWith = %v", q)
	}
}
