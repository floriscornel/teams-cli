package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestAliasRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.toml")
	if m, err := LoadAliases(path); err != nil || len(m) != 0 {
		t.Fatalf("LoadAliases(missing) = (%v, %v), want an empty map and no error", m, err)
	}
	if err := SetAlias(path, "boss", "alice@colorkrew.com"); err != nil {
		t.Fatalf("SetAlias = %v", err)
	}
	if err := SetAlias(path, "standup", "Engineering/Daily"); err != nil {
		t.Fatalf("SetAlias = %v", err)
	}
	m, err := LoadAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["boss"] != "alice@colorkrew.com" || m["standup"] != "Engineering/Daily" {
		t.Errorf("LoadAliases = %v, want both aliases", m)
	}
	// SetAlias is create or replace, which is how a target is corrected.
	if err := SetAlias(path, "boss", "bob@colorkrew.com"); err != nil {
		t.Fatal(err)
	}
	m, err = LoadAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["boss"] != "bob@colorkrew.com" {
		t.Errorf("LoadAliases = %v, want the replaced target and two aliases", m)
	}
	list, err := ListAliases(path)
	if err != nil {
		t.Fatalf("ListAliases = %v", err)
	}
	want := []Alias{
		{Name: "boss", Target: "bob@colorkrew.com"},
		{Name: "standup", Target: "Engineering/Daily"},
	}
	if len(list) != len(want) || list[0] != want[0] || list[1] != want[1] {
		t.Errorf("ListAliases = %+v, want %+v sorted by name", list, want)
	}
}

func TestAliasNamesAreCaseInsensitive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.toml")
	if err := SetAlias(path, "  Boss ", "alice@colorkrew.com"); err != nil {
		t.Fatal(err)
	}
	m, err := LoadAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := m["boss"]; !ok || got != "alice@colorkrew.com" {
		t.Errorf("LoadAliases = %v, want the name trimmed and lower-cased", m)
	}
	if len(m) != 1 {
		t.Errorf("LoadAliases = %v, want a single entry", m)
	}
	// A hand-written file is read the same way, and a dotted name survives the
	// round trip as one flat key.
	handwritten := "Boss = 'alice@colorkrew.com'\n'a.b' = 'Engineering/Daily'\n"
	if err := WriteFile(path, []byte(handwritten)); err != nil {
		t.Fatal(err)
	}
	m, err = LoadAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["boss"] != "alice@colorkrew.com" || m["a.b"] != "Engineering/Daily" {
		t.Errorf("LoadAliases = %v, want normalized names", m)
	}
	if err := SaveAliases(path, m); err != nil {
		t.Fatal(err)
	}
	again, err := LoadAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 2 || again["a.b"] != "Engineering/Daily" {
		t.Errorf("the round trip lost a dotted name: %v", again)
	}
}

func TestAliasCorruptFileIsReportedNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"invalid-toml":   "boss = ",
		"nested-table":   "[boss]\ntarget = 'alice@colorkrew.com'\n",
		"wrong-type":     "boss = 3\n",
		"duplicate-keys": "boss = 'a'\nboss = 'b'\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name, "aliases.toml")
			if err := WriteFile(path, []byte(content)); err != nil {
				t.Fatal(err)
			}
			m, err := LoadAliases(path)
			if !errors.Is(err, ErrCorruptAliases) {
				t.Fatalf("LoadAliases = %v, want ErrCorruptAliases", err)
			}
			if m == nil || len(m) != 0 {
				t.Errorf("LoadAliases = %v, want an empty, non-nil map", m)
			}
			if _, err := ListAliases(path); !errors.Is(err, ErrCorruptAliases) {
				t.Errorf("ListAliases = %v, want ErrCorruptAliases", err)
			}
			// Aliases are user data, not a rebuildable cache: the broken file is
			// reported rather than silently replaced.
			if err := SetAlias(path, "boss", "alice@colorkrew.com"); !errors.Is(err, ErrCorruptAliases) {
				t.Errorf("SetAlias = %v, want ErrCorruptAliases", err)
			}
			if removed, err := RemoveAlias(path, "boss"); removed || !errors.Is(err, ErrCorruptAliases) {
				t.Errorf("RemoveAlias = (%v, %v), want (false, ErrCorruptAliases)", removed, err)
			}
			after, err := ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != content {
				t.Errorf("the broken file was rewritten: %q", after)
			}
		})
	}
}

