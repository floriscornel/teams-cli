package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// fixedNow is the instant the tests inject as the cache's clock, so every TTL
// assertion is deterministic: the package never reads the wall clock for a
// decision (see EntityCache.SetClock).
var fixedNow = time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)

// seededCache writes one entry of every kind to path with the fixed clock and
// returns the cache that wrote it.
func seededCache(t *testing.T, path string) *EntityCache {
	t.Helper()
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatalf("LoadEntities(%s) = %v", path, err)
	}
	c.SetClock(func() time.Time { return fixedNow })
	c.PutTeam("Engineering", "team-eng")
	c.PutTeam("  Payments ", "team-pay")
	c.PutChannel("team-eng", "General", "chan-general")
	yuki := Person{UserID: "user-yuki", DisplayName: "Yuki Tanaka", Mail: "yuki@colorkrew.com"}
	c.PutPerson("@Yuki", yuki)
	c.PutPerson("yuki@colorkrew.com", yuki)
	c.PutPersonChat("@Yuki", "chat-yuki")
	if !c.Dirty() {
		t.Fatal("a cache that was written to is not dirty")
	}
	if err := c.Save(path); err != nil {
		t.Fatalf("Save = %v", err)
	}
	if c.Dirty() {
		t.Fatal("Save left the cache dirty")
	}
	return c
}

func TestEntityCacheRoundTripEveryKind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	seededCache(t, path)

	c, err := LoadEntities(path)
	if err != nil {
		t.Fatalf("LoadEntities = %v", err)
	}
	if c.Problem() != nil {
		t.Fatalf("Problem = %v, want nil for a file we just wrote", c.Problem())
	}
	if c.Dirty() {
		t.Error("a freshly loaded cache reports dirty")
	}
	// Names match case-insensitively and whitespace-trimmed (PLAN.md:165).
	for _, name := range []string{"Engineering", "engineering", "  ENGINEERING  ", "engineering "} {
		id, ok := c.Team(name)
		if !ok || id != "team-eng" {
			t.Errorf("Team(%q) = (%q, %v), want (team-eng, true)", name, id, ok)
		}
	}
	for _, name := range []string{"General", "general", " GENERAL "} {
		id, ok := c.Channel("team-eng", name)
		if !ok || id != "chan-general" {
			t.Errorf("Channel(team-eng, %q) = (%q, %v), want (chan-general, true)", name, id, ok)
		}
	}
	yuki := Person{UserID: "user-yuki", DisplayName: "Yuki Tanaka", Mail: "yuki@colorkrew.com"}
	for _, key := range []string{"@yuki", "yuki", "@Yuki", " yuki "} {
		p, ok := c.Person(key)
		if !ok || p != yuki {
			t.Errorf("Person(%q) = (%+v, %v), want %+v", key, p, ok, yuki)
		}
		if id, ok := c.PersonUserID(key); !ok || id != "user-yuki" {
			t.Errorf("PersonUserID(%q) = (%q, %v), want (user-yuki, true)", key, id, ok)
		}
	}
	if p, ok := c.Person("yuki@colorkrew.com"); !ok || p != yuki {
		t.Errorf("Person(mail) = (%+v, %v), want %+v", p, ok, yuki)
	}
	if id, ok := c.PersonChat("@Yuki"); !ok || id != "chat-yuki" {
		t.Errorf("PersonChat = (%q, %v), want (chat-yuki, true)", id, ok)
	}

	// ForEach visits every entry in a stable order and keeps the spelling the
	// user typed, which is what output shows.
	want := []Entry{
		{Key: "engineering", Kind: KindTeam, Value: "team-eng", Name: "Engineering", At: fixedNow},
		{Key: "payments", Kind: KindTeam, Value: "team-pay", Name: "Payments", At: fixedNow},
		{Key: "team-eng/general", Kind: KindChannel, Value: "chan-general", Name: "General", At: fixedNow},
		{Key: "yuki", Kind: KindPerson, Value: "user-yuki", Name: "@Yuki", At: fixedNow},
		{Key: "yuki@colorkrew.com", Kind: KindPerson, Value: "user-yuki", Name: "yuki@colorkrew.com", At: fixedNow},
		{Key: "yuki", Kind: KindPersonChat, Value: "chat-yuki", Name: "@Yuki", At: fixedNow},
	}
	var got []Entry
	c.ForEach(func(key string, entry Entry) {
		if key != entry.Key {
			t.Errorf("ForEach key %q does not match Entry.Key %q", key, entry.Key)
		}
		got = append(got, entry)
	})
	if len(got) != len(want) {
		t.Fatalf("ForEach visited %d entries, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	stats := c.Stats(fixedNow)
	if stats.Entries != len(want) || !stats.Oldest.Equal(fixedNow) {
		t.Errorf("Stats = %+v, want %d entries with Oldest = %v", stats, len(want), fixedNow)
	}

	// Misses stay misses: an unknown name, an unknown team id, a channel
	// without a team and a person without a chat.
	if _, ok := c.Team("Sales"); ok {
		t.Error("Team(Sales) hit, want a miss")
	}
	if _, ok := c.Team(""); ok {
		t.Error("Team() hit, want a miss")
	}
	if _, ok := c.Channel("team-eng", "Sales"); ok {
		t.Error("Channel(team-eng, Sales) hit, want a miss")
	}
	if _, ok := c.Channel("", "General"); ok {
		t.Error("Channel without a team id hit, want a miss")
	}
	if _, ok := c.Person("nobody"); ok {
		t.Error("Person(nobody) hit, want a miss")
	}
	if _, ok := c.PersonUserID("nobody"); ok {
		t.Error("PersonUserID(nobody) hit, want a miss")
	}
	if _, ok := c.PersonChat("nobody"); ok {
		t.Error("PersonChat(nobody) hit, want a miss")
	}
}

func TestEntityCacheKeysCollapseToOneEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	c.SetClock(func() time.Time { return fixedNow })
	c.PutTeam("Engineering", "team-1")
	c.PutTeam(" engineering ", "team-2") // the same entry: the newest wins
	if id, ok := c.Team("ENGINEERING"); !ok || id != "team-2" {
		t.Errorf("Team = (%q, %v), want (team-2, true)", id, ok)
	}
	c.PutPerson("yuki", Person{UserID: "user-yuki"})
	c.PutPerson("@Yuki", Person{UserID: "user-yuki"})              // "yuki" and "@yuki" are one key
	c.PutPerson("yuki@colorkrew.com", Person{UserID: "user-yuki"}) // a mail key is another
	if got := c.Stats(fixedNow).Entries; got != 3 {
		t.Errorf("Stats.entries = %d, want 3 after normalizing keys", got)
	}
	var names []string
	c.ForEach(func(_ string, entry Entry) { names = append(names, entry.Name) })
	want := []string{"engineering", "@Yuki", "yuki@colorkrew.com"}
	if len(names) != len(want) {
		t.Fatalf("ForEach visited %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("entry %d name = %q, want %q", i, names[i], want[i])
		}
	}
}

func TestEntityCacheIgnoresUnusableInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	c.PutTeam("", "team-1")
	c.PutTeam("   ", "team-1")
	c.PutTeam("Engineering", "")
	c.PutChannel("", "General", "chan-1")
	c.PutChannel("team-eng", "", "chan-1")
	c.PutChannel("team-eng", "General", "")
	c.PutPerson("", Person{UserID: "user-1"})
	c.PutPerson("yuki", Person{DisplayName: "no user id"})
	c.PutPerson("   ", Person{UserID: "user-1"})
	c.PutPersonChat("", "chat-1")
	c.PutPersonChat("yuki", "")
	if c.Dirty() {
		t.Error("ignored writes marked the cache dirty")
	}
	if got := c.Stats(fixedNow).Entries; got != 0 {
		t.Errorf("Stats.entries = %d, want 0", got)
	}
	// Nothing to save means no file at all, which is what a read-only command
	// must leave behind.
	if err := c.Save(path); err != nil {
		t.Fatalf("Save = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("an empty cache wrote %s", path)
	}
}

func TestEntityCacheTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	seededCache(t, path)
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}

	// PLAN.md:170: a name lives about an hour, so it is still there at t+30m.
	if _, ok := c.Team("Engineering"); !ok {
		t.Fatal("the seeded name is missing")
	}
	if n := c.Prune(fixedNow.Add(30 * time.Minute)); n != 0 {
		t.Fatalf("Prune(t+30m) dropped %d entries, want 0", n)
	}
	if c.Dirty() {
		t.Error("a prune that dropped nothing marked the cache dirty")
	}
	if got := c.Stats(fixedNow.Add(30 * time.Minute)).Entries; got != 6 {
		t.Errorf("Stats(t+30m).entries = %d, want 6", got)
	}
	// After two hours the names are stale: two teams and one channel.
	if n := c.Prune(fixedNow.Add(2 * time.Hour)); n != 3 {
		t.Fatalf("Prune(t+2h) dropped %d entries, want 3", n)
	}
	if _, ok := c.Team("Engineering"); ok {
		t.Error("the stale team name survived Prune(t+2h)")
	}
	if _, ok := c.Team("Payments"); ok {
		t.Error("the stale team name survived Prune(t+2h)")
	}
	if _, ok := c.Channel("team-eng", "General"); ok {
		t.Error("the stale channel name survived Prune(t+2h)")
	}
	if !c.Dirty() {
		t.Error("Prune did not mark the cache dirty")
	}
	if stats := c.Stats(fixedNow.Add(2 * time.Hour)); stats.Entries != 3 || !stats.Oldest.Equal(fixedNow) {
		t.Errorf("Stats(t+2h) = %+v, want the 3 person entries", stats)
	}
	// People and their 1:1 chats live a week: still there after 6 days.
	if n := c.Prune(fixedNow.Add(6 * 24 * time.Hour)); n != 0 {
		t.Fatalf("Prune(t+6d) dropped %d entries, want 0", n)
	}
	if _, ok := c.Person("@yuki"); !ok {
		t.Error("the person entry did not survive 6 days")
	}
	if _, ok := c.PersonChat("@Yuki"); !ok {
		t.Error("the 1:1 chat did not survive 6 days")
	}
	if n := c.Prune(fixedNow.Add(8 * 24 * time.Hour)); n != 3 {
		t.Fatalf("Prune(t+8d) dropped %d entries, want 3 (two people and a chat)", n)
	}
	if _, ok := c.Person("@yuki"); ok {
		t.Error("the person entry survived 8 days")
	}
	if _, ok := c.PersonChat("@Yuki"); ok {
		t.Error("the 1:1 chat survived 8 days")
	}
	if stats := c.Stats(fixedNow.Add(8 * 24 * time.Hour)); stats.Entries != 0 || !stats.Oldest.IsZero() {
		t.Errorf("Stats(t+8d) = %+v, want an empty cache", stats)
	}
	// The pruned cache saves and reloads clean.
	if err := c.Save(path); err != nil {
		t.Fatalf("Save = %v", err)
	}
	again, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Problem() != nil || again.Dirty() || again.Stats(fixedNow).Entries != 0 {
		t.Errorf("the pruned cache reloaded with %d entries and problem %v", again.Stats(fixedNow).Entries, again.Problem())
	}
}

