package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/auth/tokenstore"
	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
	"github.com/floriscornel/teams-cli/internal/testing/fakeidp"
)

// These tests drive the real MSAL client against fakeidp, which is the only way
// to cover silent refresh, the refresh-token write-back and the interactive
// fallback without a tenant.

type idpEnv struct {
	client  *Client
	idp     *fakeidp.Server
	paths   store.Paths
	store   *tokenstore.Memory
	options Options
}

// newIDPEnv builds an auth.Client wired to fakeidp with an in-memory store.
func newIDPEnv(t *testing.T, consented []string, expiresIn int) *idpEnv {
	t.Helper()
	root := t.TempDir()
	t.Setenv(store.EnvState, filepath.Join(root, "state"))
	t.Setenv(store.EnvCache, filepath.Join(root, "cache"))
	t.Setenv(store.EnvConfig, filepath.Join(root, "config.toml"))
	paths, err := store.Resolve("me")
	if err != nil {
		t.Fatal(err)
	}
	idp := fakeidp.New(t, fakeidp.Options{
		Tenant:              "colorkrew.com",
		TenantID:            "11111111-2222-3333-4444-555555555555",
		ClientID:            config.DefaultClientID,
		Account:             fakeidp.Account{PreferredUsername: "alice@example.com", Name: "Alice Example", ObjectID: "user-1"},
		Scopes:              consented,
		ExpiresIn:           expiresIn,
		DeviceCodePollCount: 1,
	})
	store := tokenstore.NewMemory()
	opts := Options{
		Effective: config.Effective{
			Name:         "me",
			Tenant:       "colorkrew.com",
			ClientID:     config.DefaultClientID,
			Cloud:        "global",
			GraphBaseURL: "https://graph.microsoft.com/v1.0",
			Scopes:       consented,
			ScopesSpec:   "read-only",
			TokenStore:   "auto",
		},
		Paths:                    paths,
		Clock:                    clock.NewFake(testNow()),
		Environ:                  []string{},
		HTTPClient:               idp.HTTPClient(),
		DisableInstanceDiscovery: true,
		Authority:                idp.Authority(),
		Store:                    store,
		OpenURL:                  idp.FakeBrowser(),
	}
	client, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return &idpEnv{client: client, idp: idp, paths: paths, store: store, options: opts}
}

func TestDeviceLoginPersistsTheCache(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read", "Chat.Read"}, 3600)
	ctx := context.Background()

	result, err := env.client.Login(ctx, LoginOptions{Device: true})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if result.Account != "alice@example.com" || result.HomeAccountID == "" {
		t.Errorf("result = %+v", result)
	}
	// MSAL's GrantedScopes echoes what the token response's scope field says,
	// which is the requested set plus the three MSAL appends itself. The
	// consented set is read from `scp` instead (the next assertion).
	requested := strings.Join(result.Scopes, " ")
	for _, want := range []string{"User.Read", "Chat.Read", "openid", "profile", "offline_access"} {
		if !strings.Contains(requested, want) {
			t.Errorf("GrantedScopes = %v, want it to contain %s", result.Scopes, want)
		}
	}
	if len(env.store.Data) == 0 || string(env.store.Data) == "{}" {
		t.Fatalf("the cache was not written: %q", env.store.Data)
	}
	if env.idp.DeviceCodePolls() == 0 {
		t.Error("the device-code grant was not polled")
	}
	// The refresh bookkeeping is ours: MSAL's blob is opaque.
	meta := LoadMetadata(env.paths.AuthMetadataFile())
	if meta.LastRefresh.IsZero() || meta.HomeAccountID != result.HomeAccountID {
		t.Errorf("metadata = %+v", meta)
	}
	if meta.Account != "alice@example.com" {
		t.Errorf("metadata account = %q", meta.Account)
	}

	// A second acquisition is served from the cache: no extra token requests.
	before := env.idp.TokenRequests()
	token, err := env.client.Token(ctx)
	if err != nil || token == "" {
		t.Fatalf("Token = (%q, %v)", token, err)
	}
	if after := env.idp.TokenRequests(); after != before {
		t.Errorf("Token made %d token requests; the cache should have served it", after-before)
	}
	scopes, err := env.client.GrantedScopes(ctx)
	if err != nil {
		t.Fatalf("GrantedScopes: %v", err)
	}
	// `scp` carries the consented set, in the order the tenant reports it, and
	// the CLI sorts it for display.
	if missing := config.Scopes.Missing(scopes, []string{"User.Read", "Chat.Read"}); len(missing) != 0 {
		t.Fatalf("GrantedScopes = %v, missing %v", scopes, missing)
	}
	accounts, err := env.client.Accounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("Accounts = (%d, %v)", len(accounts), err)
	}
	if accounts[0].PreferredUsername != "alice@example.com" {
		t.Errorf("account = %+v", accounts[0])
	}
}

