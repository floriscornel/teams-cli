package fakeidp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"testing"

	"github.com/AzureAD/microsoft-authentication-extensions-for-go/cache"
	"github.com/AzureAD/microsoft-authentication-extensions-for-go/cache/accessor/file"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"
)

// msalScopes is what the tests ask for: the scopes the fake consents to (Options.Scopes).
var msalScopes = []string{"User.Read", "Chat.Read"}

// newMSALClient builds the client the CLI builds: a public client pointed at the fake, with
// instance discovery disabled and an on-disk cache.
func newMSALClient(t *testing.T, srv *Server) public.Client {
	t.Helper()
	// cache.New's second argument is the path of a timestamp file; the lock file is that path
	// plus ".lockfile" (refs/msal-ext/cache/cache.go:52-58). The accessor owns the cache blob.
	dir := t.TempDir()
	accessor, err := file.New(filepath.Join(dir, "cache.json"))
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}
	store, err := cache.New(accessor, filepath.Join(dir, "timestamp"))
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}

	options := []public.Option{
		// MSAL rejects an authority that is not https or has no tenant path segment
		// (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:529-536).
		public.WithAuthority(srv.Authority()),
		// WithHTTPClient takes the interface *http.Client satisfies
		// (refs/msal-go/apps/internal/oauth/ops/internal/comm/comm.go:26-32), so the test server's
		// client, which trusts its certificate, works here.
		public.WithHTTPClient(srv.HTTPClient()),
		public.WithInstanceDiscovery(false),
		// The CLI declares the cp1 capability so it can act on CAE claims challenges (PLAN.md,
		// service-account bot flow).
		public.WithClientCapabilities([]string{"cp1"}),
		public.WithCache(store),
	}
	client, err := public.New(testClientID, options...)
	if err != nil {
		t.Fatalf("public.New: %v", err)
	}
	return client
}

// claimsChallenge is a CAE claims challenge, the shape a Graph 401 carries in WWW-Authenticate.
// Passing it to an acquire makes MSAL ignore the cached access token
// (refs/msal-go/apps/internal/base/base.go:398-399) and redeem the refresh token instead.
const claimsChallenge = `{"access_token":{"acrs":{"essential":true,"value":"c25"}}}`