func TestEntityCachePruneTreatsAnUndatedEntryAsStale(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	if err := WriteFile(path, []byte("{\n  \"version\": 2,\n  \"teams\": {\"engineering\": {\"id\": \"team-eng\", \"at\": \"0001-01-01T00:00:00Z\"}}\n}\n")); err != nil {
		t.Fatal(err)
	}
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := c.Team("Engineering"); !ok || id != "team-eng" {
		t.Fatalf("Team = (%q, %v), want an entry without a timestamp to be readable", id, ok)
	}
	if got := c.Stats(fixedNow).Entries; got != 0 {
		t.Errorf("Stats.entries = %d, want 0 for an entry we cannot date", got)
	}
	if n := c.Prune(fixedNow); n != 1 {
		t.Errorf("Prune dropped %d entries, want 1", n)
	}
}

func TestEntityCacheMissingFileIsAnEmptyCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatalf("LoadEntities = %v, want nil for a missing file", err)
	}
	if c == nil {
		t.Fatal("LoadEntities returned no cache")
	}
	if c.Problem() != nil {
		t.Errorf("Problem = %v, want nil", c.Problem())
	}
	if c.Dirty() {
		t.Error("a missing file made the cache dirty")
	}
	if stats := c.Stats(fixedNow); stats.Entries != 0 || !stats.Oldest.IsZero() {
		t.Errorf("Stats = %+v, want an empty cache", stats)
	}
	c.Clear()
	if c.Dirty() {
		t.Error("Clear on an empty cache marked it dirty")
	}
}

func TestEntityCacheEmptyFileIsAnEmptyCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	if err := WriteFile(path, nil); err != nil {
		t.Fatal(err)
	}
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatalf("LoadEntities = %v, want nil for an empty file", err)
	}
	if c.Problem() != nil {
		t.Errorf("Problem = %v, want nil for an empty file", c.Problem())
	}
	if got := c.Stats(fixedNow).Entries; got != 0 {
		t.Errorf("Stats.entries = %d, want 0", got)
	}
}

