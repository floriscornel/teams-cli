package fakeidp

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestServerSurface(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})

	// The authority must be https with a tenant path segment or MSAL refuses it outright
	// (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:529-536).
	authority, err := url.Parse(srv.Authority())
	if err != nil {
		t.Fatalf("parsing the authority %q: %v", srv.Authority(), err)
	}
	if authority.Scheme != "https" {
		t.Errorf("authority scheme is %q, want https", authority.Scheme)
	}
	if authority.Hostname() != "127.0.0.1" {
		t.Errorf("authority host is %q, want 127.0.0.1", authority.Hostname())
	}
	if authority.Port() == "" {
		t.Error("authority has no port")
	}
	if got := strings.TrimPrefix(authority.Path, "/"); got != DefaultTenant || strings.Contains(got, "/") {
		t.Errorf("authority path is %q, want a single %q segment", authority.Path, DefaultTenant)
	}
	if got, want := srv.Tenant(), DefaultTenant; got != want {
		t.Errorf("Tenant() = %q, want %q", got, want)
	}
	if got, want := srv.TenantID(), DefaultTenantID; got != want {
		t.Errorf("TenantID() = %q, want %q", got, want)
	}
	if got, want := srv.URL(), authority.Scheme+"://"+authority.Host; got != want {
		t.Errorf("URL() = %q, want %q", got, want)
	}
	if srv.HTTPClient() == nil {
		t.Error("HTTPClient() returned nil; MSAL needs a client that trusts the test certificate")
	}

	eps := srv.Endpoints()
	// MSAL derives the device-code endpoint with a "token" -> "devicecode" substitution over the
	// whole token endpoint URL (refs/msal-go/.../accesstokens/accesstokens.go:390). The fake's
	// route layout has to survive that.
	if want := strings.ReplaceAll(eps.Token, "token", "devicecode"); eps.DeviceCode != want {
		t.Errorf("device-code endpoint is %q, want the substitution result %q", eps.DeviceCode, want)
	}
	if want, got := srv.Authority()+"/v2.0/.well-known/openid-configuration", eps.OpenIDConfiguration; got != want {
		t.Errorf("OpenIDConfiguration = %q, want %q", got, want)
	}
	for name, endpoint := range map[string]string{
		"authorization": eps.Authorization,
		"token":         eps.Token,
		"device code":   eps.DeviceCode,
		"jwks":          eps.JWKS,
		"metadata":      eps.OpenIDConfiguration,
	} {
		if !strings.HasPrefix(endpoint, srv.URL()+"/"+DefaultTenant+"/") {
			t.Errorf("%s endpoint %q is not under the tenant path segment", name, endpoint)
		}
	}
	if !strings.HasPrefix(eps.Issuer, srv.URL()+"/") {
		t.Errorf("issuer %q is not on the authority host", eps.Issuer)
	}
}

func TestNewRejectsUnusableOptions(t *testing.T) {
	t.Parallel()
	t.Run("nil testing.T", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("New(nil, ...) did not panic")
			}
		}()
		New(nil, Options{})
	})
	t.Run("tenant containing token", func(t *testing.T) {
		// A tenant named like this would be rewritten in the device-code URL, because MSAL
		// substitutes over the whole URL
		// (refs/msal-go/apps/internal/oauth/ops/accesstokens/accesstokens.go:390).
		defer func() {
			if recover() == nil {
				t.Error("NewServer(Options{Tenant: \"token-tenant\"}) did not panic")
			}
		}()
		NewServer(Options{Tenant: "TOKEN-tenant"})
	})
}

func TestNewServerCanBeClosedDirectly(t *testing.T) {
	t.Parallel()
	srv := NewServer(Options{})
	before := srv.URL()
	srv.Close()
	// The listener is gone: the client cannot reach the URL any more.
	// A context is required by the noctx linter even in a test helper.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, before, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.HTTPClient().Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Error("the server still answered after Close")
	}
}

// openIDConfiguration is the subset of the metadata document the tests pin down.
type openIDConfiguration struct {
	Issuer                           string   `json:"issuer"`
	AuthorizationEndpoint            string   `json:"authorization_endpoint"`
	TokenEndpoint                    string   `json:"token_endpoint"`
	DeviceAuthorizationEndpoint      string   `json:"device_authorization_endpoint"`
	JWKSURI                          string   `json:"jwks_uri"`
	ScopesSupported                  []string `json:"scopes_supported"`
	GrantTypesSupported              []string `json:"grant_types_supported"`
	IDTokenSigningAlgValuesSupported []string `json:"id_token_signing_alg_values_supported"`
}

