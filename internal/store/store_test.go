package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolveAppliesEnvOverrides(t *testing.T) {
	root := t.TempDir()
	configFile := filepath.Join(root, "cfg", "config.toml")
	t.Setenv(EnvConfig, configFile)
	t.Setenv(EnvCache, filepath.Join(root, "cache"))
	t.Setenv(EnvState, filepath.Join(root, "state"))

	p, err := Resolve("bot")
	if err != nil {
		t.Fatal(err)
	}
	if p.ConfigFile != configFile {
		t.Errorf("ConfigFile = %q, want %q", p.ConfigFile, configFile)
	}
	if p.ConfigDir != filepath.Dir(configFile) {
		t.Errorf("ConfigDir = %q, want %q", p.ConfigDir, filepath.Dir(configFile))
	}
	// The profile segment is kept even when the roots are overridden, so two
	// profiles can never share a cache or a state file.
	if want := filepath.Join(root, "cache", "bot"); p.CacheDir != want {
		t.Errorf("CacheDir = %q, want %q", p.CacheDir, want)
	}
	if want := filepath.Join(root, "state", "bot"); p.StateDir != want {
		t.Errorf("StateDir = %q, want %q", p.StateDir, want)
	}
	if want := filepath.Join(root, "state", "bot", "token.bin"); p.TokenFile() != want {
		t.Errorf("TokenFile = %q, want %q", p.TokenFile(), want)
	}
}

func TestResolveProfilesAreIsolated(t *testing.T) {
	t.Setenv(EnvCache, t.TempDir())
	t.Setenv(EnvState, t.TempDir())
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "config.toml"))

	me, err := Resolve("me")
	if err != nil {
		t.Fatal(err)
	}
	bot, err := Resolve("bot")
	if err != nil {
		t.Fatal(err)
	}
	if me.CacheDir == bot.CacheDir || me.StateDir == bot.StateDir || me.TokenFile() == bot.TokenFile() {
		t.Fatal("profiles share state")
	}
}

func TestResolveRejectsBadProfileNames(t *testing.T) {
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "config.toml"))
	for _, name := range []string{"", ".", "..", "a/b", `a\b`, "-leading", strings.Repeat("x", 65)} {
		if _, err := Resolve(name); err == nil {
			t.Errorf("Resolve(%q) succeeded, want error", name)
		}
	}
	for _, name := range []string{"me", "bot", "work.2", "a_b-c"} {
		if !ValidProfileName(name) {
			t.Errorf("ValidProfileName(%q) = false, want true", name)
		}
	}
}

func TestResolveDefaultStateRootIsPerOS(t *testing.T) {
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "config.toml"))
	p, err := Resolve("me")
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "linux":
		if !strings.Contains(p.StateRoot, filepath.Join(".local", "state", "teams")) &&
			os.Getenv("XDG_STATE_HOME") == "" {
			t.Errorf("StateRoot = %q, want an XDG state directory", p.StateRoot)
		}
	default:
		base, err := os.UserConfigDir()
		if err != nil {
			t.Skip("no user config dir")
		}
		if want := filepath.Join(base, "teams", "state"); p.StateRoot != want {
			t.Errorf("StateRoot = %q, want %q", p.StateRoot, want)
		}
	}
}

func TestWriteFileIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "file.json")
	if err := WriteFile(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "two" {
		t.Errorf("ReadFile = %q, want %q", got, "two")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != FileMode {
			t.Errorf("file mode = %v, want %v", perm, FileMode)
		}
		di, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if perm := di.Mode().Perm(); perm != DirMode {
			t.Errorf("dir mode = %v, want %v", perm, DirMode)
		}
	}
	// The temporary file must not be left behind.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want 1: %v", len(entries), entries)
	}
}

