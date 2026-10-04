// Package graph is the thin, hand-written Microsoft Graph client PLAN.md
// chooses over msgraph-sdk-go. It owns the transport concerns every endpoint
// shares: base URL per cloud, bearer injection, the documented request headers,
// the error envelope, throttling retry and @odata.nextLink paging.
//
// Endpoint wrappers (teams.go, chats.go, messages.go, ...) are added per phase;
// this file is the foundation they all sit on.
package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/floriscornel/teams-cli/internal/clock"
)

// Default retry policy. Graph documents 429 with Retry-After as the throttle
// signal and exponential backoff when the header is missing
// (refs/graph/concepts/throttling.md:82-95). Jitter is our choice: the mirror
// documents none.
const (
	DefaultMaxRetries  = 3
	DefaultBaseDelay   = 500 * time.Millisecond
	DefaultMaxDelay    = 8 * time.Second
	DefaultMaxRetryFor = 60 * time.Second
)

// MaxBatchRequests is the documented limit for /$batch
// (refs/graph/concepts/json-batching.md:17).
const MaxBatchRequests = 20

// TokenSource supplies the bearer token for each request. internal/auth
// implements it; tests can pass a static one.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// TokenSourceFunc adapts a function to TokenSource.
type TokenSourceFunc func(ctx context.Context) (string, error)

// Token calls f.
func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// Recorder observes every request and response. The contract tests use it to
// validate the wire traffic against the vendored OpenAPI subset (PLAN.md Layer
// 6); production passes nil and pays nothing.
type Recorder interface {
	RecordRequest(req *http.Request, body []byte)
	RecordResponse(req *http.Request, status int, body []byte)
}

// Sleeper makes retry delays testable.
type Sleeper func(ctx context.Context, d time.Duration) error

// Options configures a Client. Only BaseURL and Token are required.
type Options struct {
	// BaseURL is the Graph service root including the version segment, for
	// example https://graph.microsoft.com/v1.0.
	BaseURL string
	// Token supplies bearer tokens.
	Token TokenSource
	// HTTPClient defaults to http.DefaultClient.
	HTTPClient *http.Client
	// UserAgent is sent as User-Agent.
	UserAgent string
	// MaxRetries defaults to DefaultMaxRetries; a negative value disables
	// retries.
	MaxRetries int
	// Clock is used for bookkeeping; defaults to the system clock.
	Clock clock.Clock
	// Sleeper defaults to a real timer. Tests inject a no-op.
	Sleeper Sleeper
	// Recorder is optional.
	Recorder Recorder
	// MaxRetryAfter caps how long a single Retry-After wait may be; a longer
	// value ends the retries instead of blocking the CLI for minutes.
	MaxRetryAfter time.Duration
	// RetryThrottledOnly disables the transport-level 503/504 retry.
	RetryThrottledOnly bool
}

// Client is a Graph HTTP client.
type Client struct {
	baseURL       string
	baseHost      string
	token         TokenSource
	http          *http.Client
	userAgent     string
	maxRetries    int
	clk           clock.Clock
	sleep         Sleeper
	recorder      Recorder
	maxRetryAfter time.Duration
	retryOnly429  bool
}

// New builds a Client.
func New(opts Options) (*Client, error) {
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, errors.New("graph: BaseURL is required")
	}
	if opts.Token == nil {
		return nil, errors.New("graph: Token is required")
	}
	u, err := url.Parse(opts.BaseURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("graph: invalid BaseURL %q", opts.BaseURL)
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.New()
	}
	sleep := opts.Sleeper
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	maxRetries := opts.MaxRetries
	if maxRetries == 0 {
		maxRetries = DefaultMaxRetries
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	maxRetryAfter := opts.MaxRetryAfter
	if maxRetryAfter == 0 {
		maxRetryAfter = DefaultMaxRetryFor
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "teams-cli"
	}
	return &Client{
		baseURL:       strings.TrimSuffix(opts.BaseURL, "/"),
		baseHost:      u.Host,
		token:         opts.Token,
		http:          hc,
		userAgent:     ua,
		maxRetries:    maxRetries,
		clk:           clk,
		sleep:         sleep,
		recorder:      opts.Recorder,
		maxRetryAfter: maxRetryAfter,
		retryOnly429:  opts.RetryThrottledOnly,
	}, nil
}

