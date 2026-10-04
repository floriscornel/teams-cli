package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/output"
)

// The update check is opt-out, terminal-only for the automatic form, and never
// fails the command that triggered it. These tests pin each of those, against a
// fake GitHub releases endpoint reached through the App's in-process hooks.

// releaseServer serves the GitHub releases answer the update check reads.
func releaseServer(t *testing.T, tag string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/repos/floriscornel/teams-cli/releases/latest" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		body, _ := json.Marshal(map[string]any{
			"tag_name":     tag,
			"html_url":     "https://example.test/" + tag,
			"published_at": "2026-10-01T00:00:00Z",
		})
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// versionForTest sets the build version for one test.
func versionForTest(t *testing.T, version string) {
	t.Helper()
	old := Version
	Version = version
	Commit, Date = "abc1234", "2026-10-04T12:00:00Z"
	t.Cleanup(func() { Version, Commit, Date = old, "none", "unknown" })
}

func updateHarness(t *testing.T, srv *httptest.Server) *harness {
	t.Helper()
	h := newHarness(t)
	h.app.Hooks.UpdateBaseURL = srv.URL
	return h
}

func TestVersionCheckReportsANewerRelease(t *testing.T) {
	versionForTest(t, "0.4.0")
	srv := releaseServer(t, "v1.0.0")
	h := updateHarness(t, srv)

	out := h.mustRun("version", "--check")
	if !strings.Contains(out, "v1.0.0 is available") || !strings.Contains(out, "this is 0.4.0") {
		t.Errorf("stdout = %q, want the release and the current version", out)
	}
	if !strings.Contains(out, "go install github.com/floriscornel/teams-cli/cmd/teams@v1.0.0") {
		t.Errorf("stdout = %q, want the upgrade command", out)
	}
	// The answer is cached, 0600, in the profile's state dir.
	paths, err := h.app.Paths()
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	info, err := os.Stat(paths.UpdateFile())
	if err != nil {
		t.Fatalf("the check did not write its cache: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("cache mode = %o, want 600", perm)
	}
}

func TestVersionCheckJSON(t *testing.T) {
	versionForTest(t, "0.4.0")
	h := updateHarness(t, releaseServer(t, "v1.0.0"))

	out := h.mustRun("version", "--check", "--json")
	var view updateView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		t.Fatalf("--json output is not JSON: %v (%q)", err, out)
	}
	if !view.Outdated || view.Latest != "v1.0.0" || view.Version != "0.4.0" || view.CheckedAt == "" {
		t.Errorf("json = %+v", view)
	}
}

func TestVersionCheckUpToDate(t *testing.T) {
	versionForTest(t, "1.0.0")
	h := updateHarness(t, releaseServer(t, "v1.0.0"))

	out := h.mustRun("version", "--check")
	if !strings.Contains(out, "is up to date") {
		t.Errorf("stdout = %q, want an up-to-date answer", out)
	}
}

func TestVersionCheckWithoutAVersion(t *testing.T) {
	versionForTest(t, "dev")
	h := updateHarness(t, releaseServer(t, "v1.0.0"))

	out := h.mustRun("version", "--check")
	if !strings.Contains(out, "reports no version") {
		t.Errorf("stdout = %q, want the unversioned answer", out)
	}
}

func TestVersionCheckWhenDisabled(t *testing.T) {
	versionForTest(t, "0.4.0")
	srv := releaseServer(t, "v1.0.0")
	h := updateHarness(t, srv)
	h.env = []string{"TEAMS_NO_UPDATE_CHECK=1"}
	h.applyHooks()
	h.app.Hooks.UpdateBaseURL = srv.URL

	err := h.run("version", "--check")
	h.wantCode(err, output.CodeUsage)
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error = %v, want the disabled explanation", err)
	}
	if hint := output.HintOf(err); !strings.Contains(hint, "update_check") {
		t.Errorf("hint = %q, want the way to re-enable it", hint)
	}
}

func TestVersionCheckPropagatesAFailure(t *testing.T) {
	versionForTest(t, "0.4.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	h := updateHarness(t, srv)

	err := h.run("version", "--check")
	h.wantCode(err, output.CodeError)
	if err == nil || !strings.Contains(err.Error(), "GitHub answered") {
		t.Fatalf("error = %v, want the check's failure", err)
	}
}

func TestNoticeNewerVersionIsTerminalOnly(t *testing.T) {
	versionForTest(t, "0.4.0")
	srv := releaseServer(t, "v1.0.0")
	h := updateHarness(t, srv)

	// Not a terminal: nothing is printed, and no request is made.
	h.app.Printer = output.New(output.Options{Out: &h.stdout, Err: &h.stderr})
	h.app.noticeNewerVersion(t.Context())
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want silence off a terminal", h.stderr.String())
	}
	paths, err := h.app.Paths()
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	if _, err := os.Stat(paths.UpdateFile()); err == nil {
		t.Error("the check ran and wrote its cache even though the session is not interactive")
	}

	// A terminal: one line on stderr, and only when a newer release exists.
	h.app.Printer = output.New(output.Options{Out: &h.stdout, Err: &h.stderr, Interactive: true})
	h.app.noticeNewerVersion(t.Context())
	msg := h.stderr.String()
	if !strings.Contains(msg, "v1.0.0") || !strings.Contains(msg, "teams version --check") {
		t.Errorf("stderr = %q, want the update notice", msg)
	}

	h.stderr.Reset()
	h.app.noticeNewerVersion(t.Context())
	if !strings.Contains(h.stderr.String(), "v1.0.0") {
		t.Errorf("a cached answer was not reused: %q", h.stderr.String())
	}
}

func TestNoticeNewerVersionHonoursTheOffSwitch(t *testing.T) {
	versionForTest(t, "0.4.0")
	srv := releaseServer(t, "v1.0.0")
	h := updateHarness(t, srv)
	h.env = []string{"TEAMS_NO_UPDATE_CHECK=1"}
	h.applyHooks()
	h.app.Hooks.UpdateBaseURL = srv.URL
	h.app.Printer = output.New(output.Options{Out: &h.stdout, Err: &h.stderr, Interactive: true})

	h.app.noticeNewerVersion(t.Context())
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing when the check is disabled", h.stderr.String())
	}
}

func TestNoticeNewerVersionStaysQuietWhenUpToDate(t *testing.T) {
	versionForTest(t, "1.0.0")
	srv := releaseServer(t, "v1.0.0")
	h := updateHarness(t, srv)
	h.app.Printer = output.New(output.Options{Out: &h.stdout, Err: &h.stderr, Interactive: true})

	h.app.noticeNewerVersion(t.Context())
	if h.stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing when up to date", h.stderr.String())
	}
}