// TestMSALDeviceCodeFlow drives a real MSAL public client through the fake: the device-code
// sign-in, a silent acquisition served from the cache, and a claims-forced refresh that rotates
// the refresh token.
func TestMSALDeviceCodeFlow(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{
		Scopes:  msalScopes,
		Account: Account{PreferredUsername: "bot@contoso.com", Name: "Teams Bot"},
	})
	client := newMSALClient(t, srv)
	ctx := context.Background()

	var acquired public.AuthResult

	t.Run("device code flow", func(t *testing.T) {
		dc, err := client.AcquireTokenByDeviceCode(ctx, msalScopes)
		if err != nil {
			t.Fatalf("AcquireTokenByDeviceCode: %v", err)
		}
		// MSAL passes the device-code message through verbatim
		// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:378-395).
		mustContain(t, dc.Result.Message, DefaultUserCode)
		if dc.Result.Interval != DefaultDeviceCodeInterval {
			t.Errorf("device code interval = %d, want %d", dc.Result.Interval, DefaultDeviceCodeInterval)
		}

		acquired, err = dc.AuthenticationResult(ctx)
		if err != nil {
			t.Fatalf("AuthenticationResult: %v", err)
		}
		if got, want := acquired.Account.PreferredUsername, "bot@contoso.com"; got != want {
			t.Errorf("PreferredUsername = %q, want %q", got, want)
		}
		// The HomeAccountID is the client_info pair, so the fake's client_info field reached MSAL
		// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:262-271).
		if got, want := acquired.Account.HomeAccountID, DefaultAccountObjectID+"."+DefaultTenantID; got != want {
			t.Errorf("HomeAccountID = %q, want %q", got, want)
		}
		if !slices.Contains(acquired.GrantedScopes, "User.Read") {
			t.Errorf("GrantedScopes = %v, want it to contain User.Read", acquired.GrantedScopes)
		}

		// The access token is a real JWT whose scp is the consented scope set
		// (refs/entra/docs/identity-platform/access-token-claims-reference.md:59).
		claims := decodeJWTClaims(t, acquired.AccessToken)
		if got, want := claims["scp"], "User.Read Chat.Read"; got != want {
			t.Errorf("scp = %v, want %q", got, want)
		}
		if got, want := claims["aud"], DefaultAudience; got != want {
			t.Errorf("aud = %v, want %q", got, want)
		}
		if got, want := claims["tid"], DefaultTenantID; got != want {
			t.Errorf("tid = %v, want %q", got, want)
		}
		if got, want := claims["appid"], testClientID; got != want {
			t.Errorf("appid = %v, want %q", got, want)
		}
		// The client declared cp1, so the token carries xms_cc
		// (refs/entra/docs/identity-platform/claims-challenge.md:167-169).
		if _, ok := claims["xms_cc"]; !ok {
			t.Error("the access token has no xms_cc claim although the client declared cp1")
		}

		// Two authorization_pending polls and the successful one: the retry loop MSAL runs
		// (refs/msal-go/apps/internal/oauth/oauth.go:277-328).
		if got, want := srv.DeviceCodePolls(), 3; got != want {
			t.Errorf("DeviceCodePolls() = %d, want %d", got, want)
		}
		if got, want := len(srv.IssuedRefreshTokens()), 1; got != want {
			t.Errorf("the fake issued %d refresh tokens, want %d", got, want)
		}
	})

	t.Run("a silent acquisition is served from the cache", func(t *testing.T) {
		tokenRequests := srv.TokenRequests()
		silent, err := client.AcquireTokenSilent(ctx, msalScopes, public.WithSilentAccount(acquired.Account))
		if err != nil {
			t.Fatalf("AcquireTokenSilent: %v", err)
		}
		if got, want := silent.Metadata.TokenSource, public.TokenSourceCache; got != want {
			t.Errorf("TokenSource = %v, want %v", got, want)
		}
		if silent.AccessToken != acquired.AccessToken {
			t.Error("the silent acquisition returned a different access token")
		}
		// A cache hit is not allowed to touch the network, let alone redeem a refresh token.
		if got := srv.TokenRequests(); got != tokenRequests {
			t.Errorf("TokenRequests() = %d, want no further request (still %d)", got, tokenRequests)
		}
		if got := srv.ConsumedRefreshTokens(); len(got) != 0 {
			t.Errorf("ConsumedRefreshTokens() = %v, want none", got)
		}
	})

	t.Run("claims force a refresh that rotates the refresh token", func(t *testing.T) {
		issued := srv.IssuedRefreshTokens()
		refreshed, err := client.AcquireTokenSilent(ctx, msalScopes,
			public.WithSilentAccount(acquired.Account), public.WithClaims(claimsChallenge))
		if err != nil {
			t.Fatalf("AcquireTokenSilent with claims: %v", err)
		}
		if got, want := refreshed.Metadata.TokenSource, public.TokenSourceIdentityProvider; got != want {
			t.Errorf("TokenSource = %v, want %v: claims must bypass the cached access token", got, want)
		}
		if refreshed.AccessToken == acquired.AccessToken {
			t.Error("the claims-forced acquisition reused the cached access token")
		}

		// The fake rotates on every redemption and invalidates the presented token.
		rotated := srv.IssuedRefreshTokens()
		if got, want := len(rotated), len(issued)+1; got != want {
			t.Fatalf("the fake issued %d refresh tokens, want %d", got, want)
		}
		if got, want := srv.ConsumedRefreshTokens(), issued; !slices.Equal(got, want) {
			t.Errorf("ConsumedRefreshTokens() = %v, want %v", got, want)
		}
		if slices.Contains(srv.ActiveRefreshTokens(), issued[0]) {
			t.Errorf("the presented refresh token %q is still active", issued[0])
		}

		// The rejected token proves the server invalidated it; the next subtest proves the client
		// stored the replacement.
		e := redeemRefreshTokenExpectError(t, srv, issued[0])
		mustContain(t, e.Description, "AADSTS70008")
	})

	t.Run("the store holds the rotated refresh token", func(t *testing.T) {
		// Only a cache write-back can make this work: the token presented here is the one the
		// previous refresh returned, and MSAL read it from the store.
		before := srv.ConsumedRefreshTokens()
		if _, err := client.AcquireTokenSilent(ctx, msalScopes,
			public.WithSilentAccount(acquired.Account), public.WithClaims(claimsChallenge)); err != nil {
			t.Fatalf("AcquireTokenSilent with claims: %v", err)
		}
		consumed := srv.ConsumedRefreshTokens()
		if got, want := len(consumed), len(before)+1; got != want {
			t.Fatalf("ConsumedRefreshTokens() has %d entries, want %d", got, want)
		}
		if consumed[len(consumed)-1] == consumed[0] {
			t.Errorf("the second refresh presented %q again, so the rotation was not stored", consumed[0])
		}
	})

	t.Run("the new access token is cached", func(t *testing.T) {
		before := srv.TokenRequests()
		silent, err := client.AcquireTokenSilent(ctx, msalScopes, public.WithSilentAccount(acquired.Account))
		if err != nil {
			t.Fatalf("AcquireTokenSilent: %v", err)
		}
		if got, want := silent.Metadata.TokenSource, public.TokenSourceCache; got != want {
			t.Errorf("TokenSource = %v, want %v", got, want)
		}
		if got := srv.TokenRequests(); got != before {
			t.Errorf("TokenRequests() = %d, want no further request (still %d)", got, before)
		}
	})
}

