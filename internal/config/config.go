// Package config loads and validates the TOML config file described in
// PLAN.md ("Profiles"): a default profile, one section per profile, and the
// environment overrides that let tests, CI and containers relocate everything.
//
// The package is deliberately free of I/O beyond the config file itself: cache
// and state directories belong to internal/store, and nothing here talks to
// Entra or Graph.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/floriscornel/teams-cli/internal/cloud"
	"github.com/floriscornel/teams-cli/internal/store"
)

// Defaults matching PLAN.md ("Profiles").
const (
	// DefaultProfileName is used when neither --profile, TEAMS_PROFILE nor
	// default_profile says otherwise.
	DefaultProfileName = "me"
	// DefaultClientID is the Microsoft Graph CLI Tools public client, the same
	// default teams-mcp ships (refs/teams-mcp/src/services/graph.ts:6).
	DefaultClientID = "14d82eec-204b-4c2f-b7e8-296a70dab67e"
	// DefaultTenant matches the MCP's `common` authority
	// (refs/teams-mcp/src/services/graph.ts:7). Users in a corporate tenant set their own.
	DefaultTenant = "common"
	// DefaultCloud is the commercial cloud.
	DefaultCloud = "global"
	// DefaultTokenStore picks the envelope store, falling back to a 0600 file
	// where no keychain is reachable.
	DefaultTokenStore = "auto"
)

// Environment variables the CLI honours. Secrets are never read from argv and
// never written to the config file.
const (
	EnvProfile     = "TEAMS_PROFILE"
	EnvReadOnly    = "TEAMS_READ_ONLY"
	EnvNoInput     = "TEAMS_NO_INPUT"
	EnvAccessToken = "TEAMS_ACCESS_TOKEN"
	EnvNoUpdate    = "TEAMS_NO_UPDATE_CHECK"
)

// Modes accepted by `mode`.
const (
	ModeFull     = "full"
	ModeReadOnly = "read-only"
)

// Token store kinds accepted by `token_store`.
const (
	TokenStoreAuto     = "auto"
	TokenStoreFile     = "file"
	TokenStoreFileURL  = "file://"
	TokenStoreKeyVault = "keyvault://"
)

// AI holds the opt-in AI settings (Phase 6). Only the fields that gate the
// feature are defined now; `teams ai setup` owns the rest.
type AI struct {
	Provider string `toml:"provider,omitempty"`
	Model    string `toml:"model,omitempty"`
	Language string `toml:"language,omitempty"`
}

// Profile is one identity: your own account (`me`) or a service account (`bot`).
type Profile struct {
	// Tenant is a domain, a GUID, or one of common/consumers/organizations.
	Tenant string `toml:"tenant,omitempty"`
	// ClientID is the app registration; empty means the Graph CLI Tools client.
	ClientID string `toml:"client_id,omitempty"`
	// Mode is full or read-only.
	Mode string `toml:"mode,omitempty"`
	// Cloud is global, usgov or china.
	Cloud string `toml:"cloud,omitempty"`
	// GraphBaseURL overrides the cloud's Graph service root. It is required for
	// `cloud = "china"`, whose Graph host the docs mirror does not state
	// (internal/cloud documents the gap), and it is what the in-process tests
	// use to point the client at fakegraph.
	GraphBaseURL string `toml:"graph_base_url,omitempty"`
	// TokenStore is auto, file, file:///path or keyvault://vault/secret.
	TokenStore string `toml:"token_store,omitempty"`
	// Scopes is a preset name (chats, read-only, full) or an explicit list.
	Scopes string `toml:"scopes,omitempty"`
	// HomeAccountID is the MSAL account key recorded at login so silent
	// acquisition has an account without a full cache read (PLAN.md:92).
	HomeAccountID string `toml:"home_account_id,omitempty"`
}

// Config is the whole config file.
type Config struct {
	DefaultProfile string             `toml:"default_profile,omitempty"`
	Profiles       map[string]Profile `toml:"profiles,omitempty"`
	AI             AI                 `toml:"ai,omitempty"`
	UpdateCheck    *bool              `toml:"update_check,omitempty"`
}

// Default returns the config a fresh install behaves as if it had: one `me`
// profile with the documented defaults.
func Default() *Config {
	return &Config{
		DefaultProfile: DefaultProfileName,
		Profiles:       map[string]Profile{},
	}
}

// Load reads the config file. A missing file is not an error: it yields the
// defaults, so `teams auth login` works before anything is configured.
func Load(path string) (*Config, error) {
	data, err := store.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := Default()
	if len(data) == 0 {
		return cfg, nil
	}
	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	if cfg.DefaultProfile == "" {
		cfg.DefaultProfile = DefaultProfileName
	}
	return cfg, nil
}

