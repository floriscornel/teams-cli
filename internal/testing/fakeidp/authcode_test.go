package fakeidp

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
)

// testVerifier is the shape MSAL generates: 32 random bytes base64url-encoded without padding,
// which is 43 characters (refs/msal-go/apps/public/public.go:786-796).
const testVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

// LocalhostURL is the redirect URI form MSAL uses for interactive auth: its listener binds
// localhost, and Entra ignores the port for localhost
// (refs/entra/docs/identity-platform/reply-url.md).
func (l *redirectListener) LocalhostURL() string {
	return strings.Replace(l.URL, "127.0.0.1", "localhost", 1)
}

func TestAuthorizationCodeFlow(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	listener := newRedirectListener(t)
	redirectURI := listener.LocalhostURL()

	authURL := authorizeURL(srv, redirectURI, testVerifier, nil)
	if err := srv.ReturnAuthorizationCode(authURL); err != nil {
		t.Fatalf("ReturnAuthorizationCode: %v", err)
	}

	// The callback MSAL's listener receives is the form_post the fake posts back.
	callback := listener.Form()
	if callback == nil {
		t.Fatal("the redirect URI received nothing")
	}
	if got, want := callback.Get("state"), "test-state"; got != want {
		t.Errorf("state = %q, want %q", got, want)
	}
	code := callback.Get("code")
	if code == "" {
		t.Fatalf("the callback carried no code: %v", callback)
	}
	if callback.Get("error") != "" {
		t.Errorf("the callback carried error %q", callback.Get("error"))
	}

	// The authorization URL MSAL built is captured, so a test can inspect the PKCE parameters.
	req, ok := srv.LastAuthorizationRequest()
	if !ok {
		t.Fatal("LastAuthorizationRequest() reported no request")
	}
	if req.ClientID != testClientID {
		t.Errorf("client_id = %q, want %q", req.ClientID, testClientID)
	}
	if req.RedirectURI != redirectURI {
		t.Errorf("redirect_uri = %q, want %q", req.RedirectURI, redirectURI)
	}
	if req.CodeChallenge != codeChallengeFor(testVerifier) {
		t.Errorf("code_challenge = %q, want the S256 hash of the verifier", req.CodeChallenge)
	}
	if req.CodeChallengeMethod != "S256" {
		t.Errorf("code_challenge_method = %q, want S256", req.CodeChallengeMethod)
	}
	if req.ResponseMode != "form_post" {
		t.Errorf("response_mode = %q, want form_post", req.ResponseMode)
	}
	if req.State != "test-state" {
		t.Errorf("state = %q, want test-state", req.State)
	}
	if !slices.Contains(req.Scopes, "User.Read") {
		t.Errorf("scopes = %v, want it to contain User.Read", req.Scopes)
	}

	// Redeeming the code with the matching verifier gets the tokens.
	token := authCodeSuccess(t, srv, code, testVerifier, redirectURI)
	if token.AccessToken == "" || token.IDToken == "" {
		t.Errorf("the token response is incomplete: %+v", token)
	}
	if got, want := srv.IssuedRefreshTokens(), []string{token.RefreshToken}; !slices.Equal(got, want) {
		t.Errorf("IssuedRefreshTokens() = %v, want %v", got, want)
	}

	// Authorization codes are single use
	// (refs/entra/docs/identity-platform/reference-breaking-changes.md:410).
	e := authCodeError(t, srv, code, testVerifier, redirectURI)
	mustContain(t, e.Description, "AADSTS54005")
}