func TestEntityCacheCorruptFileIsRebuildable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	const broken = "{not json"
	if err := WriteFile(path, []byte(broken)); err != nil {
		t.Fatal(err)
	}
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatalf("a corrupt cache must not be fatal: %v", err)
	}
	if !errors.Is(c.Problem(), ErrCorruptCache) {
		t.Fatalf("Problem = %v, want ErrCorruptCache", c.Problem())
	}
	if c.Dirty() {
		t.Error("a corrupt file made the cache dirty")
	}
	if _, ok := c.Team("Engineering"); ok {
		t.Error("a corrupt cache produced a hit")
	}
	// A read-only command must leave the broken file exactly as it was ...
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	after, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != broken {
		t.Fatalf("Save rewrote a corrupt cache: %q", after)
	}
	// ... and once a name is resolved again the rebuild replaces it.
	c.SetClock(func() time.Time { return fixedNow })
	c.PutTeam("Engineering", "team-eng")
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if c.Problem() != nil {
		t.Errorf("Problem = %v, want nil after a rebuild", c.Problem())
	}
	rebuilt, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Problem() != nil {
		t.Errorf("the rebuilt cache reports %v", rebuilt.Problem())
	}
	if id, ok := rebuilt.Team("engineering"); !ok || id != "team-eng" {
		t.Errorf("Team = (%q, %v), want the rebuilt entry", id, ok)
	}
}

func TestEntityCacheUnknownVersionIsRebuildable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	if err := WriteFile(path, []byte("{\"version\": 99, \"teams\": {\"engineering\": {\"id\": \"team-eng\"}}}")); err != nil {
		t.Fatal(err)
	}
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatalf("an unknown version must not be fatal: %v", err)
	}
	if !errors.Is(c.Problem(), ErrCorruptCache) {
		t.Fatalf("Problem = %v, want ErrCorruptCache", c.Problem())
	}
	if _, ok := c.Team("Engineering"); ok {
		t.Error("a future schema was trusted instead of rebuilt")
	}
}

func TestEntityCacheReportsReadErrors(t *testing.T) {
	// A directory is not a readable cache file: a real I/O failure is returned,
	// together with a usable empty cache.
	dir := t.TempDir()
	c, err := LoadEntities(dir)
	if err == nil {
		t.Fatal("LoadEntities accepted a directory")
	}
	if c == nil {
		t.Fatal("LoadEntities returned no cache alongside the error")
	}
	if c.Problem() != nil {
		t.Errorf("Problem = %v, want nil: the returned error is the signal", c.Problem())
	}
	if got := c.Stats(fixedNow).Entries; got != 0 {
		t.Errorf("Stats.entries = %d, want 0", got)
	}
}

func TestEntityCacheSkipsUnusableRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	if err := WriteFile(path, []byte("{\n  \"version\": 2,\n  \"teams\": {\n    \"\": {\"id\": \"team-empty-key\"},\n    \"Undated\": {\"id\": \"\"},\n    \"Engineering\": {\"id\": \"team-eng\", \"input\": \"Engineering\", \"at\": \"2026-02-01T12:00:00Z\"},\n    \" payments \": {\"id\": \"team-pay\", \"at\": \"2026-02-01T12:00:00Z\"}\n  },\n  \"channels\": {\n    \"team-eng/general\": {\"id\": \"chan-general\", \"input\": \"General\", \"at\": \"2026-02-01T12:00:00Z\"},\n    \"no-separator\": {\"id\": \"chan-nope\"},\n    \"team-eng/\": {\"id\": \"chan-nope\"},\n    \"team-eng/empty\": {\"id\": \"\"}\n  },\n  \"people\": {\n    \"\": {\"id\": \"user-empty-key\"},\n    \"nobody\": {\"id\": \"\"},\n    \"@yuki\": {\"id\": \"user-yuki\", \"input\": \"@Yuki\", \"display_name\": \"Yuki Tanaka\", \"mail\": \"yuki@colorkrew.com\", \"at\": \"2026-02-01T12:00:00Z\"}\n  },\n  \"person_chats\": {\n    \"yuki\": {\"id\": \"chat-yuki\", \"at\": \"2026-02-01T12:00:00Z\"},\n    \"\": {\"id\": \"chat-empty-key\"}\n  },\n  \"events\": {\n    \"a1b2c3d\": {\"id\": \"AAMkADevent==\", \"input\": \"a1b2c3d\", \"at\": \"2026-02-01T12:00:00Z\"},\n    \"\": {\"id\": \"event-empty-key\"},\n    \"deadbeef\": {\"id\": \"\"}\n  }\n}\n")); err != nil {
		t.Fatal(err)
	}
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Problem() != nil {
		t.Fatalf("Problem = %v, want nil for a well-formed file", c.Problem())
	}
	if id, ok := c.Team("Engineering"); !ok || id != "team-eng" {
		t.Errorf("Team = (%q, %v)", id, ok)
	}
	if id, ok := c.Team("payments"); !ok || id != "team-pay" {
		t.Errorf("a key from the file was not normalized: (%q, %v)", id, ok)
	}
	if id, ok := c.Channel("team-eng", "General"); !ok || id != "chan-general" {
		t.Errorf("Channel = (%q, %v)", id, ok)
	}
	if p, ok := c.Person("@yuki"); !ok || p.UserID != "user-yuki" || p.DisplayName != "Yuki Tanaka" {
		t.Errorf("Person = (%+v, %v)", p, ok)
	}
	if id, ok := c.PersonChat("@Yuki"); !ok || id != "chat-yuki" {
		t.Errorf("PersonChat = (%q, %v)", id, ok)
	}
	if _, ok := c.Team("Undated"); ok {
		t.Error("a record without an id was kept")
	}
	if _, ok := c.Team(""); ok {
		t.Error("a record without a key was kept")
	}
	if _, ok := c.Channel("nothing", "no-separator"); ok {
		t.Error("a channel key without a team id was kept")
	}
	if _, ok := c.Person(""); ok {
		t.Error("a person record without a key was kept")
	}
	// Two teams, one channel, one person, one person chat and one event handle
	// are usable.
	if got := c.Stats(fixedNow).Entries; got != 6 {
		t.Errorf("Stats.entries = %d, want the 6 usable records", got)
	}
}

func TestEntityCacheSaveIsPrivateVersionedAndStable(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one", "entities.json")
	second := filepath.Join(dir, "two", "entities.json")
	seededCache(t, first)
	// The same entries written in the other order produce the same bytes, so a
	// rewrite shows a real diff (and the file is stable for tests).
	c, err := LoadEntities(second)
	if err != nil {
		t.Fatal(err)
	}
	c.SetClock(func() time.Time { return fixedNow })
	c.PutPersonChat("@Yuki", "chat-yuki")
	c.PutChannel("team-eng", "General", "chan-general")
	c.PutTeam("  Payments ", "team-pay")
	c.PutPerson("@Yuki", Person{UserID: "user-yuki", DisplayName: "Yuki Tanaka", Mail: "yuki@colorkrew.com"})
	c.PutPerson("yuki@colorkrew.com", Person{UserID: "user-yuki", DisplayName: "Yuki Tanaka", Mail: "yuki@colorkrew.com"})
	c.PutTeam("Engineering", "team-eng")
	if err := c.Save(second); err != nil {
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
		t.Errorf("the same cache wrote different bytes:\n%s\n%s", one, two)
	}
	var file map[string]any
	if err := json.Unmarshal(one, &file); err != nil {
		t.Fatalf("the cache is not valid JSON: %v", err)
	}
	if version, ok := file["version"].(float64); !ok || version != 2 {
		t.Errorf("version = %v, want 2", file["version"])
	}
	teams, ok := file["teams"].(map[string]any)
	if !ok || len(teams) != 2 {
		t.Errorf("teams = %v, want the two seeded teams", file["teams"])
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(first)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != FileMode {
			t.Errorf("file mode = %v, want %v", perm, FileMode)
		}
		di, err := os.Stat(filepath.Dir(first))
		if err != nil {
			t.Fatal(err)
		}
		if perm := di.Mode().Perm(); perm != DirMode {
			t.Errorf("directory mode = %v, want %v", perm, DirMode)
		}
	}
}

