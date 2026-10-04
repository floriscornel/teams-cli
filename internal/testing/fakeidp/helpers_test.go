package fakeidp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClientID is the public client the tests sign in with.
const testClientID = "11111111-2222-3333-4444-555555555555"

// tokenResponse is the subset of a successful token response the tests assert on.
type tokenResponse struct {
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
	ExpiresIn    int    `json:"expires_in"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ClientInfo   string `json:"client_info"`
}

// errorResponse is the shape of an OAuth error body.
type errorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
	Suberror    string `json:"suberror"`
	Codes       []int  `json:"error_codes"`
	Correlation string `json:"correlation_id"`
}

// deviceCodeResponse is the shape of the device-code endpoint's body.
type deviceCodeResponse struct {
	UserCode        string `json:"user_code"`
	DeviceCode      string `json:"device_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
	Message         string `json:"message"`
}

// do performs a request with a background context and returns the status and body. The helper
// builds the request itself so every call site carries a context.
func do(t *testing.T, client *http.Client, method, endpoint string, body io.Reader) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, endpoint, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, endpoint, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body of the response to %s %s: %v", method, endpoint, err)
	}
	return resp.StatusCode, raw
}

// postForm posts an application/x-www-form-urlencoded request and returns the status and body, the
// way MSAL's client does (refs/msal-go/apps/internal/oauth/ops/internal/comm/comm.go:185-220).
func postForm(t *testing.T, client *http.Client, endpoint string, form url.Values) (int, []byte) {
	t.Helper()
	return do(t, client, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
}

// get issues a GET and returns the status and body.
func get(t *testing.T, client *http.Client, endpoint string) (int, []byte) {
	t.Helper()
	return do(t, client, http.MethodGet, endpoint, nil)
}

// doRaw performs a request with a raw body and returns the undrained response, so a test can
// assert on both the status and the body it chose to send.
func doRaw(t *testing.T, client *http.Client, method, endpoint, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, endpoint, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// readAll drains a response body.
func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body: %v", err)
	}
	return body
}

// decodeBase64URL decodes an unpadded base64url string, the encoding JWT and client_info use
// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:327-330).
func decodeBase64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// decode unmarshals a JSON body into T, failing the test when it does not fit.
func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decoding %s: %v", string(body), err)
	}
	return v
}

// tokenSuccess asserts that the token endpoint answered 200 and returns the decoded response.
func tokenSuccess(t *testing.T, status int, body []byte) tokenResponse {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("token endpoint answered HTTP %d: %s", status, body)
	}
	return decode[tokenResponse](t, body)
}

// tokenError asserts that the token endpoint answered 400 and returns the decoded error.
func tokenError(t *testing.T, status int, body []byte) errorResponse {
	t.Helper()
	// MSAL retries a device-code poll only when the status is exactly 400
	// (refs/msal-go/apps/internal/oauth/oauth.go:302-328) and surfaces every other status as a
	// CallErr whose message embeds the body, so the tests pin the status down.
	if status != http.StatusBadRequest {
		t.Fatalf("token endpoint answered HTTP %d, want 400: %s", status, body)
	}
	return decode[errorResponse](t, body)
}

// requestDeviceCode runs the device-code request MSAL starts with: client_id and scope, plus
// client_info=1 (refs/msal-go/apps/internal/oauth/ops/accesstokens/accesstokens.go:382-397).
func requestDeviceCode(t *testing.T, srv *Server) deviceCodeResponse {
	t.Helper()
	status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().DeviceCode, url.Values{
		"client_id":   {testClientID},
		"scope":       {"User.Read"},
		"client_info": {"1"},
	})
	if status != http.StatusOK {
		t.Fatalf("device-code endpoint answered HTTP %d: %s", status, body)
	}
	return decode[deviceCodeResponse](t, body)
}

// pollDeviceCode sends one device-code grant request.
func pollDeviceCode(t *testing.T, srv *Server, deviceCode string) (int, []byte) {
	t.Helper()
	return postForm(t, srv.HTTPClient(), srv.Endpoints().Token, url.Values{
		"grant_type":  {grantTypeDeviceCode},
		"client_id":   {testClientID},
		"device_code": {deviceCode},
		"scope":       {"User.Read"},
	})
}

