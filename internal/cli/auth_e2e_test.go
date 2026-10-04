package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
	"github.com/floriscornel/teams-cli/internal/testing/fakeidp"
)

// These tests drive the real CLI commands through real MSAL against fakeidp and
// a fake Graph, which is the only way to cover the parts the unit tests cannot
// reach: the device-code login, the silent acquisition MSAL serves from the
// cache, the feature matrix read from the token's `scp` claim, and exit code 3
// when the refresh token is dead.

type authEnv struct {
	harness *harness
	idp     *fakeidp.Server
	graph   *httptest.Server
	calls   *int
}

// newAuthEnv wires an App to fakeidp and a fake Graph with the given consented
// scopes.
func newAuthEnv(t *testing.T, consented []string, graphHandler http.HandlerFunc) *authEnv {
	return newAuthEnvWithExpiry(t, consented, 3600, graphHandler)
}

// newAuthEnvWithExpiry also sets the access-token lifetime. A lifetime inside
// MSAL's 5-minute expiry margin makes the next acquisition redeem the refresh
// token, which is how the rotation tests exercise write-back
// (refs/msal-go/apps/internal/base/storage/items.go:138).
func newAuthEnvWithExpiry(t *testing.T, consented []string, expiresIn int, graphHandler http.HandlerFunc) *authEnv {
	t.Helper()
	idp := fakeidp.New(t, fakeidp.Options{
		Tenant:    "colorkrew.com",
		TenantID:  "11111111-2222-3333-4444-555555555555",
		ClientID:  config.DefaultClientID,
		Account:   fakeidp.Account{PreferredUsername: "alice@example.com", Name: "Alice Example", ObjectID: "user-1"},
		Scopes:    consented,
		ExpiresIn: expiresIn,
		// Two pending polls keep the device-code path realistic without
		// slowing the test down.
		DeviceCodePollCount: 2,
	})
	calls := 0
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		graphHandler(w, r)
	}))
	t.Cleanup(graph.Close)

	h := newHarness(t)
	// The auth layer must read the real environment here: the token store's
	// TEAMS_NO_KEYCHAIN switch and the directory overrides are all read with
	// os.Getenv, which is also what a user's shell provides.
	h.env = nil
	h.app.Hooks.Environ = nil
	h.app.SetHooks(Hooks{
		ConfigPath:               h.app.ConfigPath(),
		Environ:                  nil,
		GraphBaseURL:             graph.URL + "/v1.0",
		AuthAuthority:            idp.Authority(),
		AuthHTTPClient:           idp.HTTPClient(),
		DisableInstanceDiscovery: true,
		Sleeper:                  func(context.Context, time.Duration) error { return nil },
	})
	return &authEnv{harness: h, idp: idp, graph: graph, calls: &calls}
}

func meHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"user-1","displayName":"Alice Example","userPrincipalName":"alice@example.com"}`))
	}
}

func TestAuthLoginStatusLogoutEndToEnd(t *testing.T) {
	env := newAuthEnv(t, []string{"User.Read", "Chat.Read"}, meHandler())
	h := env.harness

	// Login with the device-code flow. MSAL polls, fakeidp answers
	// authorization_pending twice and then issues tokens.
	if err := h.run("auth", "login", "--device"); err != nil {
		t.Fatalf("login failed: %v\nstderr: %s", err, h.stderr.String())
	}
	out := h.stdout.String()
	if !strings.Contains(out, "signed in as alice@example.com") {
		t.Errorf("login output = %q", out)
	}
	if !strings.Contains(h.stderr.String(), "device code") && !strings.Contains(h.stderr.String(), "code") {
		t.Errorf("device-code instructions missing from stderr: %q", h.stderr.String())
	}
	if env.idp.DeviceCodePolls() < 2 {
		t.Errorf("device-code polls = %d, want at least 2", env.idp.DeviceCodePolls())
	}

	// The token cache was written, and the account was recorded in the config so
	// silent acquisition has an account.
	paths, err := h.app.Paths()
	if err != nil {
		t.Fatal(err)
	}
	if token := store.Scan(paths.TokenFilePlain(), false); !token.Exists {
		t.Fatalf("login did not write the token cache at %s", paths.TokenFilePlain())
	}
	if got := strings.TrimSpace(h.mustRun("config", "get", "profiles.me.home_account_id")); got == "" {
		t.Error("login did not record home_account_id in the profile")
	}

	// auth status reads the granted scopes from the token's `scp` claim.
	out = h.mustRun("auth", "status")
	if !strings.Contains(out, "signed in:") || !strings.Contains(out, "alice@example.com") {
		t.Errorf("status = %q", out)
	}
	if !strings.Contains(out, "Chat.Read") || !strings.Contains(out, "User.Read") {
		t.Errorf("status does not list the granted scopes: %q", out)
	}
	var payload struct {
		SignedIn      bool     `json:"signed_in"`
		GrantedScopes []string `json:"granted_scopes"`
		StoreKind     string   `json:"token_store"`
		Account       string   `json:"account"`
	}
	out = h.mustRun("auth", "status", "--json")
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("auth status --json: %v (%q)", err, out)
	}
	if !payload.SignedIn || payload.Account != "alice@example.com" {
		t.Errorf("payload = %+v", payload)
	}
	if strings.Join(payload.GrantedScopes, " ") != "Chat.Read User.Read" {
		t.Errorf("granted scopes = %v, want the consented set from the token", payload.GrantedScopes)
	}

	// A silent acquisition is served from the cache: one login, no extra
	// token-endpoint round trips for `whoami`.
	before := env.idp.TokenRequests()
	if err := h.run("whoami"); err != nil {
		t.Fatalf("whoami failed: %v", err)
	}
	if !strings.Contains(h.stdout.String(), "Alice Example") {
		t.Errorf("whoami = %q", h.stdout.String())
	}
	if after := env.idp.TokenRequests(); after != before {
		t.Errorf("whoami made %d extra token requests; a cached access token should be reused", after-before)
	}

	// Logout removes the account and the token material.
	h.mustRun("auth", "logout", "--yes")
	if store.Scan(paths.TokenFilePlain(), false).Exists {
		t.Error("logout left the token cache behind")
	}
	if _, err := os.Stat(paths.AuthMetadataFile()); !os.IsNotExist(err) {
		t.Errorf("logout left the auth metadata behind: %v", err)
	}
	err = h.run("auth", "status")
	h.wantCode(err, output.CodeAuth)
}

func TestSilentRefreshRotatesTheRefreshToken(t *testing.T) {
	env := newAuthEnvWithExpiry(t, []string{"User.Read"}, 60, meHandler())
	h := env.harness
	if err := h.run("auth", "login", "--device"); err != nil {
		t.Fatal(err)
	}
	issuedBefore := len(env.idp.IssuedRefreshTokens())
	// Resolve the first App's layout before creating the second one: both are
	// bound to the same state directory, which is how two CLI invocations share
	// a token cache.
	firstPaths, err := h.app.Paths()
	if err != nil {
		t.Fatal(err)
	}

	// A second App in the same process but with no in-memory token cache stands
	// for the next CLI invocation: it must redeem the refresh token, and MSAL
	// must export the rotated cache back to our store.
	second := newHarness(t)
	second.app.Clock = h.app.Clock
	second.env = nil
	second.app.SetHooks(Hooks{
		ConfigPath:               h.app.ConfigPath(),
		Environ:                  nil,
		GraphBaseURL:             env.graph.URL + "/v1.0",
		AuthAuthority:            env.idp.Authority(),
		AuthHTTPClient:           env.idp.HTTPClient(),
		DisableInstanceDiscovery: true,
		Sleeper:                  func(context.Context, time.Duration) error { return nil },
	})
	// Share the state directory so the second "process" sees the same cache.
	t.Setenv(store.EnvState, filepath.Dir(firstPaths.StateDir))
	t.Setenv(store.EnvConfig, h.app.ConfigPath())

	if err := second.run("auth", "status"); err != nil {
		t.Fatalf("status after login: %v\nstderr: %s", err, second.stderr.String())
	}
	if got := len(env.idp.IssuedRefreshTokens()); got <= issuedBefore {
		t.Errorf("refresh tokens issued = %d, want more than %d: the refresh token was not redeemed", got, issuedBefore)
	}
	if len(env.idp.ConsumedRefreshTokens()) == 0 {
		t.Error("no refresh token was consumed")
	}
	if len(env.idp.ActiveRefreshTokens()) == 0 {
		t.Error("the rotated refresh token was not stored as active")
	}
}

func TestLoginSurfacesAConsentFailure(t *testing.T) {
	env := newAuthEnv(t, []string{"User.Read"}, meHandler())
	// The tenant refuses the requested scopes the way Colorkrew does.
	env.idp.SetErrors(fakeidp.Errors{DeviceCode: fakeidp.AADSTS65001()})

	err := env.harness.run("auth", "login", "--device")
	env.harness.wantCode(err, output.CodeAuth)
	if !strings.Contains(err.Error(), "AADSTS65001") {
		t.Errorf("error = %v, want the AADSTS code", err)
	}
	hint := output.HintOf(err)
	if !strings.Contains(hint, "user or admin consent") {
		t.Errorf("hint = %q", hint)
	}
	if !strings.Contains(hint, "--admin-request") {
		t.Errorf("hint = %q, want it to point at the admin request text", hint)
	}
}

func TestLoginWithoutAnInjectedHTTPClientDoesNotPanic(t *testing.T) {
	// Regression: `teams auth login` used to crash with a nil pointer
	// dereference inside MSAL, because the App's unset AuthHTTPClient field
	// reached MSAL as a typed-nil interface. The untrusted certificate of the
	// fake authority now surfaces as an ordinary error instead.
	env := newAuthEnv(t, []string{"User.Read"}, meHandler())
	env.harness.app.SetHooks(Hooks{
		ConfigPath:               env.harness.app.ConfigPath(),
		Environ:                  nil,
		GraphBaseURL:             env.graph.URL + "/v1.0",
		AuthAuthority:            env.idp.Authority(),
		DisableInstanceDiscovery: true,
	})

	err := env.harness.run("auth", "login", "--device")
	if err == nil {
		t.Fatal("login against a self-signed authority succeeded with the default client")
	}
	if strings.Contains(err.Error(), "nil pointer") {
		t.Fatalf("login panicked through a nil HTTP client: %v", err)
	}
	if output.CodeOf(err) == output.CodeOK {
		t.Errorf("exit code = %d, want a failure", output.CodeOf(err))
	}
}

func TestScopePreCheckStopsBeforeGraph(t *testing.T) {
	env := newAuthEnv(t, []string{"Chat.Read"}, meHandler())
	h := env.harness
	if err := h.run("auth", "login", "--device"); err != nil {
		t.Fatal(err)
	}
	// The token carries Chat.Read but not User.Read, so whoami must fail with
	// exit 3 *before* calling Graph.
	before := *env.calls
	err := h.run("whoami")
	h.wantCode(err, output.CodeAuth)
	if *env.calls != before {
		t.Errorf("whoami reached Graph (%d calls) without the User.Read scope", *env.calls-before)
	}
	hint := output.HintOf(err)
	if !strings.Contains(hint, "consent") && !strings.Contains(hint, "app registration") {
		t.Errorf("hint = %q", hint)
	}
}

func TestGraphForbiddenAfterTheScopeCheckExplainsItself(t *testing.T) {
	env := newAuthEnv(t, []string{"User.Read"}, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"code":"Authorization_RequestDenied","message":"API requires one of 'ChannelMessage.ReadWrite, Group.ReadWrite.All'"}}`))
	})
	h := env.harness
	if err := h.run("auth", "login", "--device"); err != nil {
		t.Fatal(err)
	}
	err := h.run("whoami")
	h.wantCode(err, output.CodeAuth)
	if !strings.Contains(output.HintOf(err), "ChannelMessage.ReadWrite") {
		t.Errorf("hint = %q, want the scopes Graph named", output.HintOf(err))
	}
}