func TestAuthorizationCodePKCE(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		method        string
		challengeFunc func(verifier string) string
		verifier      string
		wantOK        bool
		wantDesc      string
	}{
		{
			name:          "S256 with the matching verifier",
			method:        "S256",
			challengeFunc: codeChallengeFor,
			verifier:      testVerifier,
			wantOK:        true,
		},
		{
			name:          "S256 with a mismatched verifier",
			method:        "S256",
			challengeFunc: codeChallengeFor,
			verifier:      testVerifier + "-tampered",
			wantDesc:      "PKCE code verifier is invalid",
		},
		{
			name:          "S256 without a verifier",
			method:        "S256",
			challengeFunc: codeChallengeFor,
			verifier:      "",
			wantDesc:      "code_verifier is required",
		},
		{
			// "If excluded, code_challenge is assumed to be plaintext if code_challenge is
			// included" (refs/entra/docs/identity-platform/v2-oauth2-auth-code-flow.md:89).
			name:          "no method means plain",
			method:        "",
			challengeFunc: func(verifier string) string { return verifier },
			verifier:      testVerifier,
			wantOK:        true,
		},
		{
			name:          "plain with the matching verifier",
			method:        "plain",
			challengeFunc: func(verifier string) string { return verifier },
			verifier:      testVerifier,
			wantOK:        true,
		},
		{
			name:          "plain with a mismatched verifier",
			method:        "plain",
			challengeFunc: func(verifier string) string { return verifier },
			verifier:      testVerifier + "-tampered",
			wantDesc:      "PKCE code verifier is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := New(t, Options{})
			listener := newRedirectListener(t)
			redirectURI := listener.LocalhostURL()

			// The client picks the challenge: S256 (BASE64URL(SHA256(verifier))) or plain
			// (refs/entra/docs/identity-platform/v2-oauth2-auth-code-flow.md:88-89).
			authURL := authorizeURL(srv, redirectURI, testVerifier, url.Values{
				"code_challenge":        {test.challengeFunc(testVerifier)},
				"code_challenge_method": {test.method},
			})
			if err := srv.ReturnAuthorizationCode(authURL); err != nil {
				t.Fatalf("ReturnAuthorizationCode: %v", err)
			}
			code := listener.Form().Get("code")

			form := url.Values{
				"grant_type":   {grantTypeAuthCode},
				"client_id":    {testClientID},
				"code":         {code},
				"redirect_uri": {redirectURI},
				"scope":        {"User.Read"},
			}
			if test.verifier != "" {
				form.Set("code_verifier", test.verifier)
			}
			status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().Token, form)

			if test.wantOK {
				tokenSuccess(t, status, body)
				return
			}
			// A bad verifier is invalid_grant
			// (refs/entra/docs/identity-platform/v2-oauth2-auth-code-flow.md:319).
			e := tokenError(t, status, body)
			if e.Error != "invalid_grant" {
				t.Errorf("error = %q, want invalid_grant", e.Error)
			}
			mustContain(t, e.Description, test.wantDesc)
		})
	}
}

func TestAuthorizationCodeRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		mutate   func(t *testing.T, srv *Server, code string)
		redeem   func(t *testing.T, srv *Server, code, redirectURI string) errorResponse
		wantDesc string
	}{
		{
			name: "unknown code",
			redeem: func(t *testing.T, srv *Server, code, redirectURI string) errorResponse {
				return authCodeError(t, srv, "ac-unknown", testVerifier, redirectURI)
			},
			wantDesc: "authorization code is invalid",
		},
		{
			name: "expired code",
			mutate: func(t *testing.T, srv *Server, code string) {
				expireAuthCode(t, srv, code)
			},
			redeem: func(t *testing.T, srv *Server, code, redirectURI string) errorResponse {
				return authCodeError(t, srv, code, testVerifier, redirectURI)
			},
			wantDesc: "authorization code has expired",
		},
		{
			name: "redirect_uri mismatch",
			redeem: func(t *testing.T, srv *Server, code, redirectURI string) errorResponse {
				return authCodeError(t, srv, code, testVerifier, "http://localhost:1")
			},
			wantDesc: "AADSTS50011",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := New(t, Options{})
			listener := newRedirectListener(t)
			redirectURI := listener.LocalhostURL()
			if err := srv.ReturnAuthorizationCode(authorizeURL(srv, redirectURI, testVerifier, nil)); err != nil {
				t.Fatalf("ReturnAuthorizationCode: %v", err)
			}
			code := listener.Form().Get("code")
			if test.mutate != nil {
				test.mutate(t, srv, code)
			}

			e := test.redeem(t, srv, code, redirectURI)
			if e.Error != "invalid_grant" {
				t.Errorf("error = %q, want invalid_grant", e.Error)
			}
			mustContain(t, e.Description, test.wantDesc)
		})
	}
}

func TestAuthorizationCodeInjectedError(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Errors: Errors{AuthCode: AADSTS50020()}})
	listener := newRedirectListener(t)
	redirectURI := listener.LocalhostURL()
	if err := srv.ReturnAuthorizationCode(authorizeURL(srv, redirectURI, testVerifier, nil)); err != nil {
		t.Fatalf("ReturnAuthorizationCode: %v", err)
	}

	e := authCodeError(t, srv, listener.Form().Get("code"), testVerifier, redirectURI)
	mustContain(t, e.Description, "AADSTS50020")
}

func TestReturnAuthorizationCodeErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		override url.Values
		builder  func(t *testing.T, srv *Server) string
		wantErr  string
	}{
		{
			name:     "no code_challenge",
			override: url.Values{"code_challenge": nil},
			wantErr:  "no code_challenge",
		},
		{
			name:     "unsupported code_challenge_method",
			override: url.Values{"code_challenge_method": {"S512"}},
			wantErr:  "unsupported code_challenge_method",
		},
		{
			name:     "no redirect_uri",
			override: url.Values{"redirect_uri": nil},
			wantErr:  "no redirect_uri",
		},
		{
			name:     "response_mode other than form_post",
			override: url.Values{"response_mode": {"query"}},
			wantErr:  "form_post",
		},
		{
			name:    "unparseable authorization URL",
			builder: func(t *testing.T, srv *Server) string { return "https://[::1" },
			wantErr: "parsing the authorization URL",
		},
		{
			name: "redirect URI that refuses connections",
			builder: func(t *testing.T, srv *Server) string {
				closed := httptest.NewServer(http.NotFoundHandler())
				redirectURI := closed.URL
				closed.Close()
				return authorizeURL(srv, redirectURI, testVerifier, nil)
			},
			wantErr: "posting the authorization response",
		},
		{
			name: "redirect URI that fails",
			builder: func(t *testing.T, srv *Server) string {
				listener := newRedirectListener(t)
				listener.mu.Lock()
				listener.status = http.StatusInternalServerError
				listener.mu.Unlock()
				return authorizeURL(srv, listener.LocalhostURL(), testVerifier, nil)
			},
			wantErr: "HTTP 500",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := New(t, Options{})
			authURL := ""
			if test.builder != nil {
				authURL = test.builder(t, srv)
			} else {
				authURL = authorizeURL(srv, newRedirectListener(t).LocalhostURL(), testVerifier, test.override)
			}
			err := srv.ReturnAuthorizationCode(authURL)
			mustContain(t, errText(t, err), test.wantErr)
		})
	}
}

func TestReturnAuthorizationCodeInjectedAuthorizationError(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Errors: Errors{Authorization: AADSTS65001()}})
	listener := newRedirectListener(t)
	if err := srv.ReturnAuthorizationCode(authorizeURL(srv, listener.LocalhostURL(), testVerifier, nil)); err != nil {
		t.Fatalf("ReturnAuthorizationCode: %v", err)
	}
	callback := listener.Form()
	if got, want := callback.Get("error"), "invalid_grant"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	if got := callback.Get("error_subcode"); got != "consent_required" {
		t.Errorf("error_subcode = %q, want consent_required", got)
	}
	mustContain(t, callback.Get("error_description"), "AADSTS65001")
	if got, want := callback.Get("state"), "test-state"; got != want {
		t.Errorf("state = %q, want %q: MSAL rejects a mismatched state", got, want)
	}
}

func TestFakeBrowser(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	listener := newRedirectListener(t)
	openURL := srv.FakeBrowser()
	if err := openURL(authorizeURL(srv, listener.LocalhostURL(), testVerifier, nil)); err != nil {
		t.Fatalf("the browser hook failed: %v", err)
	}
	if listener.Form().Get("code") == "" {
		t.Error("the browser hook minted no code")
	}
}

func TestVerifyPKCE(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		method    string
		challenge string
		verifier  string
		want      bool
	}{
		{"s256 match", "S256", codeChallengeFor(testVerifier), testVerifier, true},
		{"s256 mismatch", "S256", codeChallengeFor(testVerifier), testVerifier + "x", false},
		{"lower-case method", "s256", codeChallengeFor(testVerifier), testVerifier, true},
		{"plain match", "plain", testVerifier, testVerifier, true},
		{"plain mismatch", "plain", testVerifier, testVerifier + "x", false},
		{"empty method means plain", "", testVerifier, testVerifier, true},
		{"unknown method", "S512", codeChallengeFor(testVerifier), testVerifier, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := verifyPKCE(test.method, test.challenge, test.verifier); got != test.want {
				t.Errorf("verifyPKCE(%q, %q, %q) = %v, want %v",
					test.method, test.challenge, test.verifier, got, test.want)
			}
		})
	}
}
