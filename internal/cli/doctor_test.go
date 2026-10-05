package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
)

// doctorRows decodes the JSON report for assertions.
func doctorRows(t *testing.T, out string) []doctorCheck {
	t.Helper()
	var payload struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("doctor --json: %v (%q)", err, out)
	}
	return payload.Checks
}

func findCheck(checks []doctorCheck, name string) (doctorCheck, bool) {
	for _, c := range checks {
		if c.Name == name {
			return c, true
		}
	}
	return doctorCheck{}, false
}

func TestDoctorWarnsAboutTheKeyVaultGap(t *testing.T) {
	h := newHarness(t)
	h.mustRun("config", "set", "profiles.me.token_store", "keyvault://kv/secret")
	h.mustRun("config", "set", "profiles.me.tenant", "colorkrew.com")
	err := h.run("doctor", "--offline", "--json")
	// The sign-in check cannot open the store, so doctor fails; the token-store
	// row must explain the Phase 8 gap rather than crashing.
	h.wantCode(err, output.CodeError)
	checks := doctorRows(t, h.stdout.String())
	storeCheck, ok := findCheck(checks, "token store")
	if !ok {
		t.Fatalf("no token store check in %+v", checks)
	}
	if storeCheck.Status != statusWarn || !strings.Contains(storeCheck.Detail, "keyvault://") {
		t.Errorf("token store check = %+v", storeCheck)
	}
	if !strings.Contains(storeCheck.Fix, "Phase 8") {
		t.Errorf("fix = %q, want the phase pointer", storeCheck.Fix)
	}
}

func TestDoctorReportsClockSkew(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// The harness clock is 2026-10-04T12:00Z; a server date two hours later
		// is a skew well past the tolerance.
		w.Header().Set("Date", harnessNow.Add(2*time.Hour).Format(http.TimeFormat))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"user-1"}`))
	}))
	defer srv.Close()

	h := newHarness(t)
	h.graphURL = srv.URL + "/v1.0"
	h.useAccessToken(t, "User.Read")
	if err := h.run("doctor"); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	checks := doctorRows(t, h.mustRun("doctor", "--json"))
	skew, ok := findCheck(checks, "clock skew")
	if !ok {
		t.Fatalf("no clock skew check in %+v", checks)
	}
	if skew.Status != statusWarn {
		t.Errorf("clock skew = %+v, want a warning", skew)
	}
	if !strings.Contains(skew.Fix, "system clock") {
		t.Errorf("fix = %q", skew.Fix)
	}
	reach, ok := findCheck(checks, "graph")
	if !ok || reach.Status != statusOK {
		t.Errorf("graph check = %+v", reach)
	}
}

func TestDoctorReportsAnUnreachableGraph(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	url := srv.URL + "/v1.0"
	srv.Close() // nothing is listening any more

	h := newHarness(t)
	h.graphURL = url
	h.useAccessToken(t, "User.Read")
	err := h.run("doctor")
	if err == nil {
		t.Fatal("doctor succeeded with an unreachable Graph")
	}
	if output.CodeOf(err) != output.CodeError {
		t.Errorf("exit code = %d, want 1 (a transport failure)", output.CodeOf(err))
	}
	if err := h.run("doctor", "--json"); err == nil {
		t.Fatal("doctor --json should exit non-zero for an unreachable Graph")
	}
	checks := doctorRows(t, h.stdout.String())
	graphCheck, ok := findCheck(checks, "graph")
	if !ok || graphCheck.Status != statusFail {
		t.Errorf("graph check = %+v", graphCheck)
	}
	if graphCheck.Fix == "" {
		t.Errorf("a failed graph check must suggest a fix: %+v", graphCheck)
	}
	if skew, ok := findCheck(checks, "clock skew"); ok && !strings.Contains(skew.Detail, "not checked") {
		t.Errorf("clock skew = %+v, want it skipped when Graph is unreachable", skew)
	}
}

func TestDoctorFeatureMatrixListsAvailableCommands(t *testing.T) {
	h := newHarness(t)
	h.useAccessToken(t, strings.Join(config.Presets[config.ScopePresetFull], " "))
	out := h.mustRun("doctor", "--offline")
	if !strings.Contains(out, "features available") {
		t.Fatalf("doctor output lacks the feature rows:\n%s", out)
	}
	checks := doctorRows(t, h.mustRun("doctor", "--offline", "--json"))
	summary, ok := findCheck(checks, "features available")
	if !ok {
		t.Fatalf("no feature check in %+v", checks)
	}
	if !strings.Contains(summary.Detail, "of") {
		t.Errorf("the summary row should count the commands: %s", summary.Detail)
	}
	// The long list is behind -v.
	commands, ok := findCheck(checks, "commands")
	if ok {
		t.Fatalf("the command list is printed without -v: %+v", commands)
	}
	checks = doctorRows(t, h.mustRun("doctor", "--offline", "--json", "-v"))
	commands, ok = findCheck(checks, "commands")
	if !ok {
		t.Fatalf("no command list in %+v", checks)
	}
	for _, want := range []string{"teams whoami", "teams channel read", "teams post <channel>"} {
		if !strings.Contains(commands.Detail, want) {
			t.Errorf("available features lack %q: %s", want, commands.Detail)
		}
	}
	// The only feature no preset unlocks is the incremental one.
	notGranted, ok := findCheck(checks, "features not granted")
	if !ok {
		t.Fatal("chat delete needs Chat.ManageDeletion.All, so it must be listed as missing")
	}
	if !strings.Contains(notGranted.Detail, "teams chat delete") {
		t.Errorf("missing features = %s", notGranted.Detail)
	}
	for _, unexpected := range []string{"teams whoami", "teams post <channel>"} {
		if strings.Contains(notGranted.Detail, unexpected) {
			t.Errorf("%q is reported as missing although the full preset has its scope: %s", unexpected, notGranted.Detail)
		}
	}
}

func TestDoctorWarnsAboutTheDefaultClientID(t *testing.T) {
	h := newHarness(t)
	h.useAccessToken(t, "User.Read")
	checks := doctorRows(t, h.mustRun("doctor", "--offline", "--json"))
	profile, ok := findCheck(checks, "profile")
	if !ok {
		t.Fatalf("no profile check in %+v", checks)
	}
	if profile.Status != statusWarn || !strings.Contains(profile.Fix, "client_id") {
		t.Errorf("profile check = %+v, want a warning about the default client", profile)
	}
}

func TestDoctorReportsAnUnparseableServerDate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Date", "not a date")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"user-1"}`))
	}))
	defer srv.Close()

	h := newHarness(t)
	h.graphURL = srv.URL + "/v1.0"
	h.useAccessToken(t, "User.Read")
	checks := doctorRows(t, h.mustRun("doctor", "--json"))
	skew, ok := findCheck(checks, "clock skew")
	if !ok || skew.Status != statusWarn {
		t.Errorf("clock skew = %+v", skew)
	}
	if !strings.Contains(skew.Detail, "unparseable") {
		t.Errorf("detail = %q", skew.Detail)
	}
}