func TestStatusReportsADeadRefreshToken(t *testing.T) {
	env := newAuthEnvWithExpiry(t, []string{"User.Read"}, 60, meHandler())
	h := env.harness
	if err := h.run("auth", "login", "--device"); err != nil {
		t.Fatal(err)
	}
	firstPaths, err := h.app.Paths()
	if err != nil {
		t.Fatal(err)
	}
	// The refresh token is revoked, as a password reset or an admin revocation
	// would: the next silent acquisition must exit 3 with the re-login hint.
	env.idp.SetErrors(fakeidp.Errors{Refresh: fakeidp.InvalidGrant()})
	second := newHarness(t)
	second.env = nil
	second.app.SetHooks(Hooks{
		ConfigPath:               h.app.ConfigPath(),
		Environ:                  nil,
		GraphBaseURL:             env.graph.URL + "/v1.0",
		AuthAuthority:            env.idp.Authority(),
		AuthHTTPClient:           env.idp.HTTPClient(),
		DisableInstanceDiscovery: true,
		Sleeper:                  func(context.Context, time.Duration) error { return nil },
	})
	// The second App has no in-memory token, so MSAL must redeem the refresh
	// token, which the fake now rejects.
	t.Setenv(store.EnvState, filepath.Dir(firstPaths.StateDir))

	err = second.run("auth", "status")
	second.wantCode(err, output.CodeAuth)
	if !strings.Contains(err.Error(), "invalid_grant") && !strings.Contains(err.Error(), "not signed in") {
		t.Errorf("err = %v", err)
	}
}