// TestMSALRefreshesAnExpiringAccessToken pins down the five-minute margin MSAL applies to a cached
// access token: it declares a token invalid when it is within five minutes of expiry
// (refs/msal-go/apps/internal/base/storage/items.go:131-144), so a short-lived token makes the
// next silent acquisition redeem the refresh token instead of hitting the cache.
func TestMSALRefreshesAnExpiringAccessToken(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Scopes: msalScopes, ExpiresIn: 60})
	client := newMSALClient(t, srv)
	ctx := context.Background()

	dc, err := client.AcquireTokenByDeviceCode(ctx, msalScopes)
	if err != nil {
		t.Fatalf("AcquireTokenByDeviceCode: %v", err)
	}
	acquired, err := dc.AuthenticationResult(ctx)
	if err != nil {
		t.Fatalf("AuthenticationResult: %v", err)
	}

	silent, err := client.AcquireTokenSilent(ctx, msalScopes, public.WithSilentAccount(acquired.Account))
	if err != nil {
		t.Fatalf("AcquireTokenSilent: %v", err)
	}
	if got, want := silent.Metadata.TokenSource, public.TokenSourceIdentityProvider; got != want {
		t.Errorf("TokenSource = %v, want %v: a token expiring within five minutes is not reusable", got, want)
	}
	if got, want := srv.ConsumedRefreshTokens(), srv.IssuedRefreshTokens()[:1]; !slices.Equal(got, want) {
		t.Errorf("ConsumedRefreshTokens() = %v, want %v", got, want)
	}
}

// TestMSALInjectedErrors asserts that an injected error reaches the caller with its code intact:
// MSAL wraps a non-2xx response in a CallErr whose message embeds the body
// (refs/msal-go/apps/internal/oauth/ops/internal/comm/comm.go:255-275), which is what the CLI's
// error classifier reads.
func TestMSALInjectedErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		errors   Errors
		wantText string
	}{
		{"AADSTS65001 on the device-code grant", Errors{DeviceCode: AADSTS65001()}, "AADSTS65001"},
		{"AADSTS50020 on the device-code grant", Errors{DeviceCode: AADSTS50020()}, "AADSTS50020"},
		{"invalid_grant on the device-code grant", Errors{DeviceCode: InvalidGrant()}, "invalid_grant"},
		{"expired device code", Errors{DeviceCode: ExpiredDeviceCode()}, "expired_token"},
		{"declined device code", Errors{DeviceCode: AuthorizationDeclined()}, "authorization_declined"},
		{"authorization endpoint error", Errors{Authorization: AADSTS65001()}, "AADSTS65001"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := New(t, Options{Scopes: msalScopes, Errors: test.errors})
			client := newMSALClient(t, srv)
			ctx := context.Background()

			var err error
			if test.errors.Authorization != nil {
				_, err = client.AcquireTokenInteractive(ctx, msalScopes, public.WithOpenURL(srv.FakeBrowser()))
			} else {
				dc, dcErr := client.AcquireTokenByDeviceCode(ctx, msalScopes)
				if dcErr != nil {
					t.Fatalf("AcquireTokenByDeviceCode: %v", dcErr)
				}
				_, err = dc.AuthenticationResult(ctx)
			}
			mustContain(t, errText(t, err), test.wantText)
		})
	}
}

