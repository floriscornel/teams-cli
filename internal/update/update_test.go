package update

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The checker is the one thing in the CLI that talks to github.com, so its tests
// pin the properties that keep it harmless: a cached answer is reused for a day,
// every failure is returned rather than panicking, and no version comparison
// happens for a build that has no version.

var versionNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

// releaseServer serves a GitHub-releases-shaped answer and counts requests.
func releaseServer(t *testing.T, status int, body string) (*httptest.Server, *int) {
	t.Helper()
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Errorf("the request carried no User-Agent")
		}
		if r.URL.Path != DefaultRepoPath {
			t.Errorf("path = %q, want %q", r.URL.Path, DefaultRepoPath)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newChecker(t *testing.T, current, apiBase, cachePath string, now func() time.Time) *Checker {
	t.Helper()
	return New(Options{
		Current:   current,
		APIBase:   apiBase,
		CachePath: cachePath,
		Now:       now,
	})
}

func TestCheckReportsANewerRelease(t *testing.T) {
	srv, hits := releaseServer(t, http.StatusOK, `{"tag_name":"v1.0.0","html_url":"https://example.test/v1.0.0","published_at":"2026-10-01T00:00:00Z"}`)
	cache := filepath.Join(t.TempDir(), "update.json")
	c := newChecker(t, "0.4.0", srv.URL, cache, func() time.Time { return versionNow })

	result, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.Outdated || result.Latest != "v1.0.0" || result.Current != "0.4.0" {
		t.Errorf("result = %+v, want an outdated 0.4.0 against v1.0.0", result)
	}
	if result.URL == "" || result.PublishedAt.IsZero() {
		t.Errorf("result = %+v, want the release URL and date", result)
	}
	if *hits != 1 {
		t.Fatalf("requests = %d, want 1", *hits)
	}

	// The second check answers from the cache: no request, same answer.
	cached, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check (cached): %v", err)
	}
	if *hits != 1 {
		t.Errorf("requests = %d, want the cache to serve the second check", *hits)
	}
	if !cached.FromCache || !cached.Outdated || !cached.CheckedAt.Equal(versionNow) {
		t.Errorf("cached result = %+v", cached)
	}

	// The cache file is 0600, like every other state file. Windows has no POSIX
	// permission bits (os.Stat there reports 0666), so the check is skipped
	// there, as it is for the other state files.
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cache)
		if err != nil {
			t.Fatalf("stat the cache: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("cache mode = %o, want 600", perm)
		}
	}

	// A day later the cached answer is stale and the check runs again.
	later := func() time.Time { return versionNow.Add(DefaultInterval + time.Minute) }
	c2 := newChecker(t, "0.4.0", srv.URL, cache, later)
	if _, err := c2.Check(context.Background()); err != nil {
		t.Fatalf("Check (stale): %v", err)
	}
	if *hits != 2 {
		t.Errorf("requests = %d, want a second request once the cache is stale", *hits)
	}
}

func TestCheckUpToDate(t *testing.T) {
	srv, _ := releaseServer(t, http.StatusOK, `{"tag_name":"v0.4.0","html_url":"https://example.test/v0.4.0"}`)
	c := newChecker(t, "0.4.0", srv.URL, "", func() time.Time { return versionNow })
	result, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Outdated {
		t.Errorf("result = %+v, want no update for the same version", result)
	}
}

func TestCheckWithNoReleaseYet(t *testing.T) {
	// A 404 is GitHub saying "no published release", which is not an error.
	srv, _ := releaseServer(t, http.StatusNotFound, `{"message":"Not Found"}`)
	c := newChecker(t, "0.1.0", srv.URL, "", func() time.Time { return versionNow })
	result, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Latest != "" || result.Outdated {
		t.Errorf("result = %+v, want an empty answer", result)
	}
}

func TestCheckIgnoresDrafts(t *testing.T) {
	srv, _ := releaseServer(t, http.StatusOK, `{"tag_name":"v9.9.9","draft":true}`)
	c := newChecker(t, "0.1.0", srv.URL, "", func() time.Time { return versionNow })
	result, err := c.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Latest != "" || result.Outdated {
		t.Errorf("result = %+v, want a draft to be ignored", result)
	}
}

func TestCheckErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, `{}`},
		{"rate limited", http.StatusForbidden, `{"message":"API rate limit exceeded"}`},
		{"malformed json", http.StatusOK, `{not json`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := releaseServer(t, tc.status, tc.body)
			c := newChecker(t, "0.1.0", srv.URL, "", func() time.Time { return versionNow })
			if _, err := c.Check(context.Background()); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestCheckReportsAnUnreachableHost(t *testing.T) {
	c := newChecker(t, "0.1.0", "http://127.0.0.1:1", "", func() time.Time { return versionNow })
	if _, err := c.Check(context.Background()); err == nil {
		t.Fatal("want an error for an unreachable host")
	}
}

func TestUnversionedBuildsCompareNothing(t *testing.T) {
	srv, _ := releaseServer(t, http.StatusOK, `{"tag_name":"v9.9.9"}`)
	for _, current := range []string{"", "dev", "(devel)"} {
		t.Run("current="+current, func(t *testing.T) {
			c := newChecker(t, current, srv.URL, "", func() time.Time { return versionNow })
			if !c.Unversioned() {
				t.Fatalf("Unversioned() = false for %q", current)
			}
			result, err := c.Check(context.Background())
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if result.Outdated {
				t.Errorf("result = %+v, want no comparison for an unversioned build", result)
			}
			if result.Latest != "v9.9.9" {
				t.Errorf("result = %+v, want the latest release reported anyway", result)
			}
		})
	}
}

func TestCorruptCacheIsIgnored(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "update.json")
	if err := os.WriteFile(cache, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, hits := releaseServer(t, http.StatusOK, `{"tag_name":"v1.0.0"}`)
	c := newChecker(t, "0.1.0", srv.URL, cache, func() time.Time { return versionNow })
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if *hits != 1 {
		t.Errorf("requests = %d, want the corrupt cache to be ignored", *hits)
	}
}

func TestCheckNowIgnoresAFreshCache(t *testing.T) {
	srv, hits := releaseServer(t, http.StatusOK, `{"tag_name":"v1.0.0"}`)
	cache := filepath.Join(t.TempDir(), "update.json")
	c := newChecker(t, "0.1.0", srv.URL, cache, func() time.Time { return versionNow })
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if _, err := c.CheckNow(context.Background()); err != nil {
		t.Fatalf("CheckNow: %v", err)
	}
	if *hits != 2 {
		t.Errorf("requests = %d, want CheckNow to ask again", *hits)
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"v1.0.0", "1.0.0", 0},
		{"1.0", "1.0.0", 0},
		{"1.0.1", "1.0.0", 1},
		{"1.1.0", "1.0.9", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.0.0", "1.0.1", -1},
		{"0.4.0", "1.0.0", -1},
		// A prerelease sorts below the same version without one.
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0-rc.1", 1},
		{"1.0.0-rc.1", "1.0.0-rc.2", -1},
		{"1.0.0-rc.10", "1.0.0-rc.9", 1},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		// Numeric identifiers sort below alphanumeric ones.
		{"1.0.0-1", "1.0.0-alpha", -1},
		// Build metadata is ignored.
		{"1.0.0+build.5", "1.0.0", 0},
		// A git-describe style version compares as a prerelease of its tag.
		{"v0.4.0-2-gabc1234", "0.4.0", -1},
	}
	for _, tc := range tests {
		t.Run(tc.a+" vs "+tc.b, func(t *testing.T) {
			got, err := Compare(tc.a, tc.b)
			if err != nil {
				t.Fatalf("Compare(%q, %q): %v", tc.a, tc.b, err)
			}
			if got != tc.want {
				t.Errorf("Compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestCompareRejectsNonsense(t *testing.T) {
	for _, bad := range []string{"", "v", "one.two.three", "1.0.0.0", "1..0", "1.x"} {
		if _, err := Compare(bad, "1.0.0"); !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("Compare(%q, …) error = %v, want ErrInvalidVersion", bad, err)
		}
	}
}