func TestReadFileMissingIsNotAnError(t *testing.T) {
	got, err := ReadFile(filepath.Join(t.TempDir(), "nope"))
	if err != nil || got != nil {
		t.Fatalf("ReadFile = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestRemoveHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := WriteFile(path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := RemoveIfExists(path); err != nil {
		t.Fatal(err)
	}
	if err := RemoveIfExists(path); err != nil {
		t.Fatalf("second remove: %v", err)
	}
	if err := RemoveTree(filepath.Join(dir, "missing")); err != nil {
		t.Fatalf("missing tree: %v", err)
	}
}

func TestLockExcludesConcurrentHolders(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	ctx := context.Background()
	l, err := Acquire(ctx, path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(ctx, path, 0); err == nil {
		t.Fatal("second Acquire succeeded, want failure")
	}
	if pid, err := PidFromLockFile(path); err != nil || pid != os.Getpid() {
		t.Errorf("PidFromLockFile = (%d, %v), want (%d, nil)", pid, err, os.Getpid())
	}
	if err := l.Release(); err != nil {
		t.Fatal(err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("double release: %v", err)
	}
	l2, err := Acquire(ctx, path, time.Second)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	defer func() { _ = l2.Release() }()
}

func TestLockWaitsForRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	ctx := context.Background()
	// A fresh lock owned by a pid that no longer exists is still honored until
	// StaleLockAfter, so the waiter must retry rather than steal it.
	if err := WriteFile(path, []byte(`{"pid":1,"time":"`+time.Now().UTC().Format(time.RFC3339Nano)+`"}`)); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(60 * time.Millisecond)
		_ = os.Remove(path)
	}()
	l, err := Acquire(ctx, path, 2*time.Second)
	<-done
	if err != nil {
		t.Fatalf("Acquire waited: %v", err)
	}
	_ = l.Release()
}

func TestLockReclaimsStaleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	stale := time.Now().Add(-2 * StaleLockAfter).UTC().Format(time.RFC3339Nano)
	if err := WriteFile(path, []byte(`{"pid":1,"time":"`+stale+`"}`)); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = l.Release() }()
}

func TestLockReclaimsGarbageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	if err := WriteFile(path, []byte("not json")); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * StaleLockAfter)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	l, err := Acquire(context.Background(), path, time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer func() { _ = l.Release() }()
}

func TestLockHonorsContextCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	other, err := Acquire(context.Background(), path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Release() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Acquire(ctx, path, time.Minute); err == nil {
		t.Fatal("Acquire succeeded, want cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Acquire took %s, want a prompt cancellation", elapsed)
	}
}

func TestWithLockRunsFunction(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".lock")
	ran := false
	if err := WithLock(context.Background(), path, time.Second, func() error {
		ran = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !ran {
		t.Fatal("fn did not run")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file still present: %v", err)
	}
}

func TestScanMeasuresFilesAndDirs(t *testing.T) {
	dir := t.TempDir()
	if err := WriteFile(filepath.Join(dir, "a"), []byte("12345")); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(dir, "sub", "b"), []byte("123")); err != nil {
		t.Fatal(err)
	}
	stats := Scan(dir, true)
	if !stats.Exists || stats.Files != 2 || stats.Bytes != 8 {
		t.Errorf("Scan = %+v, want 2 files / 8 bytes", stats)
	}
	if stats.Age() <= 0 {
		t.Errorf("Age = %v, want a positive age", stats.Age())
	}
	file := Scan(filepath.Join(dir, "a"), false)
	if file.Files != 1 || file.Bytes != 5 {
		t.Errorf("file scan = %+v", file)
	}
	missing := Scan(filepath.Join(dir, "nope"), true)
	if missing.Exists || missing.Problem != "" {
		t.Errorf("missing scan = %+v", missing)
	}
	if missing.Age() != 0 {
		t.Errorf("missing age = %v, want 0", missing.Age())
	}
}

func TestLocationsCoverEveryKind(t *testing.T) {
	p, err := Resolve("me")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, loc := range p.Locations() {
		kinds[loc.Kind] = true
		if loc.Path == "" || loc.Label == "" {
			t.Errorf("incomplete location: %+v", loc)
		}
	}
	for _, want := range []string{"config", "secrets", "cache", "state"} {
		if !kinds[want] {
			t.Errorf("Locations() has no %q entry", want)
		}
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 2048: "2.0 KiB", 5 << 20: "5.0 MiB"}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