// TestMSALInjectedRefreshError covers the revocation path the CLI has to survive: a silent
// acquisition that redeems a refresh token the tenant no longer accepts fails with the AADSTS code
// in the message, which makes `teams auth status` exit 3 (PLAN.md, service-account bot flow).
func TestMSALInjectedRefreshError(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Scopes: msalScopes})
	client := newMSALClient(t, srv)
	ctx := context.Background()

	dc, err := client.AcquireTokenByDeviceCode(ctx, msalScopes)
	if err != nil {
		t.Fatalf("AcquireTokenByDeviceCode: %v", err)
	}
	acquired, err := dc.AuthenticationResult(ctx)
	if err != nil {
		t.Fatalf("AuthenticationResult: %v", err)
	}

	srv.mu.Lock()
	srv.opts.Errors.Refresh = AADSTS65001()
	srv.mu.Unlock()

	// The cached access token is still good, so the failure needs a forced refresh — the same path
	// a claims challenge takes.
	_, err = client.AcquireTokenSilent(ctx, msalScopes,
		public.WithSilentAccount(acquired.Account), public.WithClaims(claimsChallenge))
	mustContain(t, errText(t, err), "AADSTS65001")
}

// TestMSALInteractiveFlow drives AcquireTokenInteractive without a browser, using the fake as the
// browser through public.WithOpenURL (refs/msal-go/apps/public/public.go:645-658).
func TestMSALInteractiveFlow(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Scopes: msalScopes})
	client := newMSALClient(t, srv)

	acquired, err := client.AcquireTokenInteractive(context.Background(), msalScopes,
		public.WithOpenURL(srv.FakeBrowser()))
	if err != nil {
		t.Fatalf("AcquireTokenInteractive: %v", err)
	}
	if got, want := acquired.Account.PreferredUsername, "alice@"+DefaultTenant; got != want {
		t.Errorf("PreferredUsername = %q, want %q", got, want)
	}
	if got, want := acquired.Metadata.TokenSource, public.TokenSourceIdentityProvider; got != want {
		t.Errorf("TokenSource = %v, want %v", got, want)
	}

	req, ok := srv.LastAuthorizationRequest()
	if !ok {
		t.Fatal("the fake never saw an authorization URL")
	}
	// MSAL always requests PKCE with S256 and response_mode=form_post for interactive auth
	// (refs/msal-go/apps/public/public.go:687-701,
	// refs/msal-go/apps/internal/base/base.go:321-324).
	if req.CodeChallenge == "" || req.CodeChallengeMethod != "S256" {
		t.Errorf("authorization request used code_challenge %q (%q), want an S256 challenge",
			req.CodeChallenge, req.CodeChallengeMethod)
	}
	if req.ResponseMode != "form_post" {
		t.Errorf("response_mode = %q, want form_post", req.ResponseMode)
	}
	// MSAL appends the OIDC scopes the ID token and the refresh token need
	// (refs/msal-go/apps/internal/oauth/ops/accesstokens/accesstokens.go:490-501).
	for _, scope := range []string{"openid", "profile", "offline_access"} {
		if !slices.Contains(req.Scopes, scope) {
			t.Errorf("scopes = %v, want it to contain %q", req.Scopes, scope)
		}
	}
	claims := decodeJWTClaims(t, acquired.AccessToken)
	if got, want := claims["scp"], "User.Read Chat.Read"; got != want {
		t.Errorf("scp = %v, want %q", got, want)
	}
}

// TestMSALRejectsGETCallback documents why the flow helper only POSTs. MSAL's listener answers a
// GET with 405 and fails the acquisition (refs/msal-go/apps/internal/local/server.go:155-163), so
// the fake plays the browser with the form_post the real authorization endpoint would send.
func TestMSALRejectsGETCallback(t *testing.T) {
	t.Parallel()
	srv := New(t, Options{Scopes: msalScopes})
	client := newMSALClient(t, srv)

	openURL := func(authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		query := parsed.Query()
		callback := query.Get("redirect_uri") + "?code=ac-1&state=" + url.QueryEscape(query.Get("state"))
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, callback, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			return fmt.Errorf("the listener answered HTTP %d for a GET, want 405", resp.StatusCode)
		}
		return nil
	}

	_, err := client.AcquireTokenInteractive(context.Background(), msalScopes, public.WithOpenURL(openURL))
	mustContain(t, errText(t, err), "GET operation")
}
