// Package store owns the CLI's on-disk layout and the primitives every other
// package uses to touch it: directory resolution, atomic 0600/0700 writes and a
// per-profile lock file.
//
// The layout follows PLAN.md "Local data: config, cache, state": config lives in
// os.UserConfigDir()/teams, the rebuildable cache in os.UserCacheDir()/teams,
// and user state in a directory we pick ourselves (Go has no os.UserStateDir).
// Everything except the config file is per profile, because the bot identity and
// the personal identity must never share a cache or a name resolution.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Env variables that relocate the layout. Tests and containers need them
// (PLAN.md "Local data"); TEAMS_CONFIG is documented in "Profiles".
const (
	EnvConfig = "TEAMS_CONFIG"
	EnvCache  = "TEAMS_CACHE_DIR"
	EnvState  = "TEAMS_STATE_DIR"
)

const (
	appDir         = "teams"
	configName     = "config.toml"
	tokenFileName  = "token.bin"
	tokenPlainName = "token.json"
	lockName       = ".lock"
)

// Paths is the resolved on-disk layout for one profile.
type Paths struct {
	Profile string
	// ConfigDir is the directory holding config.toml.
	ConfigDir string
	// ConfigFile is the TOML config file (shared by every profile).
	ConfigFile string
	// CacheRoot is the parent of CacheDir (os.UserCacheDir()/teams).
	CacheRoot string
	// CacheDir is the rebuildable, per-profile cache directory.
	CacheDir string
	// StateRoot is the parent of StateDir.
	StateRoot string
	// StateDir is the per-profile user-state directory.
	StateDir string
}

// Resolve returns the layout for a profile. Environment overrides apply to the
// root directories, so TEAMS_STATE_DIR=/tmp/x still keeps profiles apart.
func Resolve(profile string) (Paths, error) {
	if !ValidProfileName(profile) {
		return Paths{}, fmt.Errorf("invalid profile name %q: use letters, digits, dot, dash or underscore", profile)
	}
	configFile := os.Getenv(EnvConfig)
	configDir := ""
	if configFile == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("locate the config directory: %w", err)
		}
		configDir = filepath.Join(base, appDir)
		configFile = filepath.Join(configDir, configName)
	} else {
		configDir = filepath.Dir(configFile)
	}

	cacheRoot := os.Getenv(EnvCache)
	if cacheRoot == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return Paths{}, fmt.Errorf("locate the cache directory: %w", err)
		}
		cacheRoot = filepath.Join(base, appDir)
	}

	stateRoot := os.Getenv(EnvState)
	if stateRoot == "" {
		var err error
		if stateRoot, err = defaultStateRoot(); err != nil {
			return Paths{}, err
		}
	}

	return Paths{
		Profile:    profile,
		ConfigDir:  configDir,
		ConfigFile: configFile,
		CacheRoot:  cacheRoot,
		CacheDir:   filepath.Join(cacheRoot, profile),
		StateRoot:  stateRoot,
		StateDir:   filepath.Join(stateRoot, profile),
	}, nil
}

// defaultStateRoot implements the state-directory rule from PLAN.md: Linux
// follows the XDG base directory spec, macOS and Windows keep state beside the
// config (there is no os.UserStateDir in Go).
func defaultStateRoot() (string, error) {
	if runtime.GOOS == "linux" {
		if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
			return filepath.Join(xdg, appDir), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate the state directory: %w", err)
		}
		return filepath.Join(home, ".local", "state", appDir), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the state directory: %w", err)
	}
	return filepath.Join(base, appDir, "state"), nil
}

// TokenFile is the encrypted MSAL cache. It is a secret: `teams cache clear`
// never touches it, only `teams auth logout` removes it.
func (p Paths) TokenFile() string { return filepath.Join(p.StateDir, tokenFileName) }

// TokenFilePlain is the 0600 plaintext fallback used where no OS keychain is
// reachable, and the location `token_store = "file"` selects explicitly.
func (p Paths) TokenFilePlain() string { return filepath.Join(p.StateDir, tokenPlainName) }

// SyncFile is the timestamp file msal-ext watches. It is not a lock: msal-ext
// puts its own lock at this path plus ".lockfile".
func (p Paths) SyncFile() string { return filepath.Join(p.StateDir, "msal-cache") }

// LockFile guards concurrent writers of the per-profile state.
func (p Paths) LockFile() string { return filepath.Join(p.StateDir, lockName) }

// EntityCacheFile holds the rebuildable name/person cache.
func (p Paths) EntityCacheFile() string { return filepath.Join(p.CacheDir, "entities.json") }

// AliasesFile holds user-defined aliases (state, not cache).
func (p Paths) AliasesFile() string { return filepath.Join(p.StateDir, "aliases.toml") }

// AuthMetadataFile holds our own refresh bookkeeping and the account key. The
// MSAL blob is opaque and unsupported, so the refresh-token age cannot be read
// out of it (PLAN.md:92).
func (p Paths) AuthMetadataFile() string { return filepath.Join(p.StateDir, "auth.json") }

// UpdateFile is the update check's cache: when it last ran and what the newest
// release was. It is per profile because every state file is, and it holds
// nothing sensitive.
func (p Paths) UpdateFile() string { return filepath.Join(p.StateDir, "update.json") }

// AIDir holds session history and memory for this profile.
func (p Paths) AIDir() string { return filepath.Join(p.StateDir, "ai") }

// AISessionsDir holds one JSONL file per AI session.
func (p Paths) AISessionsDir() string { return filepath.Join(p.AIDir(), "sessions") }

// AIMemoryFile holds the curated, user-visible memory.
func (p Paths) AIMemoryFile() string { return filepath.Join(p.AIDir(), "memory.toml") }

// Location describes one on-disk artifact for `teams cache info` and for the
// deletion policy of `teams cache clear`.
type Location struct {
	// Kind is one of "config", "secrets", "cache" or "state".
	Kind string
	// Label is the human-readable name shown by `cache info`.
	Label string
	// Path is the file or directory.
	Path string
	// Dir reports whether Path is a directory (its tree is scanned).
	Dir bool
}

// Locations lists every path the CLI owns for this profile, in the order
// `teams cache info` prints them.
func (p Paths) Locations() []Location {
	return []Location{
		{Kind: "config", Label: "config", Path: p.ConfigFile},
		{Kind: "secrets", Label: "token cache", Path: p.TokenFile()},
		{Kind: "secrets", Label: "token cache (plaintext)", Path: p.TokenFilePlain()},
		{Kind: "secrets", Label: "MSAL sync marker", Path: p.SyncFile()},
		{Kind: "secrets", Label: "auth metadata", Path: p.AuthMetadataFile()},
		{Kind: "state", Label: "aliases", Path: p.AliasesFile()},
		{Kind: "state", Label: "AI history and memory", Path: p.AIDir(), Dir: true},
		{Kind: "cache", Label: "entity cache", Path: p.CacheDir, Dir: true},
	}
}

var profileNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidProfileName reports whether a profile name is usable as a path segment.
// It deliberately rejects anything that could escape the state directory.
func ValidProfileName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return false
	}
	return profileNameRE.MatchString(name)
}