func TestEntityCacheFailedSaveKeepsThePreviousFile(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs an unwritable directory and a non-root user")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "entities.json")
	seededCache(t, path)
	before, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	c, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	c.SetClock(func() time.Time { return fixedNow })
	c.PutTeam("Sales", "team-sales")
	// An unwritable directory: the temporary file cannot be created, so the
	// rename never happens and the previous cache must survive untouched.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // restoring the test fixture
	if err := c.Save(path); err == nil {
		t.Fatal("Save succeeded in an unwritable directory")
	}
	if !c.Dirty() {
		t.Error("a failed save cleared the dirty flag, so a retry would be skipped")
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("a failed save changed the file:\n%s\n%s", before, after)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the failed save left %d entries behind, want 1: %v", len(entries), entries)
	}
	// The retry, once the directory is writable again, lands.
	if err := c.Save(path); err != nil {
		t.Fatalf("Save after the failure = %v", err)
	}
	reloaded, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := reloaded.Team("Sales"); !ok || id != "team-sales" {
		t.Errorf("Team(Sales) = (%q, %v) after the retry", id, ok)
	}
}

func TestEntityCacheReadOnlyCommandsDoNotRewriteTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entities.json")
	seededCache(t, path)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The pattern every read-only command uses: load, look names up, count
	// them, prune, and save only when something changed.
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	c.SetClock(func() time.Time { return fixedNow.Add(30 * time.Minute) })
	if _, ok := c.Team("engineering"); !ok {
		t.Fatal("the lookup failed")
	}
	if got := c.Stats(fixedNow.Add(30 * time.Minute)).Entries; got != 6 {
		t.Fatalf("Stats.entries = %d, want 6", got)
	}
	if n := c.Prune(fixedNow.Add(30 * time.Minute)); n != 0 {
		t.Fatalf("Prune dropped %d entries, want 0", n)
	}
	visited := 0
	c.ForEach(func(string, Entry) { visited++ })
	if visited != 6 {
		t.Fatalf("ForEach visited %d entries, want 6", visited)
	}
	if c.Dirty() {
		t.Fatal("a read-only command dirtied the cache")
	}
	if c.Dirty() {
		if err := c.Save(path); err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("the cache file was rewritten at %v, want %v", after.ModTime(), before.ModTime())
	}
	again, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(original) {
		t.Error("the cache file changed during a read-only command")
	}
}