func TestDoctorNeverWritesToGraph(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"user-1"}`))
	}))
	defer srv.Close()

	h := newHarness(t)
	h.graphURL = srv.URL + "/v1.0"
	h.useAccessToken(t, "User.Read Chat.Read Chat.ReadWrite")
	if err := h.run("doctor"); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	for _, method := range methods {
		if method != http.MethodGet {
			t.Errorf("doctor issued a %s request; it must make no write calls", method)
		}
	}
	if len(methods) == 0 {
		t.Error("doctor never reached Graph, so reachability was not checked")
	}
}

func TestDoctorOfflineSkipsTheNetworkEntirely(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("doctor --offline called Graph")
	}))
	defer srv.Close()

	h := newHarness(t)
	h.graphURL = srv.URL + "/v1.0"
	h.useAccessToken(t, "User.Read")
	if err := h.run("doctor", "--offline"); err != nil {
		t.Fatalf("doctor --offline: %v", err)
	}
}

func TestConfirmPromptsOnlyWithATerminal(t *testing.T) {
	h := newHarness(t)
	// Non-interactive: a prompt must be a usage error, never a hang.
	err := h.app.confirm("delete everything?", false)
	if output.CodeOf(err) != output.CodeUsage {
		t.Fatalf("confirm = %v, want a usage error", err)
	}
	if !strings.Contains(output.HintOf(err), "--yes") {
		t.Errorf("hint = %q", output.HintOf(err))
	}
	// --yes short-circuits even without a terminal.
	if err := h.app.confirm("delete everything?", true); err != nil {
		t.Errorf("confirm with --yes = %v", err)
	}
	// A terminal that answers "y" proceeds; anything else aborts.
	h.app.Printer = output.New(output.Options{Out: &h.stdout, Err: &h.stderr, Interactive: true})
	h.app.Stdin = strings.NewReader("y\n")
	if err := h.app.confirm("continue?", false); err != nil {
		t.Errorf("confirm with a yes = %v", err)
	}
	h.app.Stdin = strings.NewReader("n\n")
	if err := h.app.confirm("continue?", false); err == nil {
		t.Error("confirm accepted a no")
	}
	h.app.Stdin = strings.NewReader("")
	if err := h.app.confirm("continue?", false); err == nil {
		t.Error("confirm accepted an empty answer")
	}
}

func TestReadOnlyFlagBlocksWriteCommandFromEnv(t *testing.T) {
	h := newHarness(t)
	t.Setenv(config.EnvReadOnly, "1")
	h.useProcessEnv()
	err := h.run("auth", "login")
	h.wantCode(err, output.CodeUsage)
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("err = %v", err)
	}
}

func TestAppAccessors(t *testing.T) {
	h := newHarness(t)
	eff, err := h.app.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if eff.Name != "me" {
		t.Errorf("Effective = %+v", eff)
	}
	if h.app.ConfigPath() != h.app.Hooks.ConfigPath {
		t.Errorf("ConfigPath = %q, want the hook", h.app.ConfigPath())
	}
	if !h.app.ConfigFileExists() {
		// The harness writes a config only when a command sets something.
		h.mustRun("config", "set", "default_profile", "me")
		if !h.app.ConfigFileExists() {
			t.Error("ConfigFileExists is false after writing the file")
		}
	}
	client, err := h.app.Auth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if client == nil {
		t.Fatal("Auth returned no client")
	}
	// The client is cached: a second call must not rebuild it.
	again, err := h.app.Auth(context.Background())
	if err != nil || again != client {
		t.Errorf("Auth is not cached: %v", err)
	}
	if _, err := h.app.Graph(context.Background()); err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if _, err := h.app.Graph(context.Background()); err != nil {
		t.Errorf("Graph is not cached: %v", err)
	}
}

func TestForProfileReturnsAnIndependentApp(t *testing.T) {
	h := newHarness(t)
	h.mustRun("config", "set", "profiles.bot.tenant", "colorkrew.com")
	clone := h.app.forProfile("bot")
	eff, err := clone.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if eff.Name != "bot" || eff.Tenant != "colorkrew.com" {
		t.Errorf("clone = %+v", eff)
	}
	original, err := h.app.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if original.Name != "me" {
		t.Errorf("the original App changed profile: %+v", original)
	}
}
