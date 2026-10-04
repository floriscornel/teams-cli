package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Entity cache: the per-profile, rebuildable memory for name arguments and for
// the AI tools.
//
// PLAN.md:255 puts it in the cache directory (rebuildable, deleted by
// `teams cache clear`, never by anything that touches secrets), PLAN.md:292 makes
// it the resolution memory shared by `internal/ref` and `teams ai` (so a person
// the AI resolved is resolved instantly by `teams post @yuki …` and the other
// way around), and PLAN.md:170 gives it the TTLs below.
//
// The cache holds names and mail addresses rather than secrets, but it is
// written with the same 0600 file / 0700 directory permissions as every other
// per-profile file (PLAN.md:259), atomically, so a crash never leaves a
// half-written cache behind.
//
// A missing or corrupt cache is never a fatal error: it is rebuildable data, so
// it loads as an empty cache and the fact is recorded in Problem() for the
// caller to warn about.
//
// Everything is per profile, because the bot identity and the personal identity
// must never see each other's names (PLAN.md:249).

// Entry kinds reported by ForEach.
const (
	// KindTeam marks a team name → team id entry.
	KindTeam = "team"
	// KindChannel marks a (team id, channel name) → channel id entry.
	KindChannel = "channel"
	// KindPerson marks a person key → user id entry.
	KindPerson = "person"
	// KindPersonChat marks a person key → 1:1 chat id entry.
	KindPersonChat = "person_chat"
)

// TTLs from PLAN.md:170: "a TTL of about 1h for names and 7 days for person →
// user id and person → 1:1 chat id".
const (
	// NameTTL is how long a team or channel name stays usable. Names change
	// under us - a channel can be renamed or deleted - so they are re-resolved
	// often; `--refresh` bypasses the cache entirely (PLAN.md:170).
	NameTTL = time.Hour

	// PersonTTL is how long a person key → user id or person key → 1:1 chat id
	// mapping stays usable. People and their 1:1 chats change far less often
	// than channel names, so the plan allows a week.
	PersonTTL = 7 * 24 * time.Hour
)

// entityCacheVersion is the schema version stamped into entities.json. A file
// with any other version is treated exactly like a corrupt one: the cache is
// rebuildable, so a mismatch costs one round of name resolution and never a
// fatal error.
const entityCacheVersion = 1

// ErrCorruptCache reports an entity cache file that could not be used: invalid
// JSON or an unknown schema version. LoadEntities never fails because of it -
// the returned cache is empty and usable - and records the error in Problem()
// so a caller can warn and rebuild (PLAN.md:255: the entity cache is
// rebuildable data).
var ErrCorruptCache = errors.New("entity cache is corrupt or its version is unknown")

// Person is a resolved person: the shapes PLAN.md:167 needs for `@name` and
// `--mention`, since a mention needs the user id and mentions carry a display
// name. Mail is empty when membership did not report one.
type Person struct {
	// UserID is the Entra object id.
	UserID string
	// DisplayName is the name Teams shows, used by mention rendering.
	DisplayName string
	// Mail is the address membership reported, when it reported one.
	Mail string
}

// Entry is one cached entity as exposed by ForEach. It carries the normalized
// lookup key, the kind, the resolved id, the spelling the user typed (kept so
// output can echo it back, PLAN.md:165) and when the entry was stored.
type Entry struct {
	// Key is the normalized key a lookup uses, not the typed spelling.
	Key string
	// Kind is one of KindTeam, KindChannel, KindPerson, KindPersonChat.
	Kind string
	// Value is the resolved id: a team, channel, user or chat id.
	Value string
	// Name is the spelling the user typed, for output.
	Name string
	// At is when the entry was stored.
	At time.Time
}

// CacheStats summarises a cache for `teams cache info`, which prints an entry
// count and the age of the entity cache (PLAN.md:260).
type CacheStats struct {
	// Entries is how many entries are still inside their TTL at the passed
	// instant.
	Entries int
	// Oldest is when the oldest entry still inside its TTL was stored, or the
	// zero time when there is none.
	Oldest time.Time
}