func TestInteractiveLoginUsesTheBrowserHook(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read"}, 3600)
	result, err := env.client.Login(context.Background(), LoginOptions{Interactive: true})
	if err != nil {
		t.Fatalf("interactive login: %v", err)
	}
	if result.Account != "alice@example.com" {
		t.Errorf("result = %+v", result)
	}
	req, ok := env.idp.LastAuthorizationRequest()
	if !ok {
		t.Fatal("the browser hook never saw an authorization request")
	}
	if req.CodeChallengeMethod != "S256" || req.CodeChallenge == "" {
		t.Errorf("PKCE was not used: %+v", req)
	}
	if req.ResponseMode != "form_post" {
		t.Errorf("response_mode = %q, want form_post", req.ResponseMode)
	}
	if !strings.Contains(strings.Join(req.Scopes, " "), "User.Read") {
		t.Errorf("requested scopes = %v", req.Scopes)
	}
}

func TestInteractiveLoginFallsBackToDeviceCode(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read"}, 3600)
	// Entra rejects the redirect URI the way the Phase 1 spike saw when the app
	// registration lacked http://localhost.
	env.idp.SetErrors(fakeidp.Errors{AuthCode: fakeidp.NewError("invalid_request", "AADSTS50011: The redirect URI specified in the request does not match the redirect URIs configured for the application")})

	var fallback error
	codes := 0
	result, err := env.client.Login(context.Background(), LoginOptions{
		Interactive:  true,
		OnFallback:   func(cause error) { fallback = cause },
		OnDeviceCode: func(DeviceCode) {},
	})
	if err != nil {
		t.Fatalf("Login with fallback: %v", err)
	}
	if fallback == nil || !IsRedirectURIError(fallback) {
		t.Errorf("fallback = %v, want the AADSTS50011 the browser flow hit", fallback)
	}
	if result.Account != "alice@example.com" {
		t.Errorf("result = %+v", result)
	}
	_ = codes
	if env.idp.DeviceCodePolls() == 0 {
		t.Error("the device-code fallback did not run")
	}
}

func TestDeviceCodeCallbackSeesTheCode(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read"}, 3600)
	var seen DeviceCode
	if _, err := env.client.Login(context.Background(), LoginOptions{
		Device: true,
		OnDeviceCode: func(dc DeviceCode) {
			seen = dc
		},
	}); err != nil {
		t.Fatal(err)
	}
	if seen.UserCode == "" || seen.VerificationURL == "" {
		t.Errorf("device code = %+v", seen)
	}
	if seen.Message == "" {
		t.Errorf("MSAL's message was dropped: %+v", seen)
	}
	if seen.ExpiresOn.IsZero() {
		t.Error("the device code has no expiry, so the 15-minute window cannot be shown")
	}
}

func TestStatusAndLogoutEndToEnd(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read", "Chat.ReadWrite"}, 3600)
	ctx := context.Background()
	if _, err := env.client.Login(ctx, LoginOptions{Device: true}); err != nil {
		t.Fatal(err)
	}
	status, err := env.client.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.SignedIn || status.Account != "alice@example.com" {
		t.Errorf("status = %+v", status)
	}
	if missing := config.Scopes.Missing(status.Scopes, []string{"User.Read", "Chat.ReadWrite"}); len(missing) != 0 {
		t.Errorf("granted scopes = %v, missing %v", status.Scopes, missing)
	}
	if status.Mode != config.ModeFull && status.ReadOnly {
		t.Errorf("mode = %q", status.Mode)
	}
	if status.LastRefresh.IsZero() {
		t.Error("status does not report the refresh time")
	}
	if !status.FromCache {
		t.Error("a fresh login should still be reported as cache-served by MSAL")
	}

	if err := env.client.Logout(ctx); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if !env.store.Deleted || env.store.Data != nil {
		t.Error("Logout did not delete the token store")
	}
	if _, err := os.Stat(env.paths.AuthMetadataFile()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Logout left the metadata behind: %v", err)
	}
	accounts, err := env.client.Accounts(ctx)
	if err != nil || len(accounts) != 0 {
		t.Errorf("Accounts after logout = (%d, %v)", len(accounts), err)
	}
	// A second logout is a no-op, not a crash.
	if err := env.client.Logout(ctx); err != nil {
		t.Errorf("second Logout = %v", err)
	}
	if _, err := env.client.Status(ctx); output.CodeOf(err) != output.CodeAuth {
		t.Errorf("Status after logout = %v, want exit 3", err)
	}
}

