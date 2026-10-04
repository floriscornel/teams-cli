package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/auth/tokenstore"
	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
)

type testKeyring struct {
	err   error
	items map[string]string
}

func (k *testKeyring) Get(service, user string) (string, error) {
	if k.err != nil {
		return "", k.err
	}
	if v, ok := k.items[service+"/"+user]; ok {
		return v, nil
	}
	return "", errors.New("not found in keyring")
}

func (k *testKeyring) Set(service, user, secret string) error {
	if k.err != nil {
		return k.err
	}
	if k.items == nil {
		k.items = map[string]string{}
	}
	k.items[service+"/"+user] = secret
	return nil
}

func (k *testKeyring) Delete(service, user string) error {
	delete(k.items, service+"/"+user)
	return nil
}

func testOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	t.Setenv(store.EnvState, filepath.Join(root, "state"))
	t.Setenv(store.EnvCache, filepath.Join(root, "cache"))
	t.Setenv(store.EnvConfig, filepath.Join(root, "config.toml"))
	paths, err := store.Resolve("me")
	if err != nil {
		t.Fatal(err)
	}
	return Options{
		Effective: config.Effective{
			Name:         "me",
			Tenant:       "colorkrew.com",
			ClientID:     config.DefaultClientID,
			Cloud:        "global",
			GraphBaseURL: "https://graph.microsoft.com/v1.0",
			Scopes:       []string{"User.Read"},
			ReadOnly:     true,
			ScopesSpec:   "read-only",
			TokenStore:   "auto",
		},
		Paths:                    paths,
		Clock:                    clock.NewFake(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)),
		Environ:                  []string{},
		DisableInstanceDiscovery: true,
		Keyring:                  &testKeyring{err: errors.New("no keychain in CI")},
	}
}

func TestNewUsesTheEnvAccessToken(t *testing.T) {
	opts := testOptions(t)
	token := makeToken(t, map[string]any{
		"aud":                GraphAppIDAudience,
		"exp":                float64(time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC).Unix()),
		"scp":                "User.Read Chat.Read",
		"preferred_username": "env@example.com",
		"tid":                "tenant-from-token",
	})
	opts.Environ = []string{config.EnvAccessToken + "=" + token}

	c, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Static() {
		t.Fatal("client is not in static-token mode")
	}
	if c.Store() != nil {
		t.Error("static mode should not touch a token store")
	}
	got, err := c.Token(context.Background())
	if err != nil || got != token {
		t.Fatalf("Token = (%q, %v)", got, err)
	}
	scopes, err := c.GrantedScopes(context.Background())
	if err != nil || strings.Join(scopes, " ") != "User.Read Chat.Read" {
		t.Fatalf("GrantedScopes = (%v, %v)", scopes, err)
	}
	status, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.SignedIn || !status.AccessTokenFromEnv {
		t.Errorf("status = %+v", status)
	}
	if status.Account != "env@example.com" || status.TenantID != "tenant-from-token" {
		t.Errorf("status identity = %+v", status)
	}
	if len(status.Scopes) != 2 {
		t.Errorf("status scopes = %v", status.Scopes)
	}
	if _, err := c.Silent(context.Background(), nil); output.CodeOf(err) != output.CodeAuth {
		t.Errorf("Silent in static mode = %v, want an auth error", err)
	}
	if err := c.Logout(context.Background()); output.CodeOf(err) != output.CodeUsage {
		t.Errorf("Logout in static mode = %v, want a usage error", err)
	}
	if _, err := c.Login(context.Background(), LoginOptions{}); output.CodeOf(err) != output.CodeUsage {
		t.Errorf("Login in static mode = %v, want a usage error", err)
	}
}

func TestNewRejectsABadEnvToken(t *testing.T) {
	cases := map[string]map[string]any{
		"wrong audience": {"aud": "https://outlook.office.com", "exp": float64(time.Now().Add(time.Hour).Unix())},
		"expired":        {"aud": GraphAppIDAudience, "exp": float64(time.Now().Add(-time.Hour).Unix())},
	}
	for name, claims := range cases {
		t.Run(name, func(t *testing.T) {
			opts := testOptions(t)
			opts.Environ = []string{config.EnvAccessToken + "=" + makeToken(t, claims)}
			_, err := New(context.Background(), opts)
			if err == nil {
				t.Fatal("New accepted a bad token")
			}
			if output.CodeOf(err) != output.CodeAuth {
				t.Errorf("exit code = %d, want %d", output.CodeOf(err), output.CodeAuth)
			}
			if !strings.Contains(output.HintOf(err), "TEAMS_ACCESS_TOKEN") {
				t.Errorf("hint = %q", output.HintOf(err))
			}
		})
	}
	opts := testOptions(t)
	opts.Environ = []string{config.EnvAccessToken + "=not-a-jwt"}
	if _, err := New(context.Background(), opts); err == nil {
		t.Error("New accepted a non-JWT token")
	}
}