// EntityCache is the per-profile entity cache: team and channel names → ids,
// people → user ids and people → 1:1 chat ids (PLAN.md:255, PLAN.md:292).
//
// Every method is safe on a nil *EntityCache and behaves as an empty cache, so
// a caller that could not load one does not need a special case.
//
// The zero EntityCache is not usable: build one with LoadEntities.
type EntityCache struct {
	// clock stamps new entries. It is injected (SetClock) because PutTeam and
	// friends take no time argument: the write side is the only place that
	// produces a timestamp, while every TTL decision takes now as a parameter
	// and therefore stays deterministic in tests.
	clock func() time.Time

	teams    map[string]entityRecord
	channels map[string]entityRecord
	people   map[string]personRecord
	chats    map[string]entityRecord

	// dirty records a change since the last Save, so a read-only command never
	// rewrites the file (PLAN.md:260: `cache info` makes no network calls and
	// only reads).
	dirty bool
	// problem records a cache file we could not use, for the caller to warn
	// about without treating it as fatal.
	problem error
}

// entityRecord is one team, channel or person-chat entry on disk.
type entityRecord struct {
	// ID is the resolved team, channel or chat id.
	ID string `json:"id"`
	// Input is the spelling the user typed, so output can show it.
	Input string `json:"input,omitempty"`
	// At is when the entry was stored.
	At time.Time `json:"at"`
}

// personRecord is one person entry on disk: the resolved user id plus the
// fields a mention needs.
type personRecord struct {
	entityRecord
	// DisplayName is the name Teams shows for the person.
	DisplayName string `json:"display_name,omitempty"`
	// Mail is the address membership reported, when it reported one.
	Mail string `json:"mail,omitempty"`
}

// entityCacheFile is the JSON shape of entities.json. Maps give every lookup an
// O(1) hit, and encoding/json sorts map keys, so the file is stable across
// saves.
type entityCacheFile struct {
	// Version is the schema version, always entityCacheVersion when we write.
	Version int `json:"version"`
	// Teams maps a normalized team name to its team id.
	Teams map[string]entityRecord `json:"teams,omitempty"`
	// Channels maps "<team id>/<normalized channel name>" to a channel id.
	Channels map[string]entityRecord `json:"channels,omitempty"`
	// People maps a normalized person key to the person it resolved to.
	People map[string]personRecord `json:"people,omitempty"`
	// Chats maps a normalized person key to the id of the 1:1 chat with them.
	Chats map[string]entityRecord `json:"person_chats,omitempty"`
}

// defaultClock is the default for the injected clock and the only place this
// package reads the wall clock. No TTL decision comes from it: Prune and Stats
// take now as a parameter, which keeps them deterministic, and the write side
// takes its timestamp from the clock the caller installed through SetClock.
func defaultClock() time.Time { return time.Now() }

// newEntityCache returns an empty cache that stamps entries with the wall
// clock.
func newEntityCache() *EntityCache {
	return &EntityCache{
		clock:    defaultClock,
		teams:    map[string]entityRecord{},
		channels: map[string]entityRecord{},
		people:   map[string]personRecord{},
		chats:    map[string]entityRecord{},
	}
}

// LoadEntities reads the entity cache at path, normally
// Paths.EntityCacheFile().
//
// A missing file is not an error: it returns an empty cache with a nil error
// and a nil Problem. A corrupt file (invalid JSON, or a version this build does
// not know) is not an error either - the cache is rebuildable (PLAN.md:255) -
// but the fact is recorded in Problem() so the caller can warn, and the cache
// stays clean so a read-only command does not rewrite the broken file. A real
// read failure (permissions, an unreadable path) is returned, together with a
// usable empty cache.
func LoadEntities(path string) (*EntityCache, error) {
	c := newEntityCache()
	data, err := ReadFile(path)
	if err != nil {
		return c, err
	}
	if len(data) == 0 {
		// Nothing has been written yet; an empty file is an empty cache.
		return c, nil
	}
	var file entityCacheFile
	if err := json.Unmarshal(data, &file); err != nil {
		c.problem = fmt.Errorf("%w: %s: %w", ErrCorruptCache, path, err)
		return c, nil
	}
	if file.Version != entityCacheVersion {
		c.problem = fmt.Errorf("%w: %s: version %d, want %d", ErrCorruptCache, path, file.Version, entityCacheVersion)
		return c, nil
	}
	for key, rec := range file.Teams {
		if normalized := normalizeKey(key); normalized != "" && rec.ID != "" {
			c.teams[normalized] = rec
		}
	}
	for key, rec := range file.Channels {
		if key = normalizeChannelKey(key); key != "" && rec.ID != "" {
			c.channels[key] = rec
		}
	}
	for key, rec := range file.People {
		if normalized := normalizePersonKey(key); normalized != "" && rec.ID != "" {
			c.people[normalized] = rec
		}
	}
	for key, rec := range file.Chats {
		if normalized := normalizePersonKey(key); normalized != "" && rec.ID != "" {
			c.chats[normalized] = rec
		}
	}
	return c, nil
}

