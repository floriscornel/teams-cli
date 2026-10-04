// Package update checks GitHub Releases for a newer `teams` and reports it.
//
// It is the one thing in the CLI that talks to a host other than the configured
// cloud, so it is deliberately small, opt-out and boring:
//
//   - it is never automatic in a non-interactive run (PLAN.md:351) and never
//     runs at all when `TEAMS_NO_UPDATE_CHECK` is set or `update_check = false`
//     is in the config;
//   - an answer is cached for [DefaultInterval] in the profile's state dir, so a
//     daily check is one request a day at most;
//   - every failure is the caller's to swallow: an offline machine, a rate limit
//     or a proxy must never turn into a CLI error.
//
// The release list it reads is public and carries no user data; the request
// sends no token and no identifying header beyond the User-Agent the CLI sets
// everywhere.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/floriscornel/teams-cli/internal/store"
)

// Defaults for [Options].
const (
	// DefaultAPIBase is GitHub's REST API root. The check never follows a
	// redirect off this host, and the base is only overridden by the in-process
	// tests (there is no flag or environment variable for it, so a released
	// binary cannot be pointed elsewhere).
	DefaultAPIBase = "https://api.github.com"
	// DefaultRepoPath is the GitHub Releases endpoint for this project.
	DefaultRepoPath = "/repos/floriscornel/teams-cli/releases/latest"
	// DefaultInterval is how long a cached answer is reused.
	DefaultInterval = 24 * time.Hour
	// DefaultTimeout bounds the check's own request. It is short on purpose: a
	// user who did not ask for the check must never wait for it.
	DefaultTimeout = 3 * time.Second
	// unversioned names the builds that have no release to compare against: a
	// plain `go build`, `go run` or a test binary.
	unversioned = "dev"
)

// Options configures a [Checker]. Current is required; everything else has a
// default.
type Options struct {
	// Current is the running binary's version, as the CLI reports it ("0.4.0",
	// "v0.4.0", "v0.4.0-rc.1" or "dev").
	Current string
	// APIBase overrides the GitHub API root. Tests only.
	APIBase string
	// RepoPath overrides the releases endpoint. Tests only.
	RepoPath string
	// HTTPClient defaults to a client with the check's own timeout.
	HTTPClient *http.Client
	// CachePath is where the last answer is stored. Empty disables caching, so
	// every check is a request.
	CachePath string
	// Interval is how long a cached answer is trusted.
	Interval time.Duration
	// Now is the injected clock; it decides whether a cached answer is stale.
	Now func() time.Time
	// UserAgent is sent with the request.
	UserAgent string
}

// Result is what a check found.
type Result struct {
	// Current is the version being compared, as the binary reports it.
	Current string
	// Latest is the newest published release's tag, when one was found.
	Latest string
	// URL is the release's page on GitHub.
	URL string
	// PublishedAt is when that release was published, when GitHub said.
	PublishedAt time.Time
	// Outdated reports that Latest is newer than Current.
	Outdated bool
	// CheckedAt is when the answer was fetched, or when a cached one was
	// originally fetched.
	CheckedAt time.Time
	// FromCache reports that no request was made.
	FromCache bool
	// Unversioned reports a build that has no version to compare (a plain
	// `go build`); Latest is still reported, but Outdated stays false.
	Unversioned bool
}

// Checker performs the check. It is safe to reuse.
type Checker struct {
	current     string
	apiBase     string
	repoPath    string
	http        *http.Client
	cachePath   string
	interval    time.Duration
	now         func() time.Time
	userAgent   string
	unversioned bool
}

// New builds a Checker.
func New(opts Options) *Checker {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: DefaultTimeout}
	}
	base := strings.TrimSuffix(firstNonEmpty(opts.APIBase, DefaultAPIBase), "/")
	path := firstNonEmpty(opts.RepoPath, DefaultRepoPath)
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	current := strings.TrimSpace(opts.Current)
	return &Checker{
		current:     current,
		apiBase:     base,
		repoPath:    path,
		http:        client,
		cachePath:   opts.CachePath,
		interval:    interval,
		now:         now,
		userAgent:   firstNonEmpty(opts.UserAgent, "teams-cli"),
		unversioned: current == "" || strings.EqualFold(current, unversioned) || strings.Contains(current, "(devel)"),
	}
}

// Unversioned reports whether the running binary has a version worth comparing.
func (c *Checker) Unversioned() bool { return c.unversioned }

// release is the subset of GitHub's release object the check uses.
type release struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
}