// BaseURL returns the service root.
func (c *Client) BaseURL() string { return c.baseURL }

// Request describes one Graph call.
type Request struct {
	// Method is an HTTP method.
	Method string
	// Path is a Graph path ("/me/chats") or an absolute URL (an
	// @odata.nextLink, which must be followed verbatim).
	Path string
	// Query holds query parameters.
	Query url.Values
	// Body is marshalled as JSON when non-nil.
	Body any
	// Header carries per-request headers (Prefer, Content-Type overrides).
	Header http.Header
}

// Response is a decoded-but-unparsed Graph response.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Decode unmarshals the JSON body into v, tolerating an empty body.
func (r *Response) Decode(v any) error {
	if len(bytes.TrimSpace(r.Body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decode Graph response: %w", err)
	}
	return nil
}

// Do performs the request, retrying per the documented throttling rules, and
// returns the raw response. A non-2xx response comes back as *APIError.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	if req.Method == "" {
		return nil, errors.New("graph: request method is required")
	}
	if req.Path == "" {
		return nil, errors.New("graph: request path is required")
	}
	var bodyBytes []byte
	if req.Body != nil {
		if raw, ok := req.Body.(json.RawMessage); ok {
			bodyBytes = raw
		} else {
			var err error
			if bodyBytes, err = json.Marshal(req.Body); err != nil {
				return nil, fmt.Errorf("encode request body: %w", err)
			}
		}
	}
	urlStr, err := c.resolveURL(req.Path, req.Query)
	if err != nil {
		return nil, err
	}

	var (
		lastErr  error
		resp     *Response
		attempts = c.maxRetries + 1
	)
	for attempt := range attempts {
		if attempt > 0 {
			delay, ok := retryDelay(lastErr, attempt, c.maxRetryAfter, c.retryOnly429)
			if !ok {
				break
			}
			if err := c.sleep(ctx, delay); err != nil {
				return nil, err
			}
		}
		resp, lastErr = c.attempt(ctx, req, urlStr, bodyBytes)
		if lastErr == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = errors.New("graph: request failed")
	}
	return nil, lastErr
}

// retryDelay decides the wait before the next attempt. It returns ok=false when
// the error is not retryable. 429 honors Retry-After; a missing or unusable
// Retry-After falls back to exponential backoff, which is what the throttling
// doc recommends (refs/graph/concepts/throttling.md:92-95). 503/504 are retried
// only as transport-level safety: Graph does not document them as retryable
// (PLAN.md "retry.go", refs/INDEX.md:180), so we treat them like a flaky proxy.
func retryDelay(err error, attempt int, maxRetryAfter time.Duration, only429 bool) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return 0, false
	}
	switch apiErr.Status {
	case http.StatusTooManyRequests:
		if apiErr.RetryAfter > 0 {
			if apiErr.RetryAfter > maxRetryAfter {
				return 0, false
			}
			return jitter(apiErr.RetryAfter), true
		}
		return backoff(attempt), true
	case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		if only429 {
			return 0, false
		}
		return backoff(attempt), true
	default:
		return 0, false
	}
}

func backoff(attempt int) time.Duration {
	d := DefaultBaseDelay << (attempt - 1)
	if d > DefaultMaxDelay {
		d = DefaultMaxDelay
	}
	return jitter(d)
}

// jitter spreads retries so a fleet of clients does not wake up together. The
// full-jitter form is [d/2, d].
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := d / 2
	// Jitter only spreads retries; it is not a security decision, and
	// crypto/rand would be the wrong tool for a delay.
	return half + time.Duration(rand.Int64N(int64(half)+1)) //nolint:gosec // jitter, not a secret
}