// SetClock replaces the clock that stamps new entries. LoadEntities installs
// the wall clock; a test injects a fixed one so a TTL assertion does not depend
// on the machine clock. Passing nil restores the wall clock.
func (c *EntityCache) SetClock(now func() time.Time) {
	if c == nil {
		return
	}
	if now == nil {
		now = defaultClock
	}
	c.clock = now
}

// stamp returns the timestamp for a new entry.
func (c *EntityCache) stamp() time.Time {
	if c.clock == nil {
		return defaultClock()
	}
	return c.clock()
}

// Problem reports why the loaded cache could not be used, or nil when it was
// loaded (or did not exist) normally. The returned error wraps ErrCorruptCache.
// A later Save rewrites the file in the current version and clears it.
func (c *EntityCache) Problem() error {
	if c == nil {
		return nil
	}
	return c.problem
}

// Dirty reports whether anything changed since the last Save, so a read-only
// command can skip the write (and never rewrite the file on a mere lookup). Put
// methods, Clear and a Prune that dropped something set it; Save clears it. A
// nil cache is never dirty.
func (c *EntityCache) Dirty() bool {
	return c != nil && c.dirty
}

// Save writes the cache to path, normally Paths.EntityCacheFile(), as atomic
// 0600 JSON with parent directories at 0700 (PLAN.md:259). It is a no-op when
// the cache is not dirty, so the "load, read, save only if dirty" pattern never
// touches the disk, and a nil cache has nothing to save. On success the
// recorded Problem is cleared, because the file is now one this build wrote.
func (c *EntityCache) Save(path string) error {
	if c == nil || !c.dirty {
		return nil
	}
	file := entityCacheFile{
		Version:  entityCacheVersion,
		Teams:    c.teams,
		Channels: c.channels,
		People:   c.people,
		Chats:    c.chats,
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		// Unreachable for these types, but encoding must not silently lose the
		// cache when it ever becomes reachable.
		return fmt.Errorf("encode entity cache: %w", err)
	}
	if err := WriteFile(path, append(data, '\n')); err != nil {
		return err
	}
	c.dirty = false
	c.problem = nil
	return nil
}

// Team returns the team id cached for a team name. Names match
// case-insensitively and whitespace-trimmed (PLAN.md:165), so "Engineering" and
// " engineering " are the same entry. Freshness is enforced by Prune, not here,
// so a lookup never depends on the clock.
func (c *EntityCache) Team(name string) (string, bool) {
	if c == nil {
		return "", false
	}
	rec, ok := c.teams[normalizeKey(name)]
	if !ok || rec.ID == "" {
		return "", false
	}
	return rec.ID, true
}

// PutTeam stores a team name → team id mapping, remembering the spelling the
// user typed (trimmed, but with its original case) so output can echo it. An
// empty name or id is ignored, because such an entry could only ever produce a
// bogus hit.
func (c *EntityCache) PutTeam(name, id string) {
	if c == nil || id == "" {
		return
	}
	key := normalizeKey(name)
	if key == "" {
		return
	}
	c.teams[key] = entityRecord{ID: id, Input: strings.TrimSpace(name), At: c.stamp()}
	c.dirty = true
}

// Channel returns the channel id cached for a channel of a team, looked up by
// team id and channel name (PLAN.md:255). Channel names match
// case-insensitively and whitespace-trimmed, and the team id comes from the
// team entry or from a URL's groupId.
func (c *EntityCache) Channel(teamID, name string) (string, bool) {
	if c == nil {
		return "", false
	}
	rec, ok := c.channels[channelKey(teamID, name)]
	if !ok || rec.ID == "" {
		return "", false
	}
	return rec.ID, true
}

