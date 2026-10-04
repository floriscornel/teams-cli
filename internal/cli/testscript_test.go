// Package cli_test holds the Layer 5 end-to-end tests: real CLI invocations
// driven by testdata/script/*.txtar against in-process fakes.
//
// The command under test is the real `teams` binary's code path: testscript
// re-executes this test binary with argv[0] == "teams", and runTeams then calls
// cli.MainWith on the real App. The fakes are injected through the App's Hooks,
// never through a flag or an environment variable that a release build would
// look at (PLAN.md: "No test override in shipped binaries").
package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/floriscornel/teams-cli/internal/cli"
)

// Environment variables the parent process uses to hand the fakes to the child.
const (
	envGraphURL  = "TEAMS_TEST_GRAPH_URL"
	envAuthority = "TEAMS_TEST_AUTHORITY"
)

func TestMain(m *testing.M) {
	// Keep the CLI tests (and the testscript subprocesses) away from the real OS
	// keychain: the envelope store's keychain item name is fixed, so a test that
	// reached it could read — or delete — a live data key.
	if os.Getenv("TEAMS_TEST_ALLOW_KEYCHAIN") == "" {
		_ = os.Setenv("TEAMS_NO_KEYCHAIN", "1")
	}
	// testscript.Main exits the process itself.
	testscript.Main(m, map[string]func(){
		"teams": runTeams,
	})
}

// runTeams is the in-process `teams` command the scripts invoke.
func runTeams() {
	app := cli.New(os.Stdin, os.Stdout, os.Stderr)
	app.SetHooks(cli.Hooks{
		GraphBaseURL:             os.Getenv(envGraphURL),
		DisableInstanceDiscovery: true,
		// Environ stays nil on purpose: the auth and config layers then read the
		// real environment the script set up, which is what a user's shell does.
	})
	// testscript's command functions are expected to exit the process; this is
	// the one place outside cmd/ that may call os.Exit.
	os.Exit(cli.MainWith(app, os.Args[1:])) //nolint:forbidigo // testscript command entry point
}

func TestScripts(t *testing.T) {
	graph := newFakeGraph(t)
	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "script"),
		Setup: func(e *testscript.Env) error {
			// testscript defaults HOME to /no-home, which makes the macOS
			// keychain lookup slow and unlike a real machine; give the script a
			// writable home instead.
			home := filepath.Join(e.WorkDir, "home")
			if err := os.MkdirAll(home, 0o700); err != nil {
				return err
			}
			e.Setenv("HOME", home)
			e.Setenv("TEAMS_CONFIG", filepath.Join(e.WorkDir, "config.toml"))
			e.Setenv("TEAMS_STATE_DIR", filepath.Join(e.WorkDir, "state"))
			e.Setenv("TEAMS_CACHE_DIR", filepath.Join(e.WorkDir, "cache"))
			e.Setenv("TEAMS_TEST_GRAPH_URL", graph.URL+"/v1.0")
			e.Setenv("TEAMS_TEST_AUTHORITY", graph.URL+"/tenant")
			// Deterministic, unstyled output.
			e.Setenv("NO_COLOR", "1")
			e.Setenv("TERM", "dumb")
			// Non-interactive: PLAN.md requires prompts to be refused, and CI
			// is how the CLI detects that on its own.
			e.Setenv("CI", "true")
			// Scripts must never touch the OS keychain: on macOS a lookup under a
			// temporary HOME pops a blocking "Keychain Not Found" dialog, and CI
			// runners have no keychain at all.
			e.Setenv("TEAMS_NO_KEYCHAIN", "1")
			e.Setenv("TEAMS_NO_UPDATE_CHECK", "1")
			return nil
		},
		TestWork: os.Getenv("TEAMS_TESTWORK") == "1",
	})
}

// fakeGraph is the smallest Graph the Phase 2 commands need: /me for whoami and
// doctor. Layer 2's internal/testing/fakegraph replaces it as the surface grows,
// and the contract validator will check these routes against the vendored OpenAPI
// subset.
type fakeServer struct {
	*httptest.Server
	requests []string
}

func newFakeGraph(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "/me"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"user-1","displayName":"Alice Example","userPrincipalName":"alice@example.com","mail":"alice@example.com"}`))
		case strings.HasSuffix(r.URL.Path, "/me/chats"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"value":[]}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"code": "NotFound", "message": "no route in the fake for " + r.URL.Path},
			})
		}
	}))
	t.Cleanup(f.Close)
	return f
}