// redeemRefreshToken sends a refresh_token grant request.
func redeemRefreshToken(t *testing.T, srv *Server, refreshToken string) (int, []byte) {
	t.Helper()
	return postForm(t, srv.HTTPClient(), srv.Endpoints().Token, url.Values{
		"grant_type":    {grantTypeRefresh},
		"client_id":     {testClientID},
		"refresh_token": {refreshToken},
		"scope":         {"User.Read"},
	})
}

// authorizeURL builds the authorization URL MSAL would hand to the browser: response_type=code,
// response_mode=form_post and a PKCE S256 challenge
// (refs/msal-go/apps/internal/base/base.go:297-332).
func authorizeURL(srv *Server, redirectURI, verifier string, override url.Values) string {
	form := url.Values{
		"client_id":             {testClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {"User.Read openid profile offline_access"},
		"state":                 {"test-state"},
		"code_challenge":        {codeChallengeFor(verifier)},
		"code_challenge_method": {"S256"},
		"response_mode":         {"form_post"},
		"prompt":                {"select_account"},
	}
	for key, values := range override {
		if len(values) == 0 {
			form.Del(key)
			continue
		}
		form[key] = values
	}
	return srv.Endpoints().Authorization + "?" + form.Encode()
}

// redeemAuthCode sends the authorization_code grant for a minted code.
func redeemAuthCode(t *testing.T, srv *Server, code, verifier, redirectURI string) (int, []byte) {
	t.Helper()
	return postForm(t, srv.HTTPClient(), srv.Endpoints().Token, url.Values{
		"grant_type":    {grantTypeAuthCode},
		"client_id":     {testClientID},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {redirectURI},
		"scope":         {"User.Read"},
	})
}

// authCodeSuccess redeems a minted authorization code, asserting that the token endpoint accepted
// it.
func authCodeSuccess(t *testing.T, srv *Server, code, verifier, redirectURI string) tokenResponse {
	t.Helper()
	status, body := redeemAuthCode(t, srv, code, verifier, redirectURI)
	return tokenSuccess(t, status, body)
}

// authCodeError redeems a minted authorization code, asserting the error response.
func authCodeError(t *testing.T, srv *Server, code, verifier, redirectURI string) errorResponse {
	t.Helper()
	status, body := redeemAuthCode(t, srv, code, verifier, redirectURI)
	return tokenError(t, status, body)
}

// decodeJWTClaims decodes the payload of one of the fake's unsigned JWTs, the way the CLI decodes
// the access token it is handed.
func decodeJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token %q does not have three dot-separated parts", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decoding the payload of %q: %v", token, err)
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("unmarshalling the payload of %q: %v", token, err)
	}
	return claims
}

// redirectListener stands in for the short-lived localhost listener MSAL starts for interactive
// authentication (refs/msal-go/apps/internal/local/server.go:78-115): it records the form_post
// callback the flow helper sends.
type redirectListener struct {
	*httptest.Server

	mu     sync.Mutex
	status int
	form   url.Values
}

// newRedirectListener starts a listener that answers 200 like MSAL's does on success.
func newRedirectListener(t *testing.T) *redirectListener {
	t.Helper()
	l := &redirectListener{status: http.StatusOK}
	l.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		l.mu.Lock()
		l.form = r.PostForm
		status := l.status
		l.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "recorded")
	}))
	t.Cleanup(l.Close)
	return l
}

// Form returns the recorded callback parameters.
func (l *redirectListener) Form() url.Values {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.form
}

// expireAuthCode backdates a minted code so a test can exercise the expiry branch without sleeping.
func expireAuthCode(t *testing.T, srv *Server, code string) {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	ac, ok := srv.authCodes[code]
	if !ok {
		t.Fatalf("no authorization code %q was minted", code)
	}
	ac.expiresAt = time.Now().Add(-time.Minute)
}

// expireDeviceCode backdates a minted device code, covering the expires_in branch without sleeping.
func expireDeviceCode(t *testing.T, srv *Server, deviceCode string) {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	dc, ok := srv.deviceCodes[deviceCode]
	if !ok {
		t.Fatalf("no device code %q was minted", deviceCode)
	}
	dc.expiresAt = time.Now().Add(-time.Minute)
}

// mustContain fails when haystack does not contain needle.
func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("%q does not contain %q", haystack, needle)
	}
}

// errText returns err's message, failing when err is nil.
func errText(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	return err.Error()
}