// PutChannel stores a (team id, channel name) → channel id mapping. An empty
// team id, name or id is ignored: a channel name alone is not resolvable
// (PLAN.md:164), so an entry without a team could never be used.
func (c *EntityCache) PutChannel(teamID, name, id string) {
	key := channelKey(teamID, name)
	if c == nil || id == "" || key == "" {
		return
	}
	c.channels[key] = entityRecord{ID: id, Input: strings.TrimSpace(name), At: c.stamp()}
	c.dirty = true
}

// Person returns the person a key resolved to, where a key is the typed form
// "@yuki", "yuki" or "yuki@x.com" (PLAN.md:166, PLAN.md:167).
func (c *EntityCache) Person(key string) (Person, bool) {
	if c == nil {
		return Person{}, false
	}
	rec, ok := c.people[normalizePersonKey(key)]
	if !ok || rec.ID == "" {
		return Person{}, false
	}
	return Person{UserID: rec.ID, DisplayName: rec.DisplayName, Mail: rec.Mail}, true
}

// PutPerson stores the person a key resolved to. A person without a user id is
// not stored, because every caller needs the id to mention or open a chat.
func (c *EntityCache) PutPerson(key string, p Person) {
	normalized := normalizePersonKey(key)
	if c == nil || normalized == "" || p.UserID == "" {
		return
	}
	c.people[normalized] = personRecord{
		entityRecord: entityRecord{ID: p.UserID, Input: strings.TrimSpace(key), At: c.stamp()},
		DisplayName:  p.DisplayName,
		Mail:         p.Mail,
	}
	c.dirty = true
}

// PersonUserID returns the user id cached for a person key.
func (c *EntityCache) PersonUserID(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	rec, ok := c.people[normalizePersonKey(key)]
	if !ok || rec.ID == "" {
		return "", false
	}
	return rec.ID, true
}

// PersonChat returns the id of the 1:1 chat with a person. PLAN.md:166 caches
// the scan that finds it, so the chat scan runs once per person.
func (c *EntityCache) PersonChat(key string) (string, bool) {
	if c == nil {
		return "", false
	}
	rec, ok := c.chats[normalizePersonKey(key)]
	if !ok || rec.ID == "" {
		return "", false
	}
	return rec.ID, true
}

// PutPersonChat stores the 1:1 chat id found for a person key. An empty key or
// chat id is ignored.
func (c *EntityCache) PutPersonChat(key, chatID string) {
	normalized := normalizePersonKey(key)
	if c == nil || normalized == "" || chatID == "" {
		return
	}
	c.chats[normalized] = entityRecord{ID: chatID, Input: strings.TrimSpace(key), At: c.stamp()}
	c.dirty = true
}

// ForEach calls fn for every entry, ordered by kind (teams, channels, people,
// person chats) and then by key, so `cache info` counts and tests see a stable
// order. It exists because a cache has no meaningful len() of its own, and it
// reports every stored entry - including ones past their TTL - because it takes
// no clock.
func (c *EntityCache) ForEach(fn func(key string, entry Entry)) {
	if c == nil || fn == nil {
		return
	}
	for _, entry := range c.snapshot() {
		fn(entry.Key, entry)
	}
}

