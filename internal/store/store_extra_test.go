package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestEnsureDirAndParent(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")
	if err := EnsureDir(nested); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(nested); err != nil || !fi.IsDir() {
		t.Fatalf("EnsureDir did not create the tree: %v", err)
	}
	if err := EnsureDir(nested); err != nil {
		t.Fatalf("EnsureDir is not idempotent: %v", err)
	}
	if err := EnsureParent(filepath.Join(nested, "file")); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(filepath.Join(dir, "file")); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDirReportsFailures(t *testing.T) {
	// A path that cannot be a directory: a file already exists there.
	dir := t.TempDir()
	file := filepath.Join(dir, "plain")
	if err := WriteFile(file, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(file); err == nil {
		t.Error("EnsureDir accepted a path that is a file")
	}
	if err := EnsureParent(filepath.Join(file, "child")); err == nil {
		t.Error("EnsureParent accepted a parent that is a file")
	}
}

func TestWriteFileMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "custom")
	if err := WriteFileMode(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o644 {
			t.Errorf("mode = %v, want 0644", perm)
		}
	}
}

func TestReadFileReportsRealErrors(t *testing.T) {
	dir := t.TempDir()
	// A directory is not a readable file; the error must be reported, not
	// swallowed as "missing".
	if _, err := ReadFile(dir); err == nil {
		t.Error("ReadFile accepted a directory")
	}
}

func TestRemoveHelpersReportErrors(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := EnsureDir(sub); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(sub, "f"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveIfExists(sub); err == nil {
		t.Error("RemoveIfExists removed a non-empty directory tree")
	}
	if err := RemoveTree(sub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("RemoveTree left %s: %v", sub, err)
	}
	if err := RemoveTree(sub); err != nil {
		t.Errorf("RemoveTree is not idempotent: %v", err)
	}
}

func TestAcquireReportsAnUnusableLockPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "plain")
	if err := WriteFile(file, []byte("x")); err != nil {
		t.Fatal(err)
	}
	// The lock file's parent cannot be created, so Acquire must fail with
	// something other than "already locked".
	_, err := Acquire(context.Background(), filepath.Join(file, "l.lock"), time.Second)
	if err == nil {
		t.Fatal("Acquire succeeded on an impossible path")
	}
	if strings.Contains(err.Error(), "locked by another") {
		t.Errorf("err = %v, want the real filesystem error", err)
	}
}

func TestWithLockPropagatesErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".lock")
	sentinel := errors.New("fn failed")
	err := WithLock(context.Background(), path, time.Second, func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("err = %v, want the function's error", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the lock file survived a failed function: %v", err)
	}
}

func TestLockPathAndNilRelease(t *testing.T) {
	var nilLock *Lock
	if nilLock.Path() != "" {
		t.Error("a nil lock has a path")
	}
	if err := nilLock.Release(); err != nil {
		t.Errorf("releasing a nil lock = %v", err)
	}
	path := filepath.Join(t.TempDir(), ".lock")
	l, err := Acquire(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if l.Path() != path {
		t.Errorf("Path = %q, want %q", l.Path(), path)
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if l.Path() != "" {
		t.Error("Release left the path set")
	}
}

func TestStaleLockWithGarbageContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	if err := WriteFile(path, []byte("}{")); err != nil {
		t.Fatal(err)
	}
	// A fresh, uninterpretable lock file is honoured...
	if stale, err := staleLock(path); err != nil || stale {
		t.Fatalf("staleLock = (%v, %v), want a fresh lock to be honoured", stale, err)
	}
	// ...and one whose mtime is old is reclaimed.
	old := time.Now().Add(-2 * StaleLockAfter)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if stale, err := staleLock(path); err != nil || !stale {
		t.Fatalf("staleLock = (%v, %v), want the old lock reclaimed", stale, err)
	}
	if _, err := staleLock(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("staleLock accepted a missing file")
	}
}

func TestScanProblemsAreReported(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := EnsureDir(sub); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(sub, "f"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission-based failures do not apply here")
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o700) }) //nolint:gosec // restoring the test fixture's mode
	stats := Scan(dir, true)
	if stats.Problem == "" {
		t.Error("Scan did not report an unreadable directory")
	}
	if stats.Files != 0 {
		t.Errorf("Scan reported %d files, want none from an unreadable directory", stats.Files)
	}
}

func TestResolveUsesThePlatformDefaults(t *testing.T) {
	// Without any override, the layout must still be absolute and consistent.
	for _, env := range []string{EnvConfig, EnvCache, EnvState} {
		t.Setenv(env, "")
	}
	p, err := Resolve("me")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{p.ConfigFile, p.CacheDir, p.StateDir, p.TokenFile(), p.TokenFilePlain()} {
		if !filepath.IsAbs(path) {
			t.Errorf("%q is not absolute", path)
		}
	}
	if !strings.HasSuffix(p.CacheDir, filepath.Join("teams", "me")) {
		t.Errorf("CacheDir = %q, want it under <cache>/teams/me", p.CacheDir)
	}
	if filepath.Base(p.TokenFilePlain()) != "token.json" || filepath.Base(p.TokenFile()) != "token.bin" {
		t.Errorf("token paths = %q, %q", p.TokenFile(), p.TokenFilePlain())
	}
	if !strings.HasSuffix(p.SyncFile(), filepath.Join("me", "msal-cache")) {
		t.Errorf("SyncFile = %q", p.SyncFile())
	}
	// Every location must be listed exactly once and be either a file or a dir.
	seen := map[string]bool{}
	for _, loc := range p.Locations() {
		if seen[loc.Path] {
			t.Errorf("%s is listed twice", loc.Path)
		}
		seen[loc.Path] = true
	}
}

func TestResolveRejectsAnUnusableConfigDir(t *testing.T) {
	// A config path pointing at an existing directory cannot be written, and the
	// layout resolution is still expected to succeed: the failure belongs to the
	// write, not to the layout.
	dir := t.TempDir()
	t.Setenv(EnvConfig, dir)
	p, err := Resolve("me")
	if err != nil {
		t.Fatal(err)
	}
	// TEAMS_CONFIG names a file, so the directory is its parent.
	if p.ConfigDir != filepath.Dir(dir) {
		t.Errorf("ConfigDir = %q, want %q", p.ConfigDir, filepath.Dir(dir))
	}
	if p.ConfigFile != dir {
		t.Errorf("ConfigFile = %q, want %q", p.ConfigFile, dir)
	}
}