func (c *Client) attempt(ctx context.Context, req Request, urlStr string, body []byte) (*Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, urlStr, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if err := c.authorize(ctx, httpReq); err != nil {
		return nil, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	// Not documented as a request header, but the error envelope echoes it back
	// (refs/openapi/openapi/v1.0/openapi.yaml:941262-941276) and MSAL Go sends it
	// on Entra calls (refs/msal-go/apps/internal/oauth/ops/internal/comm/comm.go:301-304).
	httpReq.Header.Set("client-request-id", newRequestID())
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			httpReq.Header.Set(k, v)
		}
	}
	if c.recorder != nil {
		c.recorder.RecordRequest(httpReq, body)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", req.Method, urlStr, err)
	}
	defer func() { _ = httpResp.Body.Close() }()
	respBody, readErr := io.ReadAll(httpResp.Body)
	if c.recorder != nil {
		c.recorder.RecordResponse(httpReq, httpResp.StatusCode, respBody)
	}
	if readErr != nil {
		return nil, fmt.Errorf("%s %s: read response: %w", req.Method, urlStr, readErr)
	}
	resp := &Response{StatusCode: httpResp.StatusCode, Header: httpResp.Header, Body: respBody}
	if httpResp.StatusCode < 200 || httpResp.StatusCode > 299 {
		return resp, parseAPIError(req.Method, urlStr, httpResp, respBody)
	}
	return resp, nil
}

func (c *Client) authorize(ctx context.Context, req *http.Request) error {
	tok, err := c.token.Token(ctx)
	if err != nil {
		return err
	}
	if tok == "" {
		return errors.New("graph: token source returned an empty token")
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

// resolveURL turns a path or an absolute nextLink into a URL. Next links are
// followed verbatim as documented (refs/graph/concepts/paging.md:91); only a
// link pointing at another host is refused, because following it would send the
// bearer token to that host.
func (c *Client) resolveURL(path string, query url.Values) (string, error) {
	var out string
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		u, err := url.Parse(path)
		if err != nil {
			return "", fmt.Errorf("graph: invalid URL %q: %w", path, err)
		}
		if !strings.EqualFold(u.Host, c.baseHost) {
			return "", fmt.Errorf("graph: refusing to follow a link to %s (expected %s)", u.Host, c.baseHost)
		}
		out = path
	} else {
		out = c.baseURL + "/" + strings.TrimPrefix(path, "/")
	}
	if len(query) == 0 {
		return out, nil
	}
	sep := "?"
	if strings.Contains(out, "?") {
		sep = "&"
	}
	return out + sep + query.Encode(), nil
}

// Get issues a GET and decodes the JSON body into out (which may be nil).
func (c *Client) Get(ctx context.Context, path string, out any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodGet, path, nil, out, opts...)
}

// Post issues a POST.
func (c *Client) Post(ctx context.Context, path string, body, out any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodPost, path, body, out, opts...)
}

// Patch issues a PATCH.
func (c *Client) Patch(ctx context.Context, path string, body, out any, opts ...RequestOption) error {
	return c.call(ctx, http.MethodPatch, path, body, out, opts...)
}

// Delete issues a DELETE.
func (c *Client) Delete(ctx context.Context, path string, opts ...RequestOption) error {
	return c.call(ctx, http.MethodDelete, path, nil, nil, opts...)
}

func (c *Client) call(ctx context.Context, method, path string, body, out any, opts ...RequestOption) error {
	req := Request{Method: method, Path: path, Body: body}
	for _, opt := range opts {
		opt(&req)
	}
	resp, err := c.Do(ctx, req)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return resp.Decode(out)
}

// RequestOption customizes a convenience call.
type RequestOption func(*Request)

// WithQuery sets query parameters.
func WithQuery(q url.Values) RequestOption {
	return func(r *Request) { r.Query = q }
}

// WithHeader sets one request header.
func WithHeader(key, value string) RequestOption {
	return func(r *Request) {
		if r.Header == nil {
			r.Header = http.Header{}
		}
		r.Header.Set(key, value)
	}
}

// WithBody replaces the request body (used by raw `teams api` calls).
func WithBody(body any) RequestOption {
	return func(r *Request) { r.Body = body }
}