func TestOpenIDConfiguration(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Scopes: []string{"User.Read", "Chat.Read"}, Tenant: "Example.ONMICROSOFT.com", TenantID: "aaaa-bbbb"})

	status, body := get(t, srv.HTTPClient(), srv.Endpoints().OpenIDConfiguration)
	if status != http.StatusOK {
		t.Fatalf("metadata document answered HTTP %d: %s", status, body)
	}
	doc := decode[openIDConfiguration](t, body)

	// MSAL requires authorization_endpoint, token_endpoint and issuer, and it compares the issuer
	// host against the authority host (refs/msal-go/.../authority/authority.go:104-162).
	eps := srv.Endpoints()
	for name, got := range map[string]string{
		"authorization_endpoint": doc.AuthorizationEndpoint,
		"token_endpoint":         doc.TokenEndpoint,
		"jwks_uri":               doc.JWKSURI,
	} {
		want := map[string]string{
			"authorization_endpoint": eps.Authorization,
			"token_endpoint":         eps.Token,
			"jwks_uri":               eps.JWKS,
		}[name]
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if doc.Issuer != eps.Issuer {
		t.Errorf("issuer = %q, want %q", doc.Issuer, eps.Issuer)
	}
	issuer, err := url.Parse(doc.Issuer)
	if err != nil {
		t.Fatalf("parsing the issuer %q: %v", doc.Issuer, err)
	}
	authority, err := url.Parse(srv.Authority())
	if err != nil {
		t.Fatalf("parsing the authority %q: %v", srv.Authority(), err)
	}
	if issuer.Scheme != authority.Scheme || !strings.EqualFold(issuer.Host, authority.Host) {
		t.Errorf("issuer host %q does not match authority host %q", issuer.Host, authority.Host)
	}
	if doc.DeviceAuthorizationEndpoint != strings.ReplaceAll(doc.TokenEndpoint, "token", "devicecode") {
		t.Errorf("device_authorization_endpoint %q is not the substitution of %q",
			doc.DeviceAuthorizationEndpoint, doc.TokenEndpoint)
	}
	for _, scope := range []string{"User.Read", "Chat.Read"} {
		if !slices.Contains(doc.ScopesSupported, scope) {
			t.Errorf("scopes_supported %v does not list %q", doc.ScopesSupported, scope)
		}
	}
	if !slices.Contains(doc.GrantTypesSupported, grantTypeDeviceCode) {
		t.Errorf("grant_types_supported %v does not list the device-code grant", doc.GrantTypesSupported)
	}
	// The fake mints unsigned tokens, so it advertises that instead of a signing algorithm it
	// cannot produce (see the package comment).
	if !slices.Contains(doc.IDTokenSigningAlgValuesSupported, "none") {
		t.Errorf("id_token_signing_alg_values_supported = %v, want it to advertise none",
			doc.IDTokenSigningAlgValuesSupported)
	}

	// The tenant is lowercased because MSAL lowercases the whole authority
	// (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:524).
	if got, want := srv.Tenant(), "example.onmicrosoft.com"; got != want {
		t.Errorf("Tenant() = %q, want %q", got, want)
	}
	if !strings.Contains(doc.Issuer, "aaaa-bbbb") {
		t.Errorf("issuer %q does not use the configured tenant ID", doc.Issuer)
	}
}

