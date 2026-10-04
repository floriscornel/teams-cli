package tokenstore

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/store"
)

func pathsFor(t *testing.T, profile string) store.Paths {
	t.Helper()
	t.Setenv(store.EnvState, t.TempDir())
	t.Setenv(store.EnvCache, t.TempDir())
	t.Setenv(store.EnvConfig, filepath.Join(t.TempDir(), "config.toml"))
	p, err := store.Resolve(profile)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenSelectsTheConfiguredStore(t *testing.T) {
	paths := pathsFor(t, "me")

	auto, err := Open(paths, config.Effective{Name: "me", TokenStore: "auto"}, newFakeKeyring())
	if err != nil {
		t.Fatal(err)
	}
	if auto.Kind() != KindEnvelope {
		t.Errorf("auto store kind = %q, want %q", auto.Kind(), KindEnvelope)
	}

	file, err := Open(paths, config.Effective{Name: "me", TokenStore: "file"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if file.Kind() != KindFile || file.Description() != paths.TokenFilePlain() {
		t.Errorf("file store = %q at %q", file.Kind(), file.Description())
	}

	explicit := filepath.Join(t.TempDir(), "container.json")
	container, err := Open(paths, config.Effective{Name: "me", TokenStore: "file://" + explicit}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if container.Description() != explicit {
		t.Errorf("file:// store path = %q, want %q", container.Description(), explicit)
	}

	empty, err := Open(paths, config.Effective{Name: "me"}, newFakeKeyring())
	if err != nil {
		t.Fatal(err)
	}
	if empty.Kind() != KindEnvelope {
		t.Errorf("empty spec kind = %q, want the default envelope store", empty.Kind())
	}
}

func TestOpenRejectsBadSpecs(t *testing.T) {
	paths := pathsFor(t, "me")
	if _, err := Open(paths, config.Effective{Name: "me", TokenStore: "file://"}, nil); err == nil {
		t.Error("empty file:// path accepted")
	}
	if _, err := Open(paths, config.Effective{Name: "me", TokenStore: "s3://bucket"}, nil); err == nil {
		t.Error("unknown scheme accepted")
	}
	_, err := Open(paths, config.Effective{Name: "me", TokenStore: "keyvault://kv/secret"}, nil)
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("keyvault error = %v, want ErrNotImplemented with a phase pointer", err)
	}
	if !strings.Contains(err.Error(), "Phase 7") {
		t.Errorf("keyvault error = %v, want it to name the phase", err)
	}
	if _, err := Open(paths, config.Effective{Name: "me", TokenStore: "keyvault://vault"}, nil); err == nil {
		t.Error("malformed keyvault URL accepted")
	}
}

func TestKeyUserIsPerProfile(t *testing.T) {
	if keyUser("me") == keyUser("bot") {
		t.Error("two profiles share a keychain account")
	}
	if !strings.Contains(keyUser("me"), "me") {
		t.Errorf("keyUser = %q", keyUser("me"))
	}
}