// cacheEntry is what the cache file holds.
type cacheEntry struct {
	CheckedAt   time.Time `json:"checked_at"`
	Latest      string    `json:"latest,omitempty"`
	URL         string    `json:"url,omitempty"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	Prerelease  bool      `json:"prerelease,omitempty"`
}

// Check answers from the cache when the cached answer is still fresh, and
// otherwise asks GitHub.
func (c *Checker) Check(ctx context.Context) (Result, error) {
	if cached, ok := c.cached(); ok {
		return c.result(cached, true), nil
	}
	return c.CheckNow(ctx)
}

// CheckNow always asks GitHub, ignoring the cache, and stores the answer.
func (c *Checker) CheckNow(ctx context.Context) (Result, error) {
	rel, err := c.fetch(ctx)
	if err != nil {
		return Result{Current: c.current, Unversioned: c.unversioned}, err
	}
	entry := cacheEntry{
		CheckedAt:   c.now().UTC(),
		Latest:      rel.TagName,
		URL:         rel.HTMLURL,
		PublishedAt: rel.PublishedAt,
		Prerelease:  rel.Prerelease,
	}
	// A cache that cannot be written is not an error: the answer is still good.
	_ = c.storeCache(entry)
	return c.result(entry, false), nil
}

// fetch reads the latest release.
func (c *Checker) fetch(ctx context.Context) (release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+c.repoPath, nil)
	if err != nil {
		return release{}, fmt.Errorf("update: build the request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return release{}, fmt.Errorf("update: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// No release yet: not an error, and nothing to report.
		return release{}, nil
	case resp.StatusCode != http.StatusOK:
		return release{}, fmt.Errorf("update: GitHub answered %s", resp.Status)
	}
	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return release{}, fmt.Errorf("update: decode the release: %w", err)
	}
	if rel.Draft {
		return release{}, nil
	}
	return rel, nil
}

// result turns a release (or a cached one) into a Result.
func (c *Checker) result(entry cacheEntry, fromCache bool) Result {
	res := Result{
		Current:     c.current,
		Latest:      entry.Latest,
		URL:         entry.URL,
		PublishedAt: entry.PublishedAt,
		CheckedAt:   entry.CheckedAt,
		FromCache:   fromCache,
		Unversioned: c.unversioned,
	}
	if c.unversioned || entry.Latest == "" {
		return res
	}
	if cmp, err := Compare(entry.Latest, c.current); err == nil && cmp > 0 {
		res.Outdated = true
	}
	return res
}

// cached returns a still-fresh cached answer.
func (c *Checker) cached() (cacheEntry, bool) {
	if c.cachePath == "" {
		return cacheEntry{}, false
	}
	data, err := store.ReadFile(c.cachePath)
	if err != nil {
		return cacheEntry{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cacheEntry{}, false
	}
	if entry.CheckedAt.IsZero() || c.now().Sub(entry.CheckedAt) > c.interval {
		return cacheEntry{}, false
	}
	return entry, true
}

// storeCache writes the answer, atomically and 0600 like every other state file.
func (c *Checker) storeCache(entry cacheEntry) error {
	if c.cachePath == "" {
		return nil
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return store.WriteFile(c.cachePath, append(data, '\n'))
}

// ErrInvalidVersion reports a version string that is not semver-like.
var ErrInvalidVersion = errors.New("update: not a version")

// Compare orders two version strings: -1 when a is older, 0 when they are the
// same version, +1 when a is newer.
//
// It implements the semver ordering rules that matter here (semver 2.0.0 §11):
// an optional "v" prefix, dot-separated numeric parts, an optional prerelease
// that sorts *below* the same version without one, and build metadata (+…) that
// is ignored. A missing part counts as zero, so "1.0" and "1.0.0" are equal.
// A plain `go build` reports "dev", which callers detect separately rather than
// asking Compare to order it.
func Compare(a, b string) (int, error) {
	av, apre, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	bv, bpre, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range 3 {
		switch {
		case av[i] < bv[i]:
			return -1, nil
		case av[i] > bv[i]:
			return 1, nil
		}
	}
	return comparePrerelease(apre, bpre), nil
}

// parseVersion splits a version into its three numeric parts and its
// prerelease, tolerating a "v" prefix and ignoring build metadata.
func parseVersion(v string) ([3]int, string, error) {
	var out [3]int
	v = strings.TrimSpace(v)
	v = strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
	if v == "" {
		return out, "", ErrInvalidVersion
	}
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	prerelease := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, prerelease = v[:i], v[i+1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) > 3 {
		return out, "", fmt.Errorf("%w: %q has too many parts", ErrInvalidVersion, v)
	}
	for i, part := range parts {
		n, err := parseNumeric(part)
		if err != nil {
			return out, "", fmt.Errorf("%w: %q", ErrInvalidVersion, part)
		}
		out[i] = n
	}
	return out, prerelease, nil
}

// parseNumeric parses one numeric part, allowing only digits.
func parseNumeric(s string) (int, error) {
	if s == "" {
		return 0, ErrInvalidVersion
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, ErrInvalidVersion
		}
		n = n*10 + int(r-'0')
		if n > 1<<30 {
			return 0, ErrInvalidVersion
		}
	}
	return n, nil
}

// comparePrerelease orders two prerelease strings by the semver rules: no
// prerelease is newer than any prerelease, numeric identifiers compare as
// numbers, and everything else compares as text.
func comparePrerelease(a, b string) int {
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	aParts, bParts := strings.Split(a, "."), strings.Split(b, ".")
	for i := range max(len(aParts), len(bParts)) {
		switch {
		case i >= len(aParts):
			return -1
		case i >= len(bParts):
			return 1
		}
		an, aErr := parseNumeric(aParts[i])
		bn, bErr := parseNumeric(bParts[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aErr == nil:
			// Numeric identifiers always sort below alphanumeric ones.
			return -1
		case bErr == nil:
			return 1
		default:
			if cmp := strings.Compare(aParts[i], bParts[i]); cmp != 0 {
				return cmp
			}
		}
	}
	return 0
}

// firstNonEmpty returns the first non-blank value, trimmed.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