func TestLoadAliasesReportsReadErrors(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadAliases(dir)
	if err == nil {
		t.Fatal("LoadAliases accepted a directory")
	}
	if m == nil || len(m) != 0 {
		t.Errorf("LoadAliases = %v, want an empty, usable map alongside the error", m)
	}
	if errors.Is(err, ErrCorruptAliases) {
		t.Error("an I/O failure is not corruption")
	}
	// An empty file is an empty mapping, not corruption.
	empty := filepath.Join(dir, "aliases.toml")
	if err := WriteFile(empty, nil); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadAliases(empty); err != nil || len(m) != 0 {
		t.Errorf("LoadAliases(empty file) = (%v, %v), want an empty map", m, err)
	}
}

func TestSaveAliasesWritesASortedStableFile(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one", "aliases.toml")
	second := filepath.Join(dir, "two", "aliases.toml")
	if err := SaveAliases(first, map[string]string{"zeta": "Z", "alpha": "A", "mid": "M"}); err != nil {
		t.Fatal(err)
	}
	if err := SaveAliases(second, map[string]string{"mid": "M", "alpha": "A", "zeta": "Z"}); err != nil {
		t.Fatal(err)
	}
	one, err := ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(one) != string(two) {
		t.Errorf("the same mapping wrote different bytes: %q and %q", one, two)
	}
	if i, j := strings.Index(string(one), "alpha"), strings.Index(string(one), "zeta"); i < 0 || j < 0 || i > j {
		t.Errorf("the file is not sorted: %q", one)
	}
	// Saving the same mapping twice cannot differ either.
	if err := SaveAliases(first, map[string]string{"alpha": "A", "mid": "M", "zeta": "Z"}); err != nil {
		t.Fatal(err)
	}
	again, err := ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(one) {
		t.Errorf("saving the same mapping twice changed the file: %q and %q", one, again)
	}
	// A nil map writes an empty file that reads back as an empty mapping.
	empty := filepath.Join(dir, "empty.toml")
	if err := SaveAliases(empty, nil); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadAliases(empty); err != nil || len(m) != 0 {
		t.Errorf("LoadAliases(empty) = (%v, %v)", m, err)
	}
	// Names that normalize to nothing are dropped instead of written as junk.
	if err := SaveAliases(first, map[string]string{"   ": "dropped", "Boss": "alice@colorkrew.com"}); err != nil {
		t.Fatal(err)
	}
	m, err := LoadAliases(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || m["boss"] != "alice@colorkrew.com" {
		t.Errorf("LoadAliases = %v, want only the usable alias", m)
	}
}

func TestSaveAliasesIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	dir := filepath.Join(t.TempDir(), "nested")
	path := filepath.Join(dir, "aliases.toml")
	if err := SaveAliases(path, map[string]string{"boss": "alice@colorkrew.com"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != FileMode {
		t.Errorf("file mode = %v, want %v", perm, FileMode)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := di.Mode().Perm(); perm != DirMode {
		t.Errorf("directory mode = %v, want %v", perm, DirMode)
	}
}

func TestSaveAliasesFailedWriteKeepsThePreviousFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs an unwritable directory and a non-root user")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases.toml")
	if err := SaveAliases(path, map[string]string{"boss": "alice@colorkrew.com"}); err != nil {
		t.Fatal(err)
	}
	before, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // restoring the test fixture
	if err := SaveAliases(path, map[string]string{"boss": "bob@colorkrew.com"}); err == nil {
		t.Fatal("SaveAliases succeeded in an unwritable directory")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("a failed save changed the file: %q and %q", before, after)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the failed save left %d entries behind, want 1: %v", len(entries), entries)
	}
}

func TestValidAliasName(t *testing.T) {
	for _, name := range []string{"boss", "Boss", "a.b", "a_b-c", "A1", "9", "  boss  ", "Engineering"} {
		if !ValidAliasName(name) {
			t.Errorf("ValidAliasName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"", "   ", "@boss", "a b", "a/b", "a:b", "boss!", "Engineering/General"} {
		if ValidAliasName(name) {
			t.Errorf("ValidAliasName(%q) = true, want false", name)
		}
	}
}

func TestSetAliasValidatesItsArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.toml")
	if err := SetAlias(path, "@boss", "alice@colorkrew.com"); err == nil {
		t.Error("SetAlias accepted an invalid name")
	}
	if err := SetAlias(path, "", "alice@colorkrew.com"); err == nil {
		t.Error("SetAlias accepted an empty name")
	}
	if err := SetAlias(path, "boss", "   "); err == nil {
		t.Error("SetAlias accepted an empty target")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("a rejected alias wrote %s", path)
	}
	// The stored forms are trimmed.
	if err := SetAlias(path, "  Boss ", " alice@colorkrew.com "); err != nil {
		t.Fatalf("SetAlias = %v", err)
	}
	m, err := LoadAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["boss"] != "alice@colorkrew.com" {
		t.Errorf("LoadAliases = %v, want the trimmed name and target", m)
	}
}

func TestRemoveAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.toml")
	if err := SetAlias(path, "boss", "alice@colorkrew.com"); err != nil {
		t.Fatal(err)
	}
	if err := SetAlias(path, "standup", "Engineering/Daily"); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveAlias(path, "Boss") // names match case-insensitively
	if err != nil || !removed {
		t.Fatalf("RemoveAlias = (%v, %v), want (true, nil)", removed, err)
	}
	m, err := LoadAliases(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m["boss"]; ok {
		t.Error("the alias survived RemoveAlias")
	}
	if m["standup"] != "Engineering/Daily" {
		t.Errorf("RemoveAlias touched another alias: %v", m)
	}
	// Removing what is not there is not an error, and does not rewrite the file.
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveAlias(path, "nope"); err != nil || removed {
		t.Fatalf("RemoveAlias(nope) = (%v, %v), want (false, nil)", removed, err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("removing a missing alias rewrote the file")
	}
	// An invalid name is a usage error rather than a missing alias.
	if removed, err := RemoveAlias(path, "@nope"); err == nil || removed {
		t.Errorf("RemoveAlias(@nope) = (%v, %v), want a usage error", removed, err)
	}
	// A missing file has nothing to remove, and is not created.
	missing := filepath.Join(t.TempDir(), "aliases.toml")
	if removed, err := RemoveAlias(missing, "boss"); err != nil || removed {
		t.Errorf("RemoveAlias on a missing file = (%v, %v), want (false, nil)", removed, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Errorf("removing from a missing file created %s", missing)
	}
}

func TestAliasesConcurrentWritersThroughTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.toml")
	const writers, each = 2, 3
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				// SetAlias holds the per-profile lock around its
				// read-modify-write, so a concurrent writer cannot lose an
				// alias stored between its own load and save.
				name := fmt.Sprintf("alias-%d-%d", w, i)
				if err := SetAlias(path, name, "alice@colorkrew.com"); err != nil {
					t.Errorf("writer %d: %v", w, err)
				}
			}
		}(w)
	}
	wg.Wait()
	m, err := LoadAliases(path)
	if err != nil {
		t.Fatalf("the concurrently written file does not parse: %v", err)
	}
	if len(m) != writers*each {
		t.Errorf("the file holds %d aliases, want %d: a writer lost one (%v)", len(m), writers*each, m)
	}
	for w := 0; w < writers; w++ {
		for i := 0; i < each; i++ {
			name := fmt.Sprintf("alias-%d-%d", w, i)
			if m[name] != "alice@colorkrew.com" {
				t.Errorf("alias %q is missing from %v", name, m)
			}
		}
	}
	if _, err := os.Stat(LockPath(path)); !os.IsNotExist(err) {
		t.Errorf("the lock file was left behind: %v", err)
	}
}