func TestNewSelectsTheStoreAndReportsItsWarning(t *testing.T) {
	opts := testOptions(t)
	c, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if c.Store() == nil {
		t.Fatal("no token store")
	}
	// Nothing has been stored, so the keychain must not have been touched yet.
	if !c.Store().Empty() {
		t.Error("a fresh profile reports a non-empty token store")
	}
	status, err := c.Status(context.Background())
	if output.CodeOf(err) != output.CodeAuth {
		t.Fatalf("Status on a clean profile = %v, want exit 3", err)
	}
	if status.SignedIn {
		t.Error("Status reports signed in without an account")
	}
	if status.StoreDescription != "not created yet" {
		t.Errorf("status store = %+v", status)
	}
	// Probing (what `teams doctor` does) reveals the unreachable keychain and
	// the plaintext fallback the store will use.
	prober, ok := c.Store().(interface{ Probe() error })
	if !ok {
		t.Fatal("the envelope store has no Probe")
	}
	if err := prober.Probe(); err != nil {
		t.Fatal(err)
	}
	if c.Store().Kind() != tokenstore.KindFile {
		t.Errorf("store kind = %q, want the plaintext fallback", c.Store().Kind())
	}
	if !strings.Contains(c.Store().Warning(), "unencrypted") {
		t.Errorf("Warning = %q", c.Store().Warning())
	}
	status, err = c.Status(context.Background())
	if output.CodeOf(err) != output.CodeAuth {
		t.Fatalf("Status after Probe = %v, want exit 3", err)
	}
	if !strings.Contains(status.StoreWarning, "unencrypted") {
		t.Errorf("StoreWarning = %q", status.StoreWarning)
	}
	if status.StoreKind != tokenstore.KindFile || status.StoreDescription == "" {
		t.Errorf("status store = %+v", status)
	}
	if status.Mode != config.ModeReadOnly || !status.ReadOnly {
		t.Errorf("status mode = %+v", status)
	}
	if status.Cloud != "global" || status.Tenant != "colorkrew.com" || status.ClientID != config.DefaultClientID {
		t.Errorf("status profile = %+v", status)
	}
	if status.RequestedScopes[0] != "User.Read" || status.ScopesSpec != "read-only" {
		t.Errorf("status scopes = %+v", status)
	}
}

func TestNewHandlesTheKeyVaultGap(t *testing.T) {
	opts := testOptions(t)
	opts.Effective.TokenStore = "keyvault://kv/secret"
	_, err := New(context.Background(), opts)
	if !errors.Is(err, tokenstore.ErrNotImplemented) {
		t.Fatalf("err = %v, want ErrNotImplemented", err)
	}
}

func TestAuthorityPerCloud(t *testing.T) {
	opts := testOptions(t)
	t.Setenv(store.EnvState, t.TempDir())
	opts.Paths, _ = store.Resolve("me")
	c, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := c.Authority(), "https://login.microsoftonline.com/colorkrew.com"; got != want {
		t.Errorf("Authority = %q, want %q", got, want)
	}
	c.eff.Cloud = "usgov"
	if got, want := c.Authority(), "https://login.microsoftonline.us/colorkrew.com"; got != want {
		t.Errorf("Authority = %q, want %q", got, want)
	}
	c.eff.Cloud = "nonsense"
	if got, want := c.Authority(), "https://login.microsoftonline.com/colorkrew.com"; got != want {
		t.Errorf("fallback Authority = %q, want %q", got, want)
	}
}

func TestLoginRequiresAnInteractiveSession(t *testing.T) {
	opts := testOptions(t)
	c, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Login(context.Background(), LoginOptions{Interactive: false})
	if output.CodeOf(err) != output.CodeUsage {
		t.Fatalf("err = %v, want a usage error", err)
	}
	if !strings.Contains(output.HintOf(err), "--device") {
		t.Errorf("hint = %q, want the device-code fix", output.HintOf(err))
	}
}

func TestAccountsWithoutMSALReturnsNothing(t *testing.T) {
	c := &Client{eff: config.Effective{Name: "me"}, staticToken: "x"}
	accounts, err := c.Accounts(context.Background())
	if err != nil || accounts != nil {
		t.Fatalf("Accounts = (%v, %v)", accounts, err)
	}
}

func TestStatusRefreshAge(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	st := Status{LastRefresh: now.Add(-3 * time.Hour)}
	if got := st.RefreshAge(now); got != 3*time.Hour {
		t.Errorf("RefreshAge = %v", got)
	}
	if got := (Status{}).RefreshAge(now); got != 0 {
		t.Errorf("RefreshAge on an empty status = %v", got)
	}
}