// snapshot returns every entry in ForEach's order.
func (c *EntityCache) snapshot() []Entry {
	out := make([]Entry, 0, c.count())
	for key, rec := range c.teams {
		out = append(out, Entry{Key: key, Kind: KindTeam, Value: rec.ID, Name: rec.Input, At: rec.At})
	}
	for key, rec := range c.channels {
		out = append(out, Entry{Key: key, Kind: KindChannel, Value: rec.ID, Name: rec.Input, At: rec.At})
	}
	for key, rec := range c.people {
		out = append(out, Entry{Key: key, Kind: KindPerson, Value: rec.ID, Name: rec.Input, At: rec.At})
	}
	for key, rec := range c.chats {
		out = append(out, Entry{Key: key, Kind: KindPersonChat, Value: rec.ID, Name: rec.Input, At: rec.At})
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := kindRank(out[i].Kind), kindRank(out[j].Kind)
		if ri != rj {
			return ri < rj
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// kindRank orders kinds for ForEach, in the order of the tables on disk.
func kindRank(kind string) int {
	switch kind {
	case KindTeam:
		return 0
	case KindChannel:
		return 1
	case KindPerson:
		return 2
	case KindPersonChat:
		return 3
	default:
		return 4
	}
}

// count returns how many entries are stored, expired or not.
func (c *EntityCache) count() int {
	return len(c.teams) + len(c.channels) + len(c.people) + len(c.chats)
}

// Prune drops the entries whose TTL ran out at now and reports how many went
// (PLAN.md:170: about an hour for names, 7 days for person → user id and
// person → 1:1 chat id). now is a parameter, never read from the clock, so the
// outcome is deterministic and a test can prune at any instant it likes. A
// prune that dropped something marks the cache dirty, so the file is rewritten
// without the stale entries.
func (c *EntityCache) Prune(now time.Time) int {
	if c == nil {
		return 0
	}
	gone := 0
	for key, rec := range c.teams {
		if expired(rec.At, NameTTL, now) {
			delete(c.teams, key)
			gone++
		}
	}
	for key, rec := range c.channels {
		if expired(rec.At, NameTTL, now) {
			delete(c.channels, key)
			gone++
		}
	}
	for key, rec := range c.people {
		if expired(rec.At, PersonTTL, now) {
			delete(c.people, key)
			gone++
		}
	}
	for key, rec := range c.chats {
		if expired(rec.At, PersonTTL, now) {
			delete(c.chats, key)
			gone++
		}
	}
	if gone > 0 {
		c.dirty = true
	}
	return gone
}

// expired reports whether an entry stored at is past its TTL at now. An entry
// without a timestamp counts as stale: a rebuildable cache would rather resolve
// the name again than serve something it cannot date.
func expired(at time.Time, ttl time.Duration, now time.Time) bool {
	if at.IsZero() {
		return true
	}
	return now.Sub(at) > ttl
}

// Clear drops every entry. It marks the cache dirty only when it actually
// dropped something, so `teams cache clear` on an already empty cache leaves the
// file (and its absence) alone.
func (c *EntityCache) Clear() {
	if c == nil || c.count() == 0 {
		return
	}
	c.teams = nil
	c.channels = nil
	c.people = nil
	c.chats = nil
	c.dirty = true
}

// Stats counts the entries that are still inside their TTL at now and reports
// the age of the oldest of them, which is what `teams cache info` needs
// (PLAN.md:260). now is a parameter so the command can report a consistent
// instant.
func (c *EntityCache) Stats(now time.Time) CacheStats {
	var stats CacheStats
	if c == nil {
		return stats
	}
	count := func(at time.Time, ttl time.Duration) {
		if expired(at, ttl, now) {
			return
		}
		stats.Entries++
		if stats.Oldest.IsZero() || at.Before(stats.Oldest) {
			stats.Oldest = at
		}
	}
	for _, rec := range c.teams {
		count(rec.At, NameTTL)
	}
	for _, rec := range c.channels {
		count(rec.At, NameTTL)
	}
	for _, rec := range c.people {
		count(rec.At, PersonTTL)
	}
	for _, rec := range c.chats {
		count(rec.At, PersonTTL)
	}
	return stats
}

// normalizeKey prepares a name for a lookup: names match case-insensitively and
// whitespace-trimmed (PLAN.md:165), and runs of interior whitespace collapse so
// "Engineering  Daily" cannot produce a second entry for the same channel. The
// typed spelling is kept in the record so output can show what the user wrote.
func normalizeKey(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// normalizePersonKey prepares a person key. The "@" of the typed form is not
// part of the key (PLAN.md:166: "@alice" and "alice" mean the same person);
// a mail-shaped key keeps its address, since that is a different kind of key.
func normalizePersonKey(s string) string {
	return normalizeKey(strings.TrimPrefix(strings.TrimSpace(s), "@"))
}

// channelKey is the cache key for a channel: the team id and the channel name,
// both normalized. The team id is required, because a channel name alone is not
// resolvable (PLAN.md:164).
func channelKey(teamID, name string) string {
	team := normalizeKey(teamID)
	channel := normalizeKey(name)
	if team == "" || channel == "" {
		return ""
	}
	return team + "/" + channel
}

// normalizeChannelKey re-normalizes a key read from the file, which stores it
// as "<team id>/<channel name>".
func normalizeChannelKey(key string) string {
	team, channel, ok := strings.Cut(key, "/")
	if !ok {
		return ""
	}
	return channelKey(team, channel)
}
