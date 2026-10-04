package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
)

// harness runs the CLI in-process with isolated directories and streams.
type harness struct {
	t        *testing.T
	app      *App
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	env      []string
	root     string
	graphURL string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	h := &harness{t: t, root: root, env: []string{}}
	// The layout is relocated through the documented environment overrides, so
	// the tests use exactly the paths a container would use.
	t.Setenv(store.EnvConfig, filepath.Join(root, "config.toml"))
	t.Setenv(store.EnvState, filepath.Join(root, "state"))
	t.Setenv(store.EnvCache, filepath.Join(root, "cache"))
	t.Setenv("CI", "true") // non-interactive by default
	// Tests must never touch the developer's real keychain: the OS dialog for a
	// missing keychain is blocking, and on CI there is no keychain at all.
	t.Setenv("TEAMS_NO_KEYCHAIN", "1")
	h.app = New(strings.NewReader(""), &h.stdout, &h.stderr)
	h.app.Clock = clock.NewFake(harnessNow)
	h.applyHooks()
	return h
}

// harnessNow is the frozen clock every CLI test runs on.
var harnessNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// useProcessEnv makes the App read the real environment, for the tests that set
// a variable with t.Setenv (the harness otherwise passes an empty environment so
// a developer's own TEAMS_* variables cannot leak into a test).
func (h *harness) useProcessEnv() {
	h.env = os.Environ()
	h.applyHooks()
}

func (h *harness) applyHooks() {
	h.app.SetHooks(Hooks{
		ConfigPath:               filepath.Join(h.root, "config.toml"),
		Environ:                  h.env,
		GraphBaseURL:             h.graphURL,
		DisableInstanceDiscovery: true,
		Sleeper:                  func(context.Context, time.Duration) error { return nil },
	})
}

func (h *harness) run(args ...string) error {
	h.t.Helper()
	h.stdout.Reset()
	h.stderr.Reset()
	return h.app.Execute(context.Background(), args)
}

func (h *harness) mustRun(args ...string) string {
	h.t.Helper()
	if err := h.run(args...); err != nil {
		h.t.Fatalf("%v failed: %v\nstderr: %s", args, err, h.stderr.String())
	}
	return h.stdout.String()
}

func (h *harness) wantCode(err error, want int) {
	h.t.Helper()
	if got := output.CodeOf(err); got != want {
		h.t.Fatalf("exit code = %d, want %d (err: %v)", got, want, err)
	}
}

func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func (h *harness) useAccessToken(t *testing.T, scopes string) {
	t.Helper()
	token := makeJWT(t, map[string]any{
		"aud":                "00000003-0000-0000-c000-000000000000",
		"tid":                "tenant",
		"preferred_username": "alice@example.com",
		"scp":                scopes,
		// The harness clock is frozen at 2026-10-04T12:00Z, so the expiry is
		// fixed too: the token test must not depend on the wall clock.
		"exp": float64(time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC).Unix()),
	})
	h.env = []string{config.EnvAccessToken + "=" + token}
	h.app.SetHooks(Hooks{
		ConfigPath:   filepath.Join(h.root, "config.toml"),
		Environ:      h.env,
		GraphBaseURL: h.graphURL,
		Sleeper:      func(context.Context, time.Duration) error { return nil },
	})
}

func TestVersionCommand(t *testing.T) {
	h := newHarness(t)
	Version, Commit, Date = "v0.1.0-rc.1", "abc1234", "2026-10-04T12:00:00Z"
	defer func() { Version, Commit, Date = "dev", "none", "unknown" }()

	out := h.mustRun("version")
	for _, want := range []string{"v0.1.0-rc.1", "abc1234", "2026-10-04T12:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output %q lacks %q", out, want)
		}
	}
	out = h.mustRun("version", "--json")
	var info map[string]string
	if err := json.Unmarshal([]byte(out), &info); err != nil {
		t.Fatalf("--json output is not JSON: %v (%q)", err, out)
	}
	if info["version"] != "v0.1.0-rc.1" {
		t.Errorf("json = %v", info)
	}
}

