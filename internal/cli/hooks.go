package cli

import (
	"net/http"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// Hooks are the in-process seams the Layer 5 tests use to point the real CLI at
// fakegraph and fakeidp.
//
// PLAN.md requires that released binaries expose no test-only URL override: there
// is deliberately no flag and no environment variable for these fields, so a
// user cannot set them. The test harness sets them on the App it constructs, and
// the release smoke test asserts the shipped binary has no such flag.
type Hooks struct {
	// ConfigPath overrides the config file (tests otherwise use TEAMS_CONFIG).
	ConfigPath string
	// GraphBaseURL points the Graph client at fakegraph.
	GraphBaseURL string
	// GraphHTTPClient is the HTTP client for Graph calls.
	GraphHTTPClient *http.Client
	// AuthHTTPClient is MSAL's HTTP client (fakeidp's TLS client).
	AuthHTTPClient *http.Client
	// AuthAuthority overrides the MSAL authority URL (fakeidp's authority).
	AuthAuthority string
	// AuthOpenURL replaces the browser opener (fakeidp's in-process browser).
	AuthOpenURL func(url string) error
	// DisableInstanceDiscovery is required by fakeidp: its authority host is not
	// a trusted Microsoft host, so MSAL would otherwise call instance discovery.
	DisableInstanceDiscovery bool
	// Environ replaces os.Environ() for the auth and config layers.
	Environ []string
	// Sleeper replaces the retry delay so throttling tests stay fast.
	Sleeper graph.Sleeper
	// Recorder observes Graph traffic for the Layer 6 contract validation.
	Recorder graph.Recorder
	// GrantedScopes replaces the scopes read from the token, so tests can
	// exercise the scope gate without a full MSAL login. nil means "read the
	// token".
	GrantedScopes []string
	// UpdateBaseURL points the update check at a fake GitHub API. Empty means
	// the real api.github.com; like the other URLs here there is no flag or
	// environment variable for it, so a released binary cannot be redirected.
	UpdateBaseURL string
	// UpdateHTTPClient is the HTTP client for the update check.
	UpdateHTTPClient *http.Client
}

// SetHooks installs the test seams on this App.
func (a *App) SetHooks(h Hooks) { a.Hooks = h }