func TestJWKS(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	status, body := get(t, srv.HTTPClient(), srv.Endpoints().JWKS)
	if status != http.StatusOK {
		t.Fatalf("jwks_uri answered HTTP %d: %s", status, body)
	}
	var doc struct {
		Keys []any `json:"keys"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the JWKS document is not JSON: %v", err)
	}
	if len(doc.Keys) != 0 {
		t.Errorf("the JWKS document published %d keys, want none: the fake signs nothing", len(doc.Keys))
	}
}

func TestRouting(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	tests := []struct {
		name       string
		method     string
		endpoint   string
		wantStatus int
		wantError  string
	}{
		{"unknown path", http.MethodGet, "/nope", http.StatusNotFound, "invalid_request"},
		{"token endpoint rejects GET", http.MethodGet, srv.Endpoints().Token, http.StatusMethodNotAllowed, "invalid_request"},
		{"device-code endpoint rejects GET", http.MethodGet, srv.Endpoints().DeviceCode, http.StatusMethodNotAllowed, "invalid_request"},
		{"metadata document rejects POST", http.MethodPost, srv.Endpoints().OpenIDConfiguration, http.StatusMethodNotAllowed, "invalid_request"},
		{"jwks rejects POST", http.MethodPost, srv.Endpoints().JWKS, http.StatusMethodNotAllowed, "invalid_request"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint := test.endpoint
			if !strings.HasPrefix(endpoint, "http") {
				endpoint = srv.URL() + endpoint
			}
			resp := doRaw(t, srv.HTTPClient(), test.method, endpoint, "") //nolint:bodyclose // doRaw drained and closed the body
			if resp.StatusCode != test.wantStatus {
				t.Errorf("%s %s answered HTTP %d, want %d", test.method, endpoint, resp.StatusCode, test.wantStatus)
			}
			var e errorResponse
			if err := json.Unmarshal(readAll(t, resp), &e); err != nil {
				t.Fatalf("decoding the error body: %v", err)
			}
			if e.Error != test.wantError {
				t.Errorf("error = %q, want %q", e.Error, test.wantError)
			}
			if e.Correlation == "" {
				t.Error("the error body has no correlation_id")
			}
		})
	}
}

func TestMalformedRequestBody(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	for name, endpoint := range map[string]string{
		"token endpoint":       srv.Endpoints().Token,
		"device-code endpoint": srv.Endpoints().DeviceCode,
	} {
		t.Run(name, func(t *testing.T) {
			resp := doRaw(t, srv.HTTPClient(), http.MethodPost, endpoint, "grant_type=%zz") //nolint:bodyclose // doRaw drained and closed the body
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("a malformed form body answered HTTP %d, want 400", resp.StatusCode)
			}
			e := decode[errorResponse](t, readAll(t, resp))
			if e.Error != "invalid_request" {
				t.Errorf("error = %q, want invalid_request", e.Error)
			}
		})
	}
}

func TestUnsupportedGrantType(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().Token, url.Values{
		"grant_type": {"password"},
		"client_id":  {testClientID},
	})
	e := tokenError(t, status, body)
	if e.Error != "unsupported_grant_type" {
		t.Errorf("error = %q, want unsupported_grant_type", e.Error)
	}
	mustContain(t, e.Description, "password")
}

func TestClientIDPinning(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{ClientID: testClientID})

	t.Run("matching client id is accepted", func(t *testing.T) {
		requestDeviceCode(t, srv)
	})
	t.Run("token endpoint rejects another client", func(t *testing.T) {
		status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().Token, url.Values{
			"grant_type": {"refresh_token"},
			"client_id":  {"someone-else"},
		})
		e := tokenError(t, status, body)
		if e.Error != "invalid_client" {
			t.Errorf("error = %q, want invalid_client", e.Error)
		}
		mustContain(t, e.Description, "someone-else")
	})
	t.Run("device-code endpoint rejects another client", func(t *testing.T) {
		status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().DeviceCode, url.Values{
			"client_id": {"someone-else"},
			"scope":     {"User.Read"},
		})
		e := tokenError(t, status, body)
		if e.Error != "invalid_client" {
			t.Errorf("error = %q, want invalid_client", e.Error)
		}
	})
}

func TestDeviceCodeEndpoint(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{DeviceCodeExpiresIn: 600, DeviceCodeInterval: 7})

	status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().DeviceCode, url.Values{
		"client_id": {testClientID},
		"scope":     {"User.Read"},
	})
	if status != http.StatusOK {
		t.Fatalf("device-code endpoint answered HTTP %d: %s", status, body)
	}
	dc := decode[deviceCodeResponse](t, body)

	// The fields the device-code flow documents
	// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:53-60), which MSAL reads into
	// DeviceCodeResult and hands to the caller verbatim
	// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:378-395).
	if dc.UserCode != DefaultUserCode {
		t.Errorf("user_code = %q, want %q", dc.UserCode, DefaultUserCode)
	}
	if dc.DeviceCode == "" {
		t.Error("device_code is empty")
	}
	if dc.VerificationURI != DefaultVerificationURI {
		t.Errorf("verification_uri = %q, want %q", dc.VerificationURI, DefaultVerificationURI)
	}
	if dc.ExpiresIn != 600 {
		t.Errorf("expires_in = %d, want 600", dc.ExpiresIn)
	}
	if dc.Interval != 7 {
		t.Errorf("interval = %d, want 7", dc.Interval)
	}
	// MSAL passes the message through verbatim
	// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:378-395).
	mustContain(t, dc.Message, DefaultUserCode)
	mustContain(t, dc.Message, DefaultVerificationURI)

	// verification_uri_complete is deliberately absent: the real service does not support it
	// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:62-63).
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["verification_uri_complete"]; ok {
		t.Error("the response carries verification_uri_complete, which the real service does not return")
	}

	if got, want := srv.DeviceAuthorizationRequests(), 1; got != want {
		t.Errorf("DeviceAuthorizationRequests() = %d, want %d", got, want)
	}
	// Two codes are never the same token.
	if second := requestDeviceCode(t, srv); second.DeviceCode == dc.DeviceCode {
		t.Errorf("two device codes are identical: %q", second.DeviceCode)
	}
	if got, want := srv.DeviceAuthorizationRequests(), 2; got != want {
		t.Errorf("DeviceAuthorizationRequests() = %d, want %d", got, want)
	}
}

func TestDeviceCodeGrantPolling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{"default is two pending polls", Options{}, []string{"authorization_pending", "authorization_pending", ""}},
		{"one pending poll", Options{DeviceCodePollCount: 1}, []string{"authorization_pending", ""}},
		{"slow_down follows the pending polls", Options{DeviceCodePollCount: 1, SlowDownPolls: 2}, []string{"authorization_pending", "slow_down", "slow_down", ""}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := New(t, test.opts)
			dc := requestDeviceCode(t, srv)

			for i, want := range test.want {
				status, body := pollDeviceCode(t, srv, dc.DeviceCode)
				if want == "" {
					token := tokenSuccess(t, status, body)
					if token.RefreshToken == "" {
						t.Error("a successful device-code grant returned no refresh token")
					}
					if got, want := srv.DeviceCodePolls(), len(test.want); got != want {
						t.Errorf("DeviceCodePolls() = %d, want %d", got, want)
					}
					return
				}
				e := tokenError(t, status, body)
				if e.Error != want {
					t.Fatalf("poll %d: error = %q, want %q (%s)", i+1, e.Error, want, e.Description)
				}
				// Only authorization_pending and slow_down are retried by MSAL, so the fake must
				// spell them exactly like this
				// (refs/msal-go/apps/internal/oauth/oauth.go:302-328).
				if e.Description == "" {
					t.Errorf("poll %d: %q carried no error_description", i+1, want)
				}
			}
			t.Fatalf("the grant never succeeded within %d polls", len(test.want))
		})
	}
}

func TestDeviceCodeGrantFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		opts     Options
		mutate   func(t *testing.T, srv *Server, dc *deviceCodeResponse)
		want     []string
		wantDesc string
	}{
		{
			name: "unknown device code",
			mutate: func(_ *testing.T, _ *Server, dc *deviceCodeResponse) {
				dc.DeviceCode = "dc-does-not-exist"
			},
			want: []string{"bad_verification_code"},
		},
		{
			name: "expired device code",
			mutate: func(t *testing.T, srv *Server, dc *deviceCodeResponse) {
				expireDeviceCode(t, srv, dc.DeviceCode)
			},
			want: []string{"expired_token"},
		},
		{
			name: "device code is single use",
			opts: Options{DeviceCodePollCount: 1},
			// The first poll is pending, the second succeeds, and the third finds the code spent.
			want: []string{"authorization_pending", "ok", "expired_token"},
		},
		{
			name: "injected expired_token on the first poll",
			opts: Options{Errors: Errors{DeviceCode: ExpiredDeviceCode()}},
			want: []string{"expired_token"},
		},
		{
			name: "injected authorization_declined after one pending poll",
			// MSAL stops polling on anything that is not authorization_pending or slow_down, so
			// authorization_declined is terminal
			// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:94).
			opts:     Options{Errors: Errors{DeviceCode: AuthorizationDeclined(), DeviceCodeAfterPolls: 1}},
			want:     []string{"authorization_pending", "authorization_declined"},
			wantDesc: "denied the authorization request",
		},
		{
			name:     "injected AADSTS65001",
			opts:     Options{Errors: Errors{DeviceCode: AADSTS65001()}},
			want:     []string{"invalid_grant"},
			wantDesc: "AADSTS65001",
		},
		{
			name:     "custom injected error",
			opts:     Options{Errors: Errors{DeviceCode: NewError("interaction_required", "AADSTS50079: Due to a configuration change made by your administrator, you must enroll in multi-factor authentication.")}},
			want:     []string{"interaction_required"},
			wantDesc: "AADSTS50079",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := New(t, test.opts)
			dc := requestDeviceCode(t, srv)
			if test.mutate != nil {
				test.mutate(t, srv, &dc)
			}
			for i, want := range test.want {
				status, body := pollDeviceCode(t, srv, dc.DeviceCode)
				if want == "ok" {
					if status != http.StatusOK {
						t.Fatalf("poll %d answered HTTP %d, want 200: %s", i+1, status, body)
					}
					continue
				}
				e := tokenError(t, status, body)
				if e.Error != want {
					t.Fatalf("poll %d: error = %q, want %q (%s)", i+1, e.Error, want, e.Description)
				}
				if test.wantDesc != "" && i == len(test.want)-1 {
					mustContain(t, e.Description, test.wantDesc)
				}
			}
			if got, want := srv.DeviceCodePolls(), len(test.want); got != want {
				t.Errorf("DeviceCodePolls() = %d, want %d", got, want)
			}
		})
	}
}

func TestDeviceAuthorizationInjectedError(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Errors: Errors{DeviceAuthorization: AADSTS50020()}})
	status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().DeviceCode, url.Values{
		"client_id": {testClientID},
		"scope":     {"User.Read"},
	})
	e := tokenError(t, status, body)
	mustContain(t, e.Description, "AADSTS50020")
	if got, want := srv.DeviceAuthorizationRequests(), 0; got != want {
		t.Errorf("DeviceAuthorizationRequests() = %d, want %d: no code was issued", got, want)
	}
}

func TestRefreshTokenRotation(t *testing.T) {
	t.Parallel()
	t.Run("every redemption rotates and invalidates", func(t *testing.T) {
		srv := New(t, Options{})
		first := deviceCodeGrantExpectSuccess(t, srv)

		if got, want := srv.IssuedRefreshTokens(), []string{first.RefreshToken}; !slices.Equal(got, want) {
			t.Fatalf("IssuedRefreshTokens() = %v, want %v", got, want)
		}

		second := redeemRefreshTokenExpectSuccess(t, srv, first.RefreshToken)
		if second.RefreshToken == first.RefreshToken {
			t.Error("the redemption returned the same refresh token, want a rotated one")
		}
		if got, want := srv.ConsumedRefreshTokens(), []string{first.RefreshToken}; !slices.Equal(got, want) {
			t.Errorf("ConsumedRefreshTokens() = %v, want %v", got, want)
		}
		if got, want := srv.IssuedRefreshTokens(), []string{first.RefreshToken, second.RefreshToken}; !slices.Equal(got, want) {
			t.Errorf("IssuedRefreshTokens() = %v, want %v", got, want)
		}
		if got, want := srv.ActiveRefreshTokens(), []string{second.RefreshToken}; !slices.Equal(got, want) {
			t.Errorf("ActiveRefreshTokens() = %v, want %v", got, want)
		}

		// Reusing the consumed token has to fail, which is what proves a client wrote the
		// rotated token back to its cache.
		e := redeemRefreshTokenExpectError(t, srv, first.RefreshToken)
		if e.Error != "invalid_grant" {
			t.Errorf("reusing a consumed refresh token gave %q, want invalid_grant", e.Error)
		}
		mustContain(t, e.Description, "AADSTS70008")
		if got, want := srv.ConsumedRefreshTokens(), []string{first.RefreshToken}; !slices.Equal(got, want) {
			t.Errorf("a rejected redemption changed ConsumedRefreshTokens() to %v, want %v", got, want)
		}
	})

	t.Run("missing and unknown refresh tokens", func(t *testing.T) {
		srv := New(t, Options{})
		for _, presented := range []string{"", "rt-nope"} {
			status, body := redeemRefreshToken(t, srv, presented)
			e := tokenError(t, status, body)
			if e.Error != "invalid_grant" {
				t.Errorf("refresh_token %q gave %q, want invalid_grant", presented, e.Error)
			}
		}
		if got := srv.ConsumedRefreshTokens(); len(got) != 0 {
			t.Errorf("ConsumedRefreshTokens() = %v, want none", got)
		}
	})

	t.Run("an injected error leaves the token redeemable", func(t *testing.T) {
		srv := New(t, Options{})
		token := deviceCodeGrantExpectSuccess(t, srv)

		srv.mu.Lock()
		srv.opts.Errors.Refresh = AADSTS65001()
		srv.mu.Unlock()

		e := redeemRefreshTokenExpectError(t, srv, token.RefreshToken)
		mustContain(t, e.Description, "AADSTS65001")
		if e.Suberror != "consent_required" {
			t.Errorf("suberror = %q, want consent_required", e.Suberror)
		}
		if got := srv.ConsumedRefreshTokens(); len(got) != 0 {
			t.Errorf("ConsumedRefreshTokens() = %v, want none: the request failed", got)
		}

		srv.mu.Lock()
		srv.opts.Errors.Refresh = nil
		srv.mu.Unlock()
		redeemRefreshTokenExpectSuccess(t, srv, token.RefreshToken)
	})
}

func TestTokenResponseShape(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{ExpiresIn: 900, Scopes: []string{"User.Read", "Chat.Read"}})
	token := deviceCodeGrantExpectSuccess(t, srv)

	if token.TokenType != "Bearer" {
		t.Errorf("token_type = %q, want Bearer", token.TokenType)
	}
	// MSAL declares a response without expires_in (or expires_on) invalid
	// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:238-245).
	if token.ExpiresIn != 900 {
		t.Errorf("expires_in = %d, want 900", token.ExpiresIn)
	}
	if token.Scope == "" {
		t.Error("scope is empty")
	}
	if token.IDToken == "" {
		t.Error("id_token is empty; MSAL builds the account from it")
	}
	if token.ClientInfo == "" {
		t.Error("client_info is empty; MSAL builds the HomeAccountID from it")
	}
}

// TestTokenResponseWithoutScope covers the fallback the fake uses when a client sends no scope:
// the response reports the consented set, so MSAL still computes the granted scopes and finds
// nothing declined (refs/msal-go/.../accesstokens/tokens.go:249-259).
func TestTokenResponseWithoutScope(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Scopes: []string{"User.Read", "Chat.Read"}, DeviceCodePollCount: 1})
	dc := requestDeviceCode(t, srv)
	for i := range 3 {
		status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().Token, url.Values{
			"grant_type":  {grantTypeDeviceCodeShort},
			"client_id":   {testClientID},
			"device_code": {dc.DeviceCode},
		})
		if status == http.StatusOK {
			token := decode[tokenResponse](t, body)
			if got, want := token.Scope, "User.Read Chat.Read"; got != want {
				t.Errorf("scope = %q, want %q", got, want)
			}
			return
		}
		if e := tokenError(t, status, body); e.Error != "authorization_pending" {
			t.Fatalf("poll %d gave %q, want authorization_pending", i+1, e.Error)
		}
	}
	t.Fatal("the device-code grant did not settle within three polls")
}

func TestErrorConstructors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		injected *Error
		wantCode string
		wantDesc string
	}{
		{"invalid_grant", InvalidGrant(), "invalid_grant", "grant is invalid"},
		{"AADSTS50020", AADSTS50020(), "invalid_grant", "AADSTS50020"},
		{"AADSTS65001", AADSTS65001(), "invalid_grant", "AADSTS65001"},
		{"expired device code", ExpiredDeviceCode(), "expired_token", "expires_in has been exceeded"},
		{"authorization declined", AuthorizationDeclined(), "authorization_declined", "denied the authorization request"},
		{
			name:     "custom",
			injected: NewError("interaction_required", "AADSTS50079: you must enroll in multi-factor authentication."),
			wantCode: "interaction_required",
			wantDesc: "AADSTS50079",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The zero status means 400, which is what MSAL's retry logic requires for the
			// device-code polls (refs/msal-go/apps/internal/oauth/oauth.go:302-328).
			if test.injected.Status != 0 {
				t.Errorf("Status = %d, want 0 so it defaults to HTTP 400", test.injected.Status)
			}
			if test.injected.Code != test.wantCode {
				t.Errorf("Code = %q, want %q", test.injected.Code, test.wantCode)
			}
			mustContain(t, test.injected.Description, test.wantDesc)
			// Error() is what a test or a caller logging an injected error sees.
			mustContain(t, test.injected.Error(), test.wantCode)
			mustContain(t, test.injected.Error(), test.wantDesc)
		})
	}
	if got, want := AADSTS65001().Codes, []int{65001}; !slices.Equal(got, want) {
		t.Errorf("AADSTS65001().Codes = %v, want %v: the real body carries error_codes", got, want)
	}
	if got, want := AADSTS65001().Suberror, "consent_required"; got != want {
		t.Errorf("AADSTS65001().Suberror = %q, want %q", got, want)
	}
	// An injected error without a description still satisfies error, because a caller logging one
	// should not print a stray colon.
	if got, want := NewError("authorization_pending", "").Error(), "authorization_pending"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// TestLastAuthorizationRequestBeforeAnyFlow covers the "nothing has happened yet" answer a test
// gets when it inspects the server before driving a flow.
func TestLastAuthorizationRequestBeforeAnyFlow(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	if got, ok := srv.LastAuthorizationRequest(); ok {
		t.Errorf("a fresh server reported an authorization request: %+v", got)
	}
}

func TestAccessTokenClaims(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		opts     Options
		claims   string
		wantSCP  string
		wantXCMS bool
		wantAud  string
	}{
		{
			name:    "consented scopes become scp without the reserved OIDC scopes",
			opts:    Options{Scopes: []string{"User.Read", "Chat.Read", "openid", "profile", "offline_access", "email"}},
			wantSCP: "User.Read Chat.Read",
			wantAud: DefaultAudience,
		},
		{
			name:     "the cp1 client capability is echoed in xms_cc",
			claims:   `{"access_token":{"xms_cc":{"values":["cp1"]}}}`,
			wantSCP:  DefaultScope,
			wantXCMS: true,
			wantAud:  DefaultAudience,
		},
		{
			name:    "a claims challenge without xms_cc is not a capability",
			claims:  `{"access_token":{"acrs":{"essential":true,"value":"c25"}}}`,
			wantSCP: DefaultScope,
			wantAud: DefaultAudience,
		},
		{
			name:    "unparseable claims are ignored",
			claims:  "not json",
			wantSCP: DefaultScope,
			wantAud: DefaultAudience,
		},
		{
			name:    "the audience is configurable",
			opts:    Options{Audience: "https://graph.microsoft.com"},
			wantSCP: DefaultScope,
			wantAud: "https://graph.microsoft.com",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := New(t, test.opts)
			token := deviceCodeGrantExpectSuccess(t, srv, test.claims)
			claims := decodeJWTClaims(t, token.AccessToken)

			if got := claims["scp"]; got != test.wantSCP {
				t.Errorf("scp = %v, want %q", got, test.wantSCP)
			}
			if got := claims["aud"]; got != test.wantAud {
				t.Errorf("aud = %v, want %q", got, test.wantAud)
			}
			_, hasXCMS := claims["xms_cc"]
			if hasXCMS != test.wantXCMS {
				t.Errorf("xms_cc present = %v, want %v (%v)", hasXCMS, test.wantXCMS, claims["xms_cc"])
			}
			if test.wantXCMS {
				values, ok := claims["xms_cc"].([]any)
				if !ok || len(values) != 1 || values[0] != "cp1" {
					t.Errorf("xms_cc = %v, want [cp1]", claims["xms_cc"])
				}
			}
			// The remaining claims the CLI reads off a Graph token.
			for claim, want := range map[string]string{
				"iss":   srv.Endpoints().Issuer,
				"tid":   srv.TenantID(),
				"sub":   "33333333-3333-3333-3333-333333333333",
				"oid":   "22222222-2222-2222-2222-222222222222",
				"appid": testClientID,
				"ver":   "2.0",
			} {
				if got := claims[claim]; got != want {
					t.Errorf("%s = %v, want %q", claim, got, want)
				}
			}
			exp, ok := claims["exp"].(float64)
			if !ok {
				t.Fatalf("exp = %v, want a number", claims["exp"])
			}
			if exp <= float64(time.Now().Unix()) {
				t.Errorf("exp = %v is not in the future", exp)
			}
		})
	}
}

func TestIdentityClaims(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Account: Account{
		PreferredUsername: "bot@contoso.com",
		Name:              "Service Bot",
		ObjectID:          "aaaa-1",
		Subject:           "bbbb-2",
	}})
	token := deviceCodeGrantExpectSuccess(t, srv)

	idClaims := decodeJWTClaims(t, token.IDToken)
	for claim, want := range map[string]string{
		"preferred_username": "bot@contoso.com",
		"name":               "Service Bot",
		"oid":                "aaaa-1",
		"sub":                "bbbb-2",
		"tid":                srv.TenantID(),
		"aud":                testClientID,
		"iss":                srv.Endpoints().Issuer,
	} {
		if got := idClaims[claim]; got != want {
			t.Errorf("ID token %s = %v, want %q", claim, got, want)
		}
	}

	// client_info is base64url-encoded JSON; MSAL joins uid and utid into the account's
	// HomeAccountID (refs/msal-go/.../accesstokens/tokens.go:120-148,262-271).
	raw, err := decodeBase64URL(token.ClientInfo)
	if err != nil {
		t.Fatalf("client_info %q is not base64url: %v", token.ClientInfo, err)
	}
	var info struct {
		UID  string `json:"uid"`
		UTID string `json:"utid"`
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		t.Fatalf("client_info did not decode to JSON: %v (%s)", err, raw)
	}
	// The UID defaults to the object ID and the UTID to the tenant ID.
	if info.UID != "aaaa-1" {
		t.Errorf("client_info uid = %q, want the account object ID", info.UID)
	}
	if info.UTID != srv.TenantID() {
		t.Errorf("client_info utid = %q, want %q", info.UTID, srv.TenantID())
	}
}

// TestDeviceCodeGrantSpellings pins down a divergence between the protocol and MSAL Go: the docs
// mandate urn:ietf:params:oauth:grant-type:device_code
// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:83) while MSAL sends the short
// "device_code" (refs/msal-go/apps/internal/oauth/ops/internal/grant/grant.go:22). The fake accepts
// both, so a client written against the documentation and MSAL's own client both work against it.
func TestDeviceCodeGrantSpellings(t *testing.T) {
	t.Parallel()
	for _, grantType := range []string{grantTypeDeviceCode, grantTypeDeviceCodeShort} {
		t.Run(grantType, func(t *testing.T) {
			srv := New(t, Options{DeviceCodePollCount: 1})
			dc := requestDeviceCode(t, srv)
			for i := range 2 {
				status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().Token, url.Values{
					"grant_type":  {grantType},
					"client_id":   {testClientID},
					"device_code": {dc.DeviceCode},
					"scope":       {"User.Read"},
				})
				if i == 0 {
					e := tokenError(t, status, body)
					if e.Error != "authorization_pending" {
						t.Fatalf("first poll gave %q, want authorization_pending", e.Error)
					}
					continue
				}
				tokenSuccess(t, status, body)
			}
		})
	}
}

func TestEveryTokenIsUnique(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	first := deviceCodeGrantExpectSuccess(t, srv)
	second := deviceCodeGrantExpectSuccess(t, srv)

	if first.AccessToken == second.AccessToken {
		t.Error("two device-code flows minted the same access token")
	}
	if first.IDToken == second.IDToken {
		t.Error("two device-code flows minted the same ID token")
	}
	for name, token := range map[string]string{"access token": first.AccessToken, "ID token": first.IDToken} {
		claims := decodeJWTClaims(t, token)
		// uti is Entra's per-token identifier, equivalent to jti
		// (refs/entra/docs/identity-platform/access-token-claims-reference.md:70).
		if claims["uti"] == nil || claims["uti"] == "" {
			t.Errorf("the %s has no uti claim", name)
		}
	}
}

func TestTokenRequestCounter(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{})
	if got := srv.TokenRequests(); got != 0 {
		t.Fatalf("TokenRequests() = %d, want 0", got)
	}
	requestDeviceCode(t, srv)
	if got := srv.TokenRequests(); got != 0 {
		t.Errorf("TokenRequests() = %d after a device-code request, want 0", got)
	}
	dc := requestDeviceCode(t, srv)
	pollDeviceCode(t, srv, dc.DeviceCode)
	if got := srv.TokenRequests(); got != 1 {
		t.Errorf("TokenRequests() = %d, want 1", got)
	}
}

// deviceCodeGrantExpectSuccess runs a whole device-code flow and asserts the token response.
func deviceCodeGrantExpectSuccess(t *testing.T, srv *Server, claims ...string) tokenResponse {
	t.Helper()
	status, body := deviceCodeGrant(t, srv, claims...)
	return tokenSuccess(t, status, body)
}

// redeemRefreshTokenExpectSuccess redeems a refresh token, asserting that it succeeded.
func redeemRefreshTokenExpectSuccess(t *testing.T, srv *Server, refreshToken string) tokenResponse {
	t.Helper()
	status, body := redeemRefreshToken(t, srv, refreshToken)
	return tokenSuccess(t, status, body)
}

// redeemRefreshTokenExpectError redeems a refresh token, asserting the error response.
func redeemRefreshTokenExpectError(t *testing.T, srv *Server, refreshToken string) errorResponse {
	t.Helper()
	status, body := redeemRefreshToken(t, srv, refreshToken)
	return tokenError(t, status, body)
}

// deviceCodeGrant runs a whole device-code flow and returns the raw status and body of the token
// response: the first poll that is not retryable, which is the successful response when the flow
// completes.
func deviceCodeGrant(t *testing.T, srv *Server, claims ...string) (int, []byte) {
	t.Helper()
	dc := requestDeviceCode(t, srv)
	form := url.Values{
		"grant_type":  {grantTypeDeviceCode},
		"client_id":   {testClientID},
		"device_code": {dc.DeviceCode},
		"scope":       {"User.Read Chat.Read"},
	}
	if len(claims) > 0 && claims[0] != "" {
		form.Set("claims", claims[0])
	}
	for range 8 {
		status, body := postForm(t, srv.HTTPClient(), srv.Endpoints().Token, form)
		if status == http.StatusOK {
			return status, body
		}
		e := tokenError(t, status, body)
		if e.Error != "authorization_pending" && e.Error != "slow_down" {
			return status, body
		}
	}
	t.Fatal("the device-code grant did not settle within 8 polls")
	return 0, nil
}