func TestHelpAndFlagErrors(t *testing.T) {
	h := newHarness(t)
	out := h.mustRun("--help")
	for _, want := range []string{"auth", "cache", "doctor", "profile", "whoami", "config"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	err := h.run("--nope")
	h.wantCode(err, output.CodeUsage)
	err = h.run("no-such-command")
	if err == nil {
		t.Fatal("unknown command succeeded")
	}
}

func TestConfigGetSetList(t *testing.T) {
	h := newHarness(t)
	if out := h.mustRun("config", "set", "profiles.bot.tenant", "colorkrew.com"); !strings.Contains(out, "colorkrew.com") {
		t.Errorf("set output = %q", out)
	}
	h.mustRun("config", "set", "profiles.bot.scopes", "read-only")
	h.mustRun("config", "set", "profiles.bot.token_store", "keyvault://kv/secret")
	h.mustRun("config", "set", "default_profile", "bot")

	if out := h.mustRun("config", "get", "profiles.bot.tenant"); strings.TrimSpace(out) != "colorkrew.com" {
		t.Errorf("get = %q", out)
	}
	if out := h.mustRun("config", "get", "default_profile"); strings.TrimSpace(out) != "bot" {
		t.Errorf("get default_profile = %q", out)
	}
	out := h.mustRun("config", "list")
	for _, want := range []string{"default_profile", "bot", "read-only", "keyvault://kv/secret"} {
		if !strings.Contains(out, want) {
			t.Errorf("config list %q lacks %q", out, want)
		}
	}
	// The file on disk must be parseable and private.
	perms := store.Scan(h.app.ConfigPath(), false)
	if !perms.Exists {
		t.Fatal("config file was not written")
	}
	if _, err := config.Load(h.app.ConfigPath()); err != nil {
		t.Fatalf("written config does not load: %v", err)
	}

	err := h.run("config", "get", "nope.nope")
	h.wantCode(err, output.CodeUsage)
	if !strings.Contains(output.HintOf(err), "known keys") {
		t.Errorf("hint = %q", output.HintOf(err))
	}
	err = h.run("config", "set", "profiles.bot.mode", "sideways")
	h.wantCode(err, output.CodeUsage)
	err = h.run("config", "get", "profiles.ghost.tenant")
	h.wantCode(err, output.CodeUsage)
	err = h.run("config", "set", "profiles.bad/name.tenant", "x")
	h.wantCode(err, output.CodeUsage)
}

func TestConfigJSON(t *testing.T) {
	h := newHarness(t)
	h.mustRun("config", "set", "profiles.bot.tenant", "colorkrew.com")
	out := h.mustRun("config", "list", "--json")
	var payload struct {
		Path   string         `json:"path"`
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("config list --json: %v (%q)", err, out)
	}
	if payload.Path == "" || payload.Config == nil {
		t.Errorf("payload = %+v", payload)
	}
	out = h.mustRun("config", "get", "profiles.bot.tenant", "--json")
	if err := json.Unmarshal([]byte(out), &map[string]string{}); err != nil {
		t.Fatalf("config get --json: %v", err)
	}
}

func TestProfileListAndUse(t *testing.T) {
	h := newHarness(t)
	h.mustRun("config", "set", "profiles.bot.tenant", "colorkrew.com")
	h.mustRun("config", "set", "profiles.bot.scopes", "read-only")
	out := h.mustRun("profile", "list")
	if !strings.Contains(out, "bot") || !strings.Contains(out, "colorkrew.com") {
		t.Errorf("profile list = %q", out)
	}
	if !strings.Contains(out, "me *") {
		t.Errorf("the default profile is not marked: %q", out)
	}
	h.mustRun("profile", "use", "bot")
	out = h.mustRun("profile", "list")
	if !strings.Contains(out, "bot *") {
		t.Errorf("default profile did not move: %q", out)
	}
	if got := strings.TrimSpace(h.mustRun("config", "get", "default_profile")); got != "bot" {
		t.Errorf("default_profile = %q, want bot", got)
	}
	err := h.run("profile", "use", "ghost")
	h.wantCode(err, output.CodeUsage)

	out = h.mustRun("profile", "list", "--json")
	var payload struct {
		Profiles []profileRow `json:"profiles"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("profile list --json: %v", err)
	}
	if len(payload.Profiles) != 1 || !payload.Profiles[0].Default || payload.Profiles[0].Name != "bot" {
		t.Errorf("profiles = %+v", payload.Profiles)
	}
}

func TestCacheInfoAndClear(t *testing.T) {
	h := newHarness(t)
	h.mustRun("config", "set", "default_profile", "me")
	paths, err := h.app.Paths()
	if err != nil {
		t.Fatal(err)
	}
	// Seed every kind of local data, including the secrets that must survive.
	must(t, store.WriteFile(paths.EntityCacheFile(), []byte(`{"names":{}}`)))
	must(t, store.WriteFile(paths.AliasesFile(), []byte("boss = \"alice@example.com\"\n")))
	must(t, store.WriteFile(filepath.Join(paths.AISessionsDir(), "s1.jsonl"), []byte("{}\n")))
	must(t, store.WriteFile(paths.AIMemoryFile(), []byte("facts = []\n")))
	must(t, store.WriteFile(paths.TokenFile(), []byte("ciphertext")))
	must(t, store.WriteFile(paths.TokenFilePlain(), []byte("plaintext")))
	must(t, store.WriteFile(paths.AuthMetadataFile(), []byte("{}")))

	out := h.mustRun("cache", "info")
	for _, want := range []string{"entity cache", "token cache", "aliases", "AI history"} {
		if !strings.Contains(out, want) {
			t.Errorf("cache info lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "age") && !strings.Contains(out, "AGE") {
		t.Errorf("cache info has no age column:\n%s", out)
	}

	// The default clear removes the rebuildable cache only.
	h.mustRun("cache", "clear")
	if store.Scan(paths.EntityCacheFile(), false).Exists {
		t.Error("entity cache survived cache clear")
	}
	for _, keep := range []string{paths.TokenFile(), paths.TokenFilePlain(), paths.AuthMetadataFile(), paths.AliasesFile()} {
		if !store.Scan(keep, false).Exists {
			t.Errorf("cache clear deleted %s", keep)
		}
	}

	// --ai removes history too, and needs confirmation when not on a TTY.
	must(t, store.WriteFile(paths.EntityCacheFile(), []byte(`{}`)))
	err = h.run("cache", "clear", "--ai")
	h.wantCode(err, output.CodeUsage)
	if !strings.Contains(output.HintOf(err), "--yes") {
		t.Errorf("hint = %q", output.HintOf(err))
	}
	if !store.Scan(paths.AIDir(), true).Exists {
		t.Error("state was deleted without confirmation")
	}
	h.mustRun("cache", "clear", "--ai", "--yes")
	if store.Scan(paths.AIDir(), true).Exists {
		t.Error("--ai did not remove the AI directory")
	}
	if !store.Scan(paths.TokenFile(), false).Exists {
		t.Error("--ai deleted the token cache")
	}

	// --all keeps only config and secrets.
	must(t, store.WriteFile(paths.AliasesFile(), []byte("x = 1\n")))
	out = h.mustRun("cache", "clear", "--all", "--yes")
	if store.Scan(paths.AliasesFile(), false).Exists {
		t.Error("--all kept aliases")
	}
	for _, keep := range []string{paths.TokenFile(), paths.TokenFilePlain(), paths.AuthMetadataFile()} {
		if !store.Scan(keep, false).Exists {
			t.Errorf("--all deleted the secret %s", keep)
		}
	}
	if !strings.Contains(out, "cleared") {
		t.Errorf("output = %q", out)
	}
	if !store.Scan(h.app.ConfigPath(), false).Exists {
		t.Error("--all deleted the config file")
	}
}

func TestCacheClearNamesAndJSON(t *testing.T) {
	h := newHarness(t)
	paths, err := h.app.Paths()
	if err != nil {
		t.Fatal(err)
	}
	must(t, store.WriteFile(paths.EntityCacheFile(), []byte(`{}`)))
	must(t, store.WriteFile(filepath.Join(paths.CacheDir, "search.json"), []byte(`{}`)))
	h.mustRun("cache", "clear", "--names")
	if store.Scan(paths.EntityCacheFile(), false).Exists {
		t.Error("--names did not remove the entity cache")
	}
	if !store.Scan(filepath.Join(paths.CacheDir, "search.json"), false).Exists {
		t.Error("--names removed more than the entity cache")
	}
	out := h.mustRun("cache", "clear", "--json")
	var payload struct {
		Removed []string `json:"removed"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("cache clear --json: %v (%q)", err, out)
	}
	if len(payload.Removed) == 0 {
		t.Error("json reported nothing removed")
	}
}

func TestDeletionTargetsNeverTouchSecrets(t *testing.T) {
	root := t.TempDir()
	t.Setenv(store.EnvConfig, filepath.Join(root, "config.toml"))
	t.Setenv(store.EnvState, filepath.Join(root, "state"))
	t.Setenv(store.EnvCache, filepath.Join(root, "cache"))
	paths, err := store.Resolve("bot")
	if err != nil {
		t.Fatal(err)
	}
	must(t, store.WriteFile(paths.TokenFile(), []byte("x")))
	must(t, store.WriteFile(paths.AuthMetadataFile(), []byte("{}")))
	must(t, store.WriteFile(paths.AliasesFile(), []byte("x")))
	must(t, store.WriteFile(paths.SyncFile()+".lockfile", []byte("lock")))
	plan, err := deletionTargets(paths, false, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range plan.Targets {
		if isSecretFile(target) {
			t.Errorf("plan deletes a secret: %s", target)
		}
		if strings.HasSuffix(target, ".lock") || strings.HasSuffix(target, ".lockfile") {
			t.Errorf("plan deletes a lock file: %s", target)
		}
	}
	if !plan.State {
		t.Error("--all must be marked as a state deletion (it needs confirmation)")
	}
	if len(plan.Protected) == 0 {
		t.Error("plan does not report what it protected")
	}
	// --names must never be a state deletion.
	plan, err = deletionTargets(paths, true, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.State {
		t.Error("--names needs no confirmation")
	}
}

func TestAuthStatusWithoutAccount(t *testing.T) {
	h := newHarness(t)
	err := h.run("auth", "status")
	h.wantCode(err, output.CodeAuth)
	out := h.stdout.String()
	if !strings.Contains(out, "signed in:") || !strings.Contains(out, "no\n") {
		t.Errorf("status output = %q", out)
	}
	if !strings.Contains(out, "requested scopes") {
		t.Errorf("status output lacks the scope list: %q", out)
	}

	err = h.run("auth", "status", "--json")
	h.wantCode(err, output.CodeAuth)
	var payload map[string]any
	if jsonErr := json.Unmarshal(h.stdout.Bytes(), &payload); jsonErr != nil {
		t.Fatalf("auth status --json: %v (%q)", jsonErr, h.stdout.String())
	}
	if payload["signed_in"] != false {
		t.Errorf("payload = %v", payload)
	}
}

func TestAuthStatusAdminRequest(t *testing.T) {
	h := newHarness(t)
	err := h.run("auth", "status", "--admin-request")
	h.wantCode(err, output.CodeAuth)
	out := h.stdout.String()
	for _, want := range []string{"adminconsent", "Mobile and desktop", "Allow public client flows", "http://localhost"} {
		if !strings.Contains(out, want) {
			t.Errorf("admin request lacks %q:\n%s", want, out)
		}
	}
}

func TestAuthStatusWithEnvToken(t *testing.T) {
	h := newHarness(t)
	h.useAccessToken(t, "User.Read Chat.Read")
	out := h.mustRun("auth", "status")
	if !strings.Contains(out, "TEAMS_ACCESS_TOKEN") {
		t.Errorf("status does not mention the escape hatch: %q", out)
	}
	if !strings.Contains(out, "granted scopes:") || !strings.Contains(out, "Chat.Read User.Read") {
		t.Errorf("status scopes = %q", out)
	}
	if !strings.Contains(out, "preset \"chats\": missing") {
		t.Errorf("status does not compare presets: %q", out)
	}
}

func TestAuthLogoutWithoutAccount(t *testing.T) {
	h := newHarness(t)
	h.mustRun("auth", "logout", "--yes")
	if !strings.Contains(h.stdout.String(), "signed out") {
		t.Errorf("output = %q", h.stdout.String())
	}
	err := h.run("auth", "logout")
	h.wantCode(err, output.CodeUsage)
}

func TestWhoamiReadsTheGraph(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.0/me" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("$select"); !strings.Contains(got, "displayName") {
			t.Errorf("$select = %q", got)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Error("no bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"user-1","displayName":"Alice Example","userPrincipalName":"alice@example.com"}`)
	}))
	defer srv.Close()

	h := newHarness(t)
	h.graphURL = srv.URL + "/v1.0"
	h.useAccessToken(t, "User.Read")
	out := h.mustRun("whoami")
	if !strings.Contains(out, "Alice Example") || !strings.Contains(out, "alice@example.com") {
		t.Errorf("whoami = %q", out)
	}

	h.graphURL = srv.URL + "/v1.0"
	h.useAccessToken(t, "User.Read")
	out = h.mustRun("whoami", "--json")
	var me Me
	if err := json.Unmarshal([]byte(out), &me); err != nil {
		t.Fatalf("whoami --json: %v (%q)", err, out)
	}
	if me.ID != "user-1" || me.DisplayName != "Alice Example" {
		t.Errorf("me = %+v", me)
	}
}

func TestWhoamiScopePreCheckFailsBeforeTheCall(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	h := newHarness(t)
	h.graphURL = srv.URL + "/v1.0"
	// A token without User.Read must fail with exit 3 and never reach Graph.
	h.useAccessToken(t, "Chat.Read")
	h.app.Hooks.GrantedScopes = []string{"Chat.Read"}
	err := h.run("whoami")
	h.wantCode(err, output.CodeAuth)
	if calls != 0 {
		t.Errorf("whoami called Graph %d times despite a missing scope", calls)
	}
	if hint := output.HintOf(err); !strings.Contains(hint, "app registration") && !strings.Contains(hint, "consent") {
		t.Errorf("hint = %q", hint)
	}
}

func TestWhoamiPropagatesGraphErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"error":{"code":"Authorization_RequestDenied","message":"Insufficient privileges"}}`)
	}))
	defer srv.Close()

	h := newHarness(t)
	h.graphURL = srv.URL + "/v1.0"
	h.useAccessToken(t, "User.Read")
	err := h.run("whoami")
	h.wantCode(err, output.CodeAuth)
	if !strings.Contains(err.Error(), "Authorization_RequestDenied") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(output.HintOf(err), "consent") {
		t.Errorf("hint = %q", output.HintOf(err))
	}
}

func TestDoctorOffline(t *testing.T) {
	h := newHarness(t)
	h.useAccessToken(t, "User.Read Chat.Read")
	err := h.run("doctor", "--offline")
	h.wantCode(err, output.CodeOK)
	out := h.stdout.String()
	for _, want := range []string{"config file", "profile", "sign-in", "graph"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "skipped (--offline)") {
		t.Errorf("doctor did not skip the network checks:\n%s", out)
	}

	h.stdout.Reset()
	h.stderr.Reset()
	err = h.run("doctor", "--offline", "--json")
	h.wantCode(err, output.CodeOK)
	var payload struct {
		Checks []doctorCheck `json:"checks"`
	}
	if jsonErr := json.Unmarshal(h.stdout.Bytes(), &payload); jsonErr != nil {
		t.Fatalf("doctor --json: %v", jsonErr)
	}
	if len(payload.Checks) < 4 {
		t.Errorf("checks = %+v", payload.Checks)
	}
}

func TestDoctorReportsAMissingAccount(t *testing.T) {
	h := newHarness(t)
	err := h.run("doctor", "--offline")
	h.wantCode(err, output.CodeAuth)
	if !strings.Contains(h.stdout.String(), "fail") {
		t.Errorf("doctor did not report a failure:\n%s", h.stdout.String())
	}
	if !strings.Contains(h.stderr.String(), "fix (sign-in)") {
		t.Errorf("doctor printed no fix:\n%s", h.stderr.String())
	}
}

func TestDoctorFeatureMatrixWithPartialScopes(t *testing.T) {
	h := newHarness(t)
	h.useAccessToken(t, "User.Read")
	out := h.mustRun("doctor", "--offline")
	if !strings.Contains(out, "features available") {
		t.Errorf("no availability row:\n%s", out)
	}
	if !strings.Contains(h.stderr.String(), "features not granted") && !strings.Contains(out, "features not granted") {
		t.Errorf("doctor did not list the missing features:\n%s\n%s", out, h.stderr.String())
	}
}

func TestReadOnlyGuardRecognizesWriteCommands(t *testing.T) {
	cases := map[string]bool{
		"post": true, "reply": true, "edit": true, "delete": true, "react": true,
		"logout": true, "create": true, "add-member": true, "mark-read": true,
		"auth": false, "cache": false, "whoami": false, "doctor": false, "profile": false,
	}
	for name, want := range cases {
		cmd := &cobra.Command{Use: name}
		if got := isWriteCommand(cmd); got != want {
			t.Errorf("isWriteCommand(%q) = %v, want %v", name, got, want)
		}
	}
	// A write command nested under a noun is still recognized.
	parent := &cobra.Command{Use: "chat"}
	child := &cobra.Command{Use: "delete"}
	parent.AddCommand(child)
	if !isWriteCommand(child) {
		t.Error("nested write command not detected")
	}
}

func TestGlobalFlagsReachThePrinter(t *testing.T) {
	h := newHarness(t)
	h.mustRun("version", "--no-color", "--quiet")
	if h.app.Printer.Color() {
		t.Error("--no-color did not disable colour")
	}
	if !h.app.Printer.Quiet() {
		t.Error("--quiet was not applied")
	}
	if h.app.Printer.Interactive() {
		t.Error("CI runs must not be interactive")
	}
}

func TestReadOnlyFlagBlocksWriteCommands(t *testing.T) {
	h := newHarness(t)
	// No write command exists yet in Phase 2, so assert the guard directly on a
	// fabricated one through the command tree.
	root := h.app.newRootCmd()
	fake := &cobra.Command{Use: "post", RunE: func(*cobra.Command, []string) error { return nil }}
	root.AddCommand(fake)
	root.SetArgs([]string{"--read-only", "post"})
	err := root.ExecuteContext(context.Background())
	h.wantCode(err, output.CodeUsage)
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("error = %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