// Save writes the config file atomically as 0600. Secrets are never stored
// here: the config holds metadata and identifiers only.
func (c *Config) Save(path string) error {
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return store.WriteFile(path, data)
}

// SetProfile stores a profile, creating the map when needed.
func (c *Config) SetProfile(name string, p Profile) error {
	if !store.ValidProfileName(name) {
		return fmt.Errorf("invalid profile name %q", name)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]Profile{}
	}
	if err := p.Validate(name); err != nil {
		return err
	}
	c.Profiles[name] = p
	return nil
}

// ProfileNames returns the configured profile names, sorted.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Validate checks one profile's fields.
func (p Profile) Validate(name string) error {
	if name != "" && !store.ValidProfileName(name) {
		return fmt.Errorf("invalid profile name %q: use letters, digits, dot, dash or underscore", name)
	}
	if p.Mode != "" && p.Mode != ModeFull && p.Mode != ModeReadOnly {
		return fmt.Errorf("profile %q: mode must be %q or %q, got %q", name, ModeFull, ModeReadOnly, p.Mode)
	}
	if p.Cloud != "" {
		if _, ok := cloud.Lookup(p.Cloud); !ok {
			return fmt.Errorf("profile %q: cloud must be one of %s, got %q", name, strings.Join(cloud.Names(), ", "), p.Cloud)
		}
	}
	if err := validateTokenStore(name, p.TokenStore); err != nil {
		return err
	}
	if p.GraphBaseURL != "" {
		u, err := url.Parse(p.GraphBaseURL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("profile %q: graph_base_url must be an https URL, got %q", name, p.GraphBaseURL)
		}
	}
	if _, err := Scopes.For(p.Scopes); err != nil {
		return fmt.Errorf("profile %q: %w", name, err)
	}
	return nil
}

func validateTokenStore(profile, spec string) error {
	switch {
	case spec == "" || spec == TokenStoreAuto || spec == TokenStoreFile:
		return nil
	case strings.HasPrefix(spec, TokenStoreKeyVault):
		if _, err := ParseKeyVaultURL(spec); err != nil {
			return fmt.Errorf("profile %q: %w", profile, err)
		}
		return nil
	case strings.HasPrefix(spec, TokenStoreFileURL):
		if strings.TrimPrefix(spec, TokenStoreFileURL) == "" {
			return fmt.Errorf("profile %q: token_store %q needs a path, for example file:///data/teams-cache.json", profile, spec)
		}
		return nil
	default:
		return fmt.Errorf("profile %q: token_store must be auto, file, file://<path> or keyvault://<vault>/<secret>, got %q", profile, spec)
	}
}

// KeyVaultTarget is a parsed keyvault://<vault>/<secret> store URL.
type KeyVaultTarget struct {
	Vault  string
	Secret string
}

// String renders the target back into its URL form.
func (k KeyVaultTarget) String() string { return TokenStoreKeyVault + k.Vault + "/" + k.Secret }

// ParseKeyVaultURL parses keyvault://<vault>/<secret>.
func ParseKeyVaultURL(spec string) (KeyVaultTarget, error) {
	rest := strings.TrimPrefix(spec, TokenStoreKeyVault)
	vault, secret, ok := strings.Cut(rest, "/")
	if !ok || vault == "" || secret == "" || strings.Contains(secret, "/") {
		return KeyVaultTarget{}, fmt.Errorf("want keyvault://<vault>/<secret>, got %q", spec)
	}
	return KeyVaultTarget{Vault: vault, Secret: secret}, nil
}

// Effective is a profile with every default filled in, ready to hand to the
// auth and Graph layers.
type Effective struct {
	Name string
	// Profile is the stored profile, before defaults.
	Profile Profile
	// Tenant, ClientID, Cloud, GraphBaseURL and TokenStore are the resolved
	// values.
	Tenant       string
	ClientID     string
	Cloud        string
	GraphBaseURL string
	TokenStore   string
	// Scopes is the final, ordered scope list.
	Scopes []string
	// ReadOnly blocks every write command before any network call.
	ReadOnly bool
	// UpdateCheck reports whether the CLI may check GitHub Releases for a newer
	// version (PLAN.md:351): on by default, off when `update_check = false` or
	// TEAMS_NO_UPDATE_CHECK is set. It only ever gates the check; nothing else
	// depends on it.
	UpdateCheck bool
	// ScopesSpec is what the user configured: "" (derived), a preset name or a
	// list. `auth status` and `doctor` show it.
	ScopesSpec string
}

// Mode returns the effective mode string (full or read-only), which is what
// `profile list`, `auth status` and `doctor` report.
func (e Effective) Mode() string {
	if e.ReadOnly {
		return ModeReadOnly
	}
	return ModeFull
}