func TestEntityCacheClearDropsEverything(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	c := seededCache(t, path)
	c.Clear()
	if !c.Dirty() {
		t.Error("Clear did not mark the cache dirty, so the file would keep the old entries")
	}
	if stats := c.Stats(fixedNow); stats.Entries != 0 || !stats.Oldest.IsZero() {
		t.Errorf("Stats = %+v, want an empty cache", stats)
	}
	if _, ok := c.Team("Engineering"); ok {
		t.Error("Clear left a team entry behind")
	}
	visited := 0
	c.ForEach(func(string, Entry) { visited++ })
	if visited != 0 {
		t.Errorf("ForEach visited %d entries after Clear", visited)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if c.Dirty() {
		t.Error("Save after Clear left the cache dirty")
	}
	// The cleared file is a valid, empty, versioned cache, not a corrupt one.
	empty, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Problem() != nil {
		t.Errorf("Problem = %v, want nil for a cleared cache", empty.Problem())
	}
	if got := empty.Stats(fixedNow).Entries; got != 0 {
		t.Errorf("Stats.entries = %d, want 0", got)
	}
	// Clearing an already empty cache leaves the file alone.
	if empty.Dirty() {
		t.Error("a cleared cache reloaded dirty")
	}
}

func TestNilEntityCacheBehavesAsEmpty(t *testing.T) {
	var c *EntityCache
	c.PutTeam("Engineering", "team-eng")
	c.PutChannel("team-eng", "General", "chan-general")
	c.PutPerson("yuki", Person{UserID: "user-yuki"})
	c.PutPersonChat("yuki", "chat-yuki")
	c.Clear()
	c.SetClock(func() time.Time { return fixedNow })
	if c.Dirty() {
		t.Error("a nil cache reports dirty")
	}
	if c.Problem() != nil {
		t.Errorf("Problem = %v, want nil", c.Problem())
	}
	if _, ok := c.Team("Engineering"); ok {
		t.Error("a nil cache produced a team hit")
	}
	if _, ok := c.Channel("team-eng", "General"); ok {
		t.Error("a nil cache produced a channel hit")
	}
	if _, ok := c.Person("yuki"); ok {
		t.Error("a nil cache produced a person hit")
	}
	if _, ok := c.PersonUserID("yuki"); ok {
		t.Error("a nil cache produced a user id")
	}
	if _, ok := c.PersonChat("yuki"); ok {
		t.Error("a nil cache produced a chat id")
	}
	if got := c.Stats(fixedNow); got.Entries != 0 || !got.Oldest.IsZero() {
		t.Errorf("Stats = %+v, want an empty cache", got)
	}
	if n := c.Prune(fixedNow); n != 0 {
		t.Errorf("Prune = %d, want 0", n)
	}
	c.ForEach(func(string, Entry) { t.Error("ForEach called fn on a nil cache") })
	if err := c.Save(filepath.Join(t.TempDir(), "entities.json")); err != nil {
		t.Errorf("Save on a nil cache = %v, want nil", err)
	}
}

func TestEntityCacheForEachToleratesANilFunction(t *testing.T) {
	c := seededCache(t, filepath.Join(t.TempDir(), "entities.json"))
	c.ForEach(nil)
}

func TestEntityCacheSetClockRestoresTheWallClock(t *testing.T) {
	c := seededCache(t, filepath.Join(t.TempDir(), "entities.json"))
	c.SetClock(nil)
	c.PutTeam("Sales", "team-sales")
	visited := 0
	c.ForEach(func(_ string, entry Entry) {
		if entry.Kind != KindTeam || entry.Key != "sales" {
			return
		}
		visited++
		if entry.At.IsZero() || time.Since(entry.At) > time.Minute {
			t.Errorf("At = %v, want the wall clock after SetClock(nil)", entry.At)
		}
	})
	if visited != 1 {
		t.Fatalf("the entry written after SetClock(nil) is missing")
	}
	var nilCache *EntityCache
	nilCache.SetClock(nil)
}

func TestEntityCacheStampFallsBackToTheWallClock(t *testing.T) {
	// The zero value is not a supported cache, but a hand-built one must not
	// panic on the clock: it falls back to the wall clock.
	var c EntityCache
	c.teams = map[string]entityRecord{}
	c.PutTeam("Engineering", "team-eng")
	rec, ok := c.teams["engineering"]
	if !ok {
		t.Fatal("PutTeam did not store the entry")
	}
	if rec.At.IsZero() || time.Since(rec.At) > time.Minute {
		t.Errorf("At = %v, want the wall clock", rec.At)
	}
}

func TestEntityCacheLockPathIsPerProfile(t *testing.T) {
	t.Setenv(EnvConfig, filepath.Join(t.TempDir(), "config.toml"))
	t.Setenv(EnvCache, t.TempDir())
	t.Setenv(EnvState, t.TempDir())
	paths, err := Resolve("me")
	if err != nil {
		t.Fatal(err)
	}
	// The alias file's lock is the profile lock; the cache file gets the cache
	// directory's own lock, so the two never contend.
	if got := LockPath(paths.AliasesFile()); got != paths.LockFile() {
		t.Errorf("LockPath(aliases) = %q, want %q", got, paths.LockFile())
	}
	if got, want := LockPath(paths.EntityCacheFile()), filepath.Join(paths.CacheDir, ".lock"); got != want {
		t.Errorf("LockPath(entities) = %q, want %q", got, want)
	}
}

func TestEntityCacheConcurrentWritersThroughTheLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "entities.json")
	lock := LockPath(path)
	const writers = 4
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := fmt.Sprintf("team-%d", i)
			// The read-modify-write holds the per-profile lock; without it the
			// last writer would win and the other names would vanish.
			err := WithLock(context.Background(), lock, 5*time.Second, func() error {
				c, err := LoadEntities(path)
				if err != nil {
					return err
				}
				c.SetClock(func() time.Time { return fixedNow })
				c.PutTeam(name, "id-"+name)
				return c.Save(path)
			})
			if err != nil {
				t.Errorf("writer %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	c, err := LoadEntities(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Problem() != nil {
		t.Fatalf("the concurrently written file does not parse: %v", c.Problem())
	}
	if got := c.Stats(fixedNow).Entries; got != writers {
		t.Errorf("Stats.entries = %d, want %d: a writer lost its entry", got, writers)
	}
	for i := 0; i < writers; i++ {
		name := fmt.Sprintf("Team-%d", i)
		if id, ok := c.Team(name); !ok || id != "id-team-"+fmt.Sprint(i) {
			t.Errorf("Team(%q) = (%q, %v): a writer lost its entry", name, id, ok)
		}
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Errorf("the lock file was left behind: %v", err)
	}
}