func TestSilentRedeemsTheRefreshTokenWhenTheAccessTokenIsStale(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read"}, 60)
	ctx := context.Background()
	if _, err := env.client.Login(ctx, LoginOptions{Device: true}); err != nil {
		t.Fatal(err)
	}
	// A 60-second lifetime is inside MSAL's 5-minute margin, so the next client
	// must redeem the refresh token and write the rotated cache back.
	second, err := New(ctx, env.options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.Silent(ctx, []string{"User.Read"}); err != nil {
		t.Fatalf("Silent: %v", err)
	}
	if len(env.idp.ConsumedRefreshTokens()) == 0 {
		t.Error("the refresh token was not redeemed")
	}
	if got := env.store.WriteCount; got < 2 {
		t.Errorf("the store was written %d times, want the rotated cache written back", got)
	}
}

func TestRefreshFailureIsClassified(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read"}, 60)
	ctx := context.Background()
	if _, err := env.client.Login(ctx, LoginOptions{Device: true}); err != nil {
		t.Fatal(err)
	}
	env.idp.SetErrors(fakeidp.Errors{Refresh: fakeidp.InvalidGrant()})
	second, err := New(ctx, env.options)
	if err != nil {
		t.Fatal(err)
	}
	_, err = second.Silent(ctx, []string{"User.Read"})
	if err == nil {
		t.Fatal("Silent succeeded with a revoked refresh token")
	}
	if output.CodeOf(err) != output.CodeAuth {
		t.Errorf("exit code = %d, want %d (%v)", output.CodeOf(err), output.CodeAuth, err)
	}
}

func TestNewSurfacesBadConfiguration(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read"}, 3600)
	bad := env.options
	bad.Authority = "http://not-https.example/tenant"
	if _, err := New(context.Background(), bad); err == nil {
		t.Error("New accepted a non-https authority")
	}
	bad = env.options
	bad.Store = nil
	bad.Keyring = &testKeyring{err: errors.New("no keychain")}
	bad.Effective.TokenStore = "file"
	bad.Paths.StateDir = filepath.Join(t.TempDir(), "state") // fresh dir
	client, err := New(context.Background(), bad)
	if err != nil {
		t.Fatalf("New with a file store: %v", err)
	}
	if client.Store().Kind() != tokenstore.KindFile {
		t.Errorf("store kind = %q", client.Store().Kind())
	}
	if !client.Store().Empty() {
		t.Error("a fresh file store is not empty")
	}
}

func TestMetadataPathAndStoreAccessors(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read"}, 3600)
	if env.client.Effective().Name != "me" {
		t.Errorf("Effective = %+v", env.client.Effective())
	}
	if env.client.Paths().Profile != "me" {
		t.Errorf("Paths = %+v", env.client.Paths())
	}
	if env.client.Store() == nil {
		t.Error("Store is nil")
	}
	if env.client.Static() {
		t.Error("a MSAL-backed client reports static mode")
	}
	if !env.client.Metadata().LastRefresh.IsZero() {
		t.Error("metadata was populated before any login")
	}
	// Authority is derived from the cloud and tenant, which is what a real
	// profile needs; the test override is applied to MSAL separately, and the
	// login tests above prove it is.
	if got, want := env.client.Authority(), "https://login.microsoftonline.com/colorkrew.com"; got != want {
		t.Errorf("Authority = %q, want %q", got, want)
	}
}

func TestNilHTTPClientFallsBackToTheDefault(t *testing.T) {
	// Regression: an unset *http.Client used to reach MSAL as a typed-nil
	// interface value, and MSAL dereferenced it on the first discovery request,
	// panicking before any sign-in could start. The fake's TLS certificate is
	// not trusted by the default client, so the login must now fail with a TLS
	// error instead of a nil pointer dereference.
	env := newIDPEnv(t, []string{"User.Read"}, 3600)
	opts := env.options
	opts.HTTPClient = nil

	client, err := New(context.Background(), opts)
	if err != nil {
		t.Fatalf("New with a nil HTTP client: %v", err)
	}
	if _, err := client.Login(context.Background(), LoginOptions{Device: true}); err == nil {
		t.Fatal("Login against a self-signed endpoint succeeded with the default client")
	} else if strings.Contains(err.Error(), "nil pointer") {
		t.Fatalf("Login panicked through a nil client: %v", err)
	}

	if got := msalHTTPClientFor(nil); got == nil {
		t.Error("msalHTTPClientFor(nil) returned nil")
	}
	custom := &http.Client{}
	if got := msalHTTPClientFor(custom); got != custom {
		t.Error("msalHTTPClientFor dropped the caller's client")
	}
}

func TestLoginRecoversWhenTheDataKeyIsGone(t *testing.T) {
	// The exact situation a user hit: the ciphertext is on disk, the keychain no
	// longer holds its key, and `teams auth login` must be able to start over
	// rather than repeat "run auth login again" forever.
	env := newIDPEnv(t, []string{"User.Read"}, 3600)
	ctx := context.Background()
	// A fake keychain, so the process-wide guard (auth_guard_test.go) is off for
	// this one test: the test binary must never touch the real keychain, but this
	// test needs the envelope path rather than the plaintext fallback.
	t.Setenv(tokenstore.EnvNoKeychain, "")

	// The envelope store with a fake keychain: this test is about the key, not
	// about the flows.
	keyring := &testKeyring{items: map[string]string{}}
	opts := env.options
	opts.Store = nil
	opts.Keyring = keyring
	opts.Effective.TokenStore = "auto"

	first, err := New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Login(ctx, LoginOptions{Device: true}); err != nil {
		t.Fatal(err)
	}
	// A keychain reset, a restored backup, or an item someone deleted.
	keyring.noItems()

	// Reading still fails, with the code and the fix a user needs.
	blocked, err := New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	_, statusErr := blocked.Status(ctx)
	if output.CodeOf(statusErr) != output.CodeAuth {
		t.Fatalf("Status = %v, want exit 3", statusErr)
	}
	if hint := output.HintOf(statusErr); !strings.Contains(hint, "auth logout") || !strings.Contains(hint, "auth login") {
		t.Errorf("hint = %q, want it to offer both the new-session and the discard path", hint)
	}

	// Logging in again replaces the unusable cache and says so.
	recovered, err := New(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovered.Login(ctx, LoginOptions{Device: true}); err != nil {
		t.Fatalf("Login after the key was lost: %v", err)
	}
	if w := recovered.Store().Warning(); !strings.Contains(w, "new session") {
		t.Errorf("store warning = %q, want it to explain the new session", w)
	}
	status, err := recovered.Status(ctx)
	if err != nil {
		t.Fatalf("Status after recovery: %v", err)
	}
	if !status.SignedIn {
		t.Errorf("status = %+v, want a signed-in session", status)
	}
}

func TestBrowserTimeoutExplainsTheRedirectURIFix(t *testing.T) {
	err := browserTimeoutError()
	if output.CodeOf(err) != output.CodeAuth {
		t.Errorf("exit code = %d, want %d", output.CodeOf(err), output.CodeAuth)
	}
	msg := err.Error()
	if !strings.Contains(msg, "5m0s") {
		t.Errorf("message does not say how long we waited: %q", msg)
	}
	hint := output.HintOf(err)
	for _, want := range []string{"AADSTS50011", "--device", "http://localhost", "admin"} {
		if !strings.Contains(hint, want) {
			t.Errorf("hint = %q, want it to mention %q", hint, want)
		}
	}
	if !strings.Contains(output.HintOf(err), "AADSTS50011") {
		t.Error("the hint does not name the error the user actually saw")
	}
}

func TestLoginIsRefusedWithoutATerminal(t *testing.T) {
	env := newIDPEnv(t, []string{"User.Read"}, 3600)
	_, err := env.client.Login(context.Background(), LoginOptions{Interactive: false})
	if output.CodeOf(err) != output.CodeUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(output.HintOf(err), "--device") {
		t.Errorf("hint = %q", output.HintOf(err))
	}
}