// ResolveInput carries the CLI-level inputs that override the config file.
type ResolveInput struct {
	// ProfileFlag is --profile; empty means fall through to env and config.
	ProfileFlag string
	// ReadOnlyFlag is the global --read-only.
	ReadOnlyFlag bool
	// Environ is os.Environ() (injectable for tests).
	Environ []string
	// Cwd is unused today; kept out on purpose.
}

// Resolve picks the profile and fills in every default.
//
// Precedence for the profile name: --profile, TEAMS_PROFILE, default_profile,
// DefaultProfileName. Read-only comes from `mode = "read-only"`, TEAMS_READ_ONLY
// or --read-only; any of them wins, because read-only is a safety setting.
func (c *Config) Resolve(in ResolveInput) (Effective, error) {
	env := environMap(in.Environ)
	name := firstNonEmpty(in.ProfileFlag, env[EnvProfile], c.DefaultProfile, DefaultProfileName)
	if !store.ValidProfileName(name) {
		return Effective{}, fmt.Errorf("invalid profile name %q: use letters, digits, dot, dash or underscore", name)
	}
	p, ok := c.Profiles[name]
	if !ok && name != DefaultProfileName {
		return Effective{}, fmt.Errorf("profile %q is not configured (known profiles: %s)", name, strings.Join(c.ProfileNames(), ", "))
	}
	if err := p.Validate(name); err != nil {
		return Effective{}, err
	}

	eff := Effective{
		Name:       name,
		Profile:    p,
		Tenant:     firstNonEmpty(p.Tenant, DefaultTenant),
		ClientID:   firstNonEmpty(p.ClientID, DefaultClientID),
		Cloud:      firstNonEmpty(p.Cloud, DefaultCloud),
		TokenStore: firstNonEmpty(p.TokenStore, DefaultTokenStore),
		ReadOnly:   p.Mode == ModeReadOnly || in.ReadOnlyFlag || truthy(env[EnvReadOnly]),
		ScopesSpec: p.Scopes,
	}
	// The update check is opt-out: absent or true means yes, and the
	// environment can only turn it off (a CI runner sets TEAMS_NO_UPDATE_CHECK
	// to guarantee the CLI never talks to github.com).
	eff.UpdateCheck = c.UpdateCheck == nil || *c.UpdateCheck
	if ParseBoolEnv(in.Environ, EnvNoUpdate) {
		eff.UpdateCheck = false
	}
	endpoints, ok := cloud.Lookup(eff.Cloud)
	if !ok {
		return Effective{}, fmt.Errorf("profile %q: unknown cloud %q", name, eff.Cloud)
	}
	eff.GraphBaseURL = firstNonEmpty(p.GraphBaseURL, endpoints.Graph)
	if eff.GraphBaseURL == "" {
		return Effective{}, fmt.Errorf("profile %q: cloud %q: %w", name, eff.Cloud, cloud.ErrGraphBaseUndocumented)
	}
	if eff.ReadOnly && p.Mode == "" {
		// A global read-only switch narrows the requested scopes too: asking for
		// write scopes we then refuse to use would only look alarming to admins.
		eff.ScopesSpec = ScopePresetReadOnly
	}
	scopes, err := Scopes.For(eff.ScopesSpec)
	if err != nil {
		return Effective{}, fmt.Errorf("profile %q: %w", name, err)
	}
	eff.Scopes = scopes
	return eff, nil
}

// Validate checks the whole config, including every profile.
func (c *Config) Validate() error {
	if c.DefaultProfile != "" && !store.ValidProfileName(c.DefaultProfile) {
		return fmt.Errorf("default_profile %q is not a valid profile name", c.DefaultProfile)
	}
	for _, name := range c.ProfileNames() {
		if err := c.Profiles[name].Validate(name); err != nil {
			return err
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func environMap(environ []string) map[string]string {
	if environ == nil {
		environ = os.Environ()
	}
	out := make(map[string]string, len(environ))
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

// EnvironValue reads one variable with the same nil-means-os.Getenv rule.
func EnvironValue(environ []string, key string) string {
	return environMap(environ)[key]
}

// BoolPointer is a small helper for tests and config writers.
func BoolPointer(b bool) *bool { return &b }

// ParseBoolEnv reads a boolean environment variable.
func ParseBoolEnv(environ []string, key string) bool {
	v := EnvironValue(environ, key)
	if v == "" {
		return false
	}
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	return truthy(v)
}

// ErrNoProfile is returned when a profile name is referenced but not
// configured; commands map it to a usage error.
var ErrNoProfile = errors.New("profile is not configured")
