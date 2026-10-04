package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/store"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMissingFileYieldsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultProfile != DefaultProfileName {
		t.Errorf("DefaultProfile = %q, want %q", cfg.DefaultProfile, DefaultProfileName)
	}
	eff, err := cfg.Resolve(ResolveInput{Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Name != "me" || eff.Tenant != DefaultTenant || eff.ClientID != DefaultClientID ||
		eff.Cloud != "global" || eff.TokenStore != "auto" || eff.ReadOnly {
		t.Errorf("unexpected defaults: %+v", eff)
	}
	if len(eff.Scopes) != len(Presets[ScopePresetFull]) {
		t.Errorf("default scopes = %v, want the full preset", eff.Scopes)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg := Default()
	if err := cfg.SetProfile("bot", Profile{
		Tenant:     "colorkrew.com",
		ClientID:   "11111111-2222-3333-4444-555555555555",
		TokenStore: "keyvault://kv-teams-cli/teams-bot-cache",
		Scopes:     "read-only",
	}); err != nil {
		t.Fatal(err)
	}
	cfg.AI = AI{Provider: "anthropic", Model: "claude-sonnet-5-5"}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := back.Profiles["bot"]
	if got.Tenant != "colorkrew.com" || got.Scopes != "read-only" {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if back.AI.Model != "claude-sonnet-5-5" {
		t.Errorf("AI section lost: %+v", back.AI)
	}
	if err := back.Validate(); err != nil {
		t.Errorf("reloaded config does not validate: %v", err)
	}
}

func TestSaveIsPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	if err := Default().Save(path); err != nil {
		t.Fatal(err)
	}
	// Windows has no POSIX mode bits: Go maps 0600 onto the file's ACL there, so
	// Mode().Perm() reads back as 0666 and the assertion below cannot hold. What
	// protects the config on Windows is the ACL of the user profile it lives in.
	// The other mode assertions in this repo are guarded the same way
	// (internal/store, internal/auth/tokenstore).
	if runtime.GOOS == "windows" {
		t.Skip("windows has no POSIX file modes; the config file's ACL is what protects it")
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != store.FileMode {
		t.Errorf("config mode = %v, want %v", perm, store.FileMode)
	}
}

func TestLoadRejectsBadTOML(t *testing.T) {
	path := writeConfig(t, "this is not = = toml")
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted broken TOML")
	}
}

func TestProfileValidation(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		wantErr string
	}{
		{"ok", Profile{Mode: ModeFull, Cloud: "global", TokenStore: "auto", Scopes: "full"}, ""},
		{"bad mode", Profile{Mode: "chatty"}, "mode must be"},
		{"bad cloud", Profile{Cloud: "moon"}, "cloud must be one of"},
		{"bad store", Profile{TokenStore: "s3://bucket"}, "token_store must be"},
		{"bad keyvault", Profile{TokenStore: "keyvault://vaultonly"}, "keyvault://<vault>/<secret>"},
		{"empty file url", Profile{TokenStore: "file://"}, "needs a path"},
		{"bad scopes", Profile{Scopes: "full,nope!"}, "not a Graph scope name"},
		{"offline_access", Profile{Scopes: "User.Read offline_access"}, "added by MSAL"},
		{"file url ok", Profile{TokenStore: "file:///data/cache.json"}, ""},
		{"file ok", Profile{TokenStore: "file"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.profile.Validate("me")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
	if err := (Profile{}).Validate("bad/name"); err == nil {
		t.Error("Validate accepted an invalid profile name")
	}
}

func TestResolveProfilePrecedence(t *testing.T) {
	cfg := &Config{
		DefaultProfile: "work",
		Profiles: map[string]Profile{
			"work":  {Tenant: "work.example"},
			"other": {Tenant: "other.example"},
		},
	}
	cases := []struct {
		name string
		in   ResolveInput
		want string
	}{
		{"flag wins", ResolveInput{ProfileFlag: "other", Environ: []string{"TEAMS_PROFILE=work"}}, "other"},
		{"env beats config", ResolveInput{Environ: []string{"TEAMS_PROFILE=other"}}, "other"},
		{"config default", ResolveInput{Environ: []string{}}, "work"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eff, err := cfg.Resolve(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if eff.Name != tc.want {
				t.Errorf("profile = %q, want %q", eff.Name, tc.want)
			}
		})
	}
}

func TestResolveFallsBackToBuiltInMeProfile(t *testing.T) {
	cfg := Default()
	eff, err := cfg.Resolve(ResolveInput{Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Name != "me" || eff.Tenant != DefaultTenant {
		t.Errorf("built-in me profile not applied: %+v", eff)
	}
}

func TestResolveUnknownProfileIsAnError(t *testing.T) {
	cfg := Default()
	_, err := cfg.Resolve(ResolveInput{ProfileFlag: "ghost", Environ: []string{}})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Resolve = %v, want a not-configured error", err)
	}
	if _, err := cfg.Resolve(ResolveInput{ProfileFlag: "../escape", Environ: []string{}}); err == nil {
		t.Fatal("Resolve accepted a path-traversal profile name")
	}
}

func TestReadOnlyComesFromModeEnvOrFlag(t *testing.T) {
	cases := []struct {
		name    string
		profile Profile
		in      ResolveInput
		want    bool
	}{
		{"mode", Profile{Mode: ModeReadOnly}, ResolveInput{Environ: []string{}}, true},
		{"env", Profile{}, ResolveInput{Environ: []string{"TEAMS_READ_ONLY=1"}}, true},
		{"env off", Profile{}, ResolveInput{Environ: []string{"TEAMS_READ_ONLY=0"}}, false},
		{"flag", Profile{}, ResolveInput{ReadOnlyFlag: true, Environ: []string{}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{Profiles: map[string]Profile{"me": tc.profile}}
			eff, err := cfg.Resolve(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if eff.ReadOnly != tc.want {
				t.Errorf("ReadOnly = %v, want %v", eff.ReadOnly, tc.want)
			}
			if tc.want && len(Scopes.Missing(eff.Scopes, Presets[ScopePresetReadOnly])) > 0 {
				t.Errorf("read-only profile requested %v, which is not the read-only preset", eff.Scopes)
			}
		})
	}
}

// TestReadOnlyNeverReplacesAnExplicitScopeList is the regression test for a
// reported bug: `teams --read-only` on a profile with an explicit `scopes` list
// replaced that list with the read-only preset, whose extra scopes
// (Files.Read.All, User.ReadBasic.All, People.Read) the app registration was not
// consented for. Consent is all-or-nothing per request, so the sign-in failed
// with AADSTS65001 and *every* command broke, including `teams whoami`, while the
// same commands worked without the flag.
func TestReadOnlyNeverReplacesAnExplicitScopeList(t *testing.T) {
	configured := "User.Read Chat.Read ChannelMessage.Read.All"
	cfg := &Config{Profiles: map[string]Profile{"me": {Scopes: configured}}}
	eff, err := cfg.Resolve(ResolveInput{ReadOnlyFlag: true, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(eff.Scopes, " "); got != configured {
		t.Errorf("Scopes = %q, want the configured list untouched (%q)", got, configured)
	}
	if eff.ScopesSpec != configured {
		t.Errorf("ScopesSpec = %q, want what the user configured", eff.ScopesSpec)
	}
	// The whole point: nothing was added, so nothing can fail consent.
	for _, scope := range []string{"Files.Read.All", "User.ReadBasic.All", "People.Read"} {
		if Scopes.Missing(eff.Scopes, []string{scope}) == nil {
			t.Errorf("read-only added %s to an explicit scope list", scope)
		}
	}
}

// TestReadOnlyNarrowsPresetsWithoutWidening: a preset is intersected with the
// read-only preset rather than replaced by it, so a `chats` profile never grows
// the channel and file scopes it was not asking for.
func TestReadOnlyNarrowsPresetsWithoutWidening(t *testing.T) {
	cases := []struct {
		spec string
		want string
	}{
		{"", strings.Join(Presets[ScopePresetReadOnly], " ")},
		{ScopePresetFull, strings.Join(Presets[ScopePresetReadOnly], " ")},
		{ScopePresetReadOnly, strings.Join(Presets[ScopePresetReadOnly], " ")},
		{ScopePresetChats, "User.Read User.ReadBasic.All People.Read Chat.ReadBasic Chat.Read"},
	}
	for _, tc := range cases {
		t.Run("scopes="+tc.spec, func(t *testing.T) {
			cfg := &Config{Profiles: map[string]Profile{"me": {Scopes: tc.spec}}}
			eff, err := cfg.Resolve(ResolveInput{ReadOnlyFlag: true, Environ: []string{}})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(eff.Scopes, " "); got != tc.want {
				t.Errorf("Scopes = %q, want %q", got, tc.want)
			}
			// Whatever the preset, the narrowed request stays inside it.
			configured, err := Scopes.For(tc.spec)
			if err != nil {
				t.Fatal(err)
			}
			if missing := Scopes.Missing(configured, eff.Scopes); len(missing) > 0 {
				t.Errorf("read-only added %v, which the configured preset does not carry", missing)
			}
		})
	}
}

// TestWriteScopesAreNotReadScopes guards the invariant the narrowing rule rests
// on: a scope may only be dropped from a read-only request if the read-only
// preset does not carry it either, so narrowing can never remove the only scope
// that makes a read work.
func TestWriteScopesAreNotReadScopes(t *testing.T) {
	readOnly := make(map[string]bool, len(Presets[ScopePresetReadOnly]))
	for _, scope := range Presets[ScopePresetReadOnly] {
		readOnly[strings.ToLower(scope)] = true
	}
	for _, scope := range writeScopes {
		if !readOnly[strings.ToLower(scope)] {
			continue
		}
		t.Errorf("%s is in the read-only preset, so dropping it removes read access", scope)
	}
	// And every read scope the presets carry survives the narrowing, which is the
	// difference from intersecting with the read-only preset: Chat.ReadBasic is a
	// read scope the read-only preset does not list.
	for _, preset := range []string{ScopePresetChats, ScopePresetFull, ""} {
		configured, err := Scopes.For(preset)
		if err != nil {
			t.Fatal(err)
		}
		got := Scopes.ReadOnlyView(preset, configured)
		for _, scope := range configured {
			lower := strings.ToLower(scope)
			readOnlyPreset := readOnly[lower]
			if !readOnlyPreset && containsScope(writeScopes, scope) {
				continue
			}
			if containsScope(got, scope) {
				continue
			}
			t.Errorf("narrowing preset %q dropped %s, which is not a write scope", preset, scope)
		}
	}
}

// containsScope reports whether scopes holds scope, case-insensitively.
func containsScope(scopes []string, scope string) bool {
	for _, candidate := range scopes {
		if strings.EqualFold(candidate, scope) {
			return true
		}
	}
	return false
}

func TestReadOnlyViewEdgeCases(t *testing.T) {
	// An explicit list is returned as-is, whatever it holds.
	list := []string{"User.Read", "ChatMessage.Send"}
	if got := Scopes.ReadOnlyView("User.Read ChatMessage.Send", list); strings.Join(got, " ") != strings.Join(list, " ") {
		t.Errorf("ReadOnlyView(explicit) = %v, want the list untouched", got)
	}
	// A preset with nothing in common with the read-only set keeps its scopes
	// rather than requesting none: no preset looks like this today, and the
	// fallback documents what would happen.
	if got := Scopes.ReadOnlyView(ScopePresetFull, []string{"Mail.Send"}); len(got) != 1 || got[0] != "Mail.Send" {
		t.Errorf("ReadOnlyView(no overlap) = %v, want the configured scopes", got)
	}
}

func TestExplicitScopesWinOverMode(t *testing.T) {
	cfg := &Config{Profiles: map[string]Profile{"me": {Mode: ModeReadOnly, Scopes: "User.Read, Chat.Read"}}}
	eff, err := cfg.Resolve(ResolveInput{Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(eff.Scopes, " ") != "User.Read Chat.Read" {
		t.Errorf("Scopes = %v, want the explicit list", eff.Scopes)
	}
}

func TestParseKeyVaultURL(t *testing.T) {
	target, err := ParseKeyVaultURL("keyvault://kv-teams-cli/teams-bot-cache")
	if err != nil {
		t.Fatal(err)
	}
	if target.Vault != "kv-teams-cli" || target.Secret != "teams-bot-cache" {
		t.Errorf("parsed %+v", target)
	}
	if target.String() != "keyvault://kv-teams-cli/teams-bot-cache" {
		t.Errorf("String = %q", target.String())
	}
	for _, bad := range []string{"keyvault://vault", "keyvault:///secret", "keyvault://vault/a/b", "https://example.com"} {
		if _, err := ParseKeyVaultURL(bad); err == nil {
			t.Errorf("ParseKeyVaultURL(%q) succeeded", bad)
		}
	}
}

func TestEnvironValueAndBoolParsing(t *testing.T) {
	env := []string{"A=1", "B=true", "C=maybe"}
	if got := EnvironValue(env, "A"); got != "1" {
		t.Errorf("EnvironValue = %q", got)
	}
	if !ParseBoolEnv(env, "B") || !ParseBoolEnv(env, "C") || ParseBoolEnv(env, "Z") {
		t.Error("ParseBoolEnv mis-parsed the environment")
	}
	if EnvironValue(nil, "PATH") == "" {
		t.Error("nil environ should fall back to os.Environ()")
	}
}

func TestChinaNeedsAnExplicitGraphBase(t *testing.T) {
	// The docs mirror documents the China authority but not the Graph service
	// root, so a china profile without graph_base_url is a config error rather
	// than a guessed host.
	cfg := &Config{Profiles: map[string]Profile{"cn": {Cloud: "china"}}}
	_, err := cfg.Resolve(ResolveInput{ProfileFlag: "cn", Environ: []string{}})
	if err == nil || !strings.Contains(err.Error(), "graph_base_url") {
		t.Fatalf("Resolve = %v, want a graph_base_url error", err)
	}
	cfg.Profiles["cn"] = Profile{Cloud: "china", GraphBaseURL: "https://graph.example.cn/v1.0"}
	eff, err := cfg.Resolve(ResolveInput{ProfileFlag: "cn", Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if eff.GraphBaseURL != "https://graph.example.cn/v1.0" {
		t.Errorf("GraphBaseURL = %q", eff.GraphBaseURL)
	}
	if eff.Cloud != "china" {
		t.Errorf("Cloud = %q", eff.Cloud)
	}
}

func TestGraphBaseURLMustBeHTTPS(t *testing.T) {
	for _, bad := range []string{"http://graph.microsoft.com/v1.0", "graph.microsoft.com", ""} {
		if bad == "" {
			continue
		}
		p := Profile{GraphBaseURL: bad}
		if err := p.Validate("me"); err == nil {
			t.Errorf("Validate accepted graph_base_url %q", bad)
		}
	}
	if err := (Profile{GraphBaseURL: "https://graph.microsoft.com/v1.0"}).Validate("me"); err != nil {
		t.Errorf("Validate rejected a valid graph_base_url: %v", err)
	}
}

func TestProfileNamesSorted(t *testing.T) {
	cfg := &Config{Profiles: map[string]Profile{"zeta": {}, "alpha": {}, "me": {}}}
	got := cfg.ProfileNames()
	want := []string{"alpha", "me", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ProfileNames = %v, want %v", got, want)
		}
	}
}

func TestValidateWholeConfig(t *testing.T) {
	cfg := &Config{DefaultProfile: "bad/name", Profiles: map[string]Profile{}}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted an invalid default_profile")
	}
	cfg = &Config{Profiles: map[string]Profile{"me": {Mode: "nope"}}}
	if err := cfg.Validate(); err == nil {
		t.Error("Validate accepted an invalid mode")
	}
	if err := Default().Validate(); err != nil {
		t.Errorf("default config does not validate: %v", err)
	}
}
