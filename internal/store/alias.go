package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// Aliases: the explicit, durable, per-profile names PLAN.md:168 gives every
// command and the AI prompts - `teams alias set boss alice@colorkrew.com`,
// `teams alias set standup Engineering/Daily`.
//
// They live in the state directory (Paths.AliasesFile()), not in the cache:
// they are user data the user typed, so `teams cache clear` must not touch them
// (PLAN.md:255, PLAN.md:256), and PLAN.md:297 makes them the bridge between the
// curated AI memory and the plain commands ("the AI's people facts should
// become aliases where they can").
//
// The file is plain TOML, a flat name → target mapping, written atomically with
// 0600 permissions and sorted, so its bytes are stable (PLAN.md:259).
//
// Reading and writing are separate calls on purpose: a read-only command loads
// and never writes, and a read-modify-write (SetAlias, RemoveAlias) takes the
// per-profile lock around both halves.

// ErrCorruptAliases reports an alias file that could not be parsed. Aliases are
// user data rather than rebuildable cache, so the loaders return the error next
// to an empty, usable map, and SetAlias/RemoveAlias refuse to rewrite the file
// instead of silently dropping what the user configured.
var ErrCorruptAliases = errors.New("alias file is corrupt")

// LockTimeout is how long a read-modify-write waits for the per-profile lock
// before giving up. It is short on purpose: the lock is advisory and
// cross-process (a second `teams` invocation, or two commands in one shell
// script), and a command should fail with a clear message rather than wait
// behind a process that is stuck.
const LockTimeout = 2 * time.Second

// LockPath returns the per-profile lock file that guards path: a ".lock" beside
// it, created 0600 by Acquire. For Paths.AliasesFile() that is exactly
// Paths.LockFile(), the lock every per-profile read-modify-write uses
// (PLAN.md:259). For Paths.EntityCacheFile() it is the cache directory's own
// .lock, so a cache writer never contends with a state writer.
//
// The lock is advisory and cross-process: it excludes our own processes with an
// O_EXCL lock file, it cannot stop any other program from writing the same
// path, and it is not reentrant - a caller that already holds Paths.LockFile()
// must call LoadAliases/SaveAliases directly rather than SetAlias, which would
// wait for its own lock.
func LockPath(path string) string { return filepath.Join(filepath.Dir(path), lockName) }

// Alias is one name → target mapping, for `teams alias list` (PLAN.md:168).
type Alias struct {
	// Name is the name as it is typed (`@boss` on the command line), without the
	// "@", lower-cased like every other name (PLAN.md:165).
	Name string
	// Target is what the name resolves to: a person, a channel path, a team or
	// a raw id, in the same forms the commands accept.
	Target string
}

// LoadAliases reads the alias file at path, normally Paths.AliasesFile(). A
// missing or empty file is not an error: it returns an empty, non-nil map and a
// nil error.
//
// A file that does not parse as a flat TOML name → target table is reported as
// ErrCorruptAliases, together with that same empty map, so a caller can warn
// and continue with no aliases. Names are lower-cased on load, because aliases
// match case-insensitively like every other name (PLAN.md:165).
func LoadAliases(path string) (map[string]string, error) {
	empty := map[string]string{}
	data, err := ReadFile(path)
	if err != nil {
		return empty, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return empty, nil
	}
	raw := map[string]string{}
	if err := toml.Unmarshal(data, &raw); err != nil {
		return empty, fmt.Errorf("%w: %s: %w", ErrCorruptAliases, path, err)
	}
	return normalizeAliases(raw), nil
}

// SaveAliases writes the mapping to path as TOML: atomic, 0600, with parent
// directories at 0700 (PLAN.md:259). Names are lower-cased and the keys are
// sorted, so saving the same mapping twice produces byte-identical files and a
// rewrite shows a one-line diff. A nil map writes an empty file, which
// LoadAliases reads back as an empty mapping.
func SaveAliases(path string, m map[string]string) error {
	data, err := toml.Marshal(normalizeAliases(m))
	if err != nil {
		return fmt.Errorf("encode aliases: %w", err)
	}
	return WriteFile(path, data)
}

// ListAliases returns the aliases sorted by name, which is the order
// `teams alias list` prints and a stable order for tests. The corrupt-file rule
// is the one from LoadAliases.
func ListAliases(path string) ([]Alias, error) {
	m, err := LoadAliases(path)
	if err != nil {
		return nil, err
	}
	out := make([]Alias, 0, len(m))
	for _, name := range sortedNames(m) {
		out = append(out, Alias{Name: name, Target: m[name]})
	}
	return out, nil
}

// SetAlias creates or replaces an alias, so re-running it for the same name is
// how a target is corrected. The name must pass ValidAliasName and the target
// must not be empty; surrounding whitespace is trimmed from both.
//
// The read-modify-write runs under the per-profile lock (LockPath, short
// timeout), so two processes cannot lose each other's aliases. A corrupt file
// is an error rather than something we overwrite, and a caller that already
// holds the lock must not call this.
func SetAlias(path, name, target string) error {
	if !ValidAliasName(name) {
		return fmt.Errorf("invalid alias name %q: use letters, digits, dot, dash or underscore", name)
	}
	target = strings.TrimSpace(target)
	if target == "" {
		return fmt.Errorf("alias %q needs a target", strings.TrimSpace(name))
	}
	key := normalizeAliasName(name)
	return WithLock(context.Background(), LockPath(path), LockTimeout, func() error {
		m, err := LoadAliases(path)
		if err != nil {
			return err
		}
		m[key] = target
		return SaveAliases(path, m)
	})
}

// RemoveAlias deletes an alias and reports whether it existed. A name that
// was never set is not an error (a script can remove without checking first),
// but an invalid name is, because that is a usage mistake rather than a missing
// alias. Nothing is written when the alias was not there, so removing a missing
// alias never rewrites the file.
func RemoveAlias(path, name string) (bool, error) {
	if !ValidAliasName(name) {
		return false, fmt.Errorf("invalid alias name %q: use letters, digits, dot, dash or underscore", name)
	}
	key := normalizeAliasName(name)
	removed := false
	err := WithLock(context.Background(), LockPath(path), LockTimeout, func() error {
		m, err := LoadAliases(path)
		if err != nil {
			return err
		}
		if _, ok := m[key]; !ok {
			return nil
		}
		delete(m, key)
		removed = true
		return SaveAliases(path, m)
	})
	return removed, err
}

// aliasNameRE is the alias name charset: letters, digits, dot, dash and
// underscore. It excludes "@", "/" and whitespace on purpose, because an alias
// is typed as a bare word (`@boss`) and a name must never look like a path, a
// profile or a person key.
var aliasNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidAliasName reports whether a name can be used as an alias: letters,
// digits, dot, dash and underscore, and never empty. Surrounding whitespace is
// trimmed first, so " boss " is a valid spelling of "boss"; the name is stored
// lower-cased, because aliases match case-insensitively.
func ValidAliasName(name string) bool { return aliasNameRE.MatchString(strings.TrimSpace(name)) }

// normalizeAliases is the one spelling of a mapping: names trimmed and
// lower-cased, empty names dropped, and a deterministic winner when two keys
// differ only by case (the later one in sort order, so it is always the same
// key and SaveAliases stays byte-stable).
func normalizeAliases(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for _, name := range sortedNames(m) {
		key := normalizeAliasName(name)
		if key == "" {
			continue
		}
		out[key] = m[name]
	}
	return out
}

// normalizeAliasName is the single spelling of an alias name: trimmed and
// lower-cased.
func normalizeAliasName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// sortedNames returns the keys of m in sorted order.
func sortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
