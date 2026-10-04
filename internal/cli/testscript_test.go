// Package cli_test holds the Layer 5 end-to-end tests: real CLI invocations
// driven by testdata/script/*.txtar against in-process fakes.
//
// The command under test is the real teams binary's code path: testscript
// re-executes this test binary with argv[0] == "teams", and runTeams then calls
// cli.MainWith on the real App. The fakes are injected through the App's Hooks,
// never through a flag or an environment variable that a release build would
// look at (PLAN.md: "No test override in shipped binaries").
//
// The Graph fake is the Layer 2 stateful server (internal/testing/fakegraph), so
// the scripts exercise the real request shapes against a server that implements
// the documented query limits, and every request and response is validated
// against the vendored OpenAPI subset (PLAN.md Layer 6) through the contract
// hook.
package cli_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/floriscornel/teams-cli/internal/cli"
	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// Environment variables the parent process uses to hand the fakes to the child.
const (
	envGraphURL  = "TEAMS_TEST_GRAPH_URL"
	envAuthority = "TEAMS_TEST_AUTHORITY"
)

func TestMain(m *testing.M) {
	// Keep the CLI tests (and the testscript subprocesses) away from the real OS
	// keychain: the envelope store's keychain item name is fixed, so a test that
	// reached it could read - or delete - a live data key.
	if os.Getenv("TEAMS_TEST_ALLOW_KEYCHAIN") == "" {
		_ = os.Setenv("TEAMS_NO_KEYCHAIN", "1")
	}
	// testscript.Main exits the process itself.
	testscript.Main(m, map[string]func(){
		"teams": runTeams,
	})
}

// runTeams is the in-process teams command the scripts invoke.
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
	// Every script gets its own fake Graph, so the write scripts can post, edit
	// and delete in the seeded data without changing what another script sees.
	// (They run in parallel, and a shared server would make the assertions depend
	// on the order.)
	servers := &scriptServers{}
	t.Cleanup(servers.closeAll)
	clock := func() time.Time { return time.Now().UTC().Truncate(time.Second) }

	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "script"),
		Setup: func(e *testscript.Env) error {
			graph := fakegraph.NewServer(fakegraph.Options{
				Model: scriptsModel(clock()),
				// Layer 6: every request and response these scripts produce is
				// checked against the trimmed Graph OpenAPI description.
				Contract: fakegraph.WithContract(t),
			})
			servers.add(graph)
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
			e.Setenv("TEAMS_TEST_GRAPH_URL", graph.URL())
			e.Setenv("TEAMS_TEST_AUTHORITY", graph.URL()+"/tenant")
			// The write scripts need a token that carries the Phase 4 scopes;
			// the read scripts keep their own inline token.
			e.Setenv("TEAMS_TEST_WRITE_TOKEN", scriptsWriteToken())
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

// scriptServers owns the per-script fake Graph servers so they can be closed
// when the test ends.
type scriptServers struct {
	mu      sync.Mutex
	servers []*fakegraph.Server
}

func (s *scriptServers) add(server *fakegraph.Server) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.servers = append(s.servers, server)
}

func (s *scriptServers) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, server := range s.servers {
		server.Close()
	}
}

// tokenPrefix is the header of the unsigned test tokens the scripts use; the
// fake reads the scp claim out of the payload, exactly as it reads a real one.
//
// scriptsModel is the seed every script runs against. Times are relative to the
// moment the test starts, so a --since duration in a script keeps its meaning
// whatever the wall clock says.
func scriptsModel(now time.Time) fakegraph.Model {
	generalID := "19:general@thread.tacv2"
	releasesID := "19:releases@thread.tacv2"
	return fakegraph.Model{
		Me: "user-1",
		Users: []fakegraph.User{
			{ID: "user-1", DisplayName: "Alice Example", UserPrincipalName: "alice@example.com", Mail: "alice@example.com"},
			{ID: "user-2", DisplayName: "Bob Builder", UserPrincipalName: "bob@example.com", Mail: "bob@example.com", JobTitle: "Engineer", Relevance: 0.9},
			{ID: "user-3", DisplayName: "Yuki Tanaka", UserPrincipalName: "yuki@example.com", Mail: "yuki@example.com", JobTitle: "Manager", Relevance: 0.4},
			// A fourth user nobody is a member of yet, so `chat add-member` has
			// someone to add.
			{ID: "user-4", DisplayName: "Carol Chen", UserPrincipalName: "carol@example.com", Mail: "carol@example.com", JobTitle: "Designer", Relevance: 0.3},
		},
		Teams: []fakegraph.Team{{
			ID:          "team-eng",
			DisplayName: "Engineering",
			Description: "Where the work happens",
			Channels: []fakegraph.Channel{
				{
					ID: generalID, DisplayName: "General",
					DriveID: "drive-eng", FilesFolderID: "folder-general",
					Messages: []fakegraph.Message{
						{
							ID: "m-1001", AuthorID: "user-2", Created: now.Add(-3 * time.Hour),
							Body: "<p>Morning all, the deploy notes are attached.</p>",
							Attachments: []fakegraph.Attachment{{
								ID: "att-1", Name: "deploy-notes.md", ContentType: "text/markdown",
								ContentURL: "https://example.sharepoint.com/deploy-notes.md",
							}},
						},
						{
							ID: "m-1002", AuthorID: "user-1", Created: now.Add(-2 * time.Hour),
							Subject: "Deploy",
							Body: "<p>Hello <at id=\"0\">Yuki Tanaka</at>, can you review the deploy?</p>" +
								"<p><img src=\"../hostedContents/1/$value\" alt=\"chart\"></p>",
							Mentions: []fakegraph.Mention{{
								ID: 0, Text: "Yuki Tanaka", UserID: "user-3",
								UserDisplayName: "Yuki Tanaka", UserIdentityType: "aadUser",
							}},
							Reactions: []fakegraph.Reaction{
								{Type: "👍", UserID: "user-3", Created: now.Add(-time.Hour)},
								{Type: "👍", UserID: "user-2", Created: now.Add(-time.Hour)},
							},
							HostedContents: []fakegraph.HostedContent{{
								ID: "1", ContentType: "image/png", Content: []byte("fake-png-bytes"),
							}},
							Replies: []fakegraph.Message{{
								ID: "m-1003", AuthorID: "user-3", Created: now.Add(-90 * time.Minute),
								Body: "<p>On it.</p>",
							}},
						},
					},
				},
				{
					ID: releasesID, DisplayName: "Releases",
					Messages: []fakegraph.Message{
						{
							ID: "m-2001", AuthorID: "user-2", Created: now.Add(-30 * time.Minute),
							Body: "<p>v0.2 is out.</p>",
						},
						{
							ID: "m-2002", AuthorID: "user-2", Created: now.Add(-20 * time.Minute),
							Body: "<p>cc <at id=\"0\">Alice Example</at> for the release notes</p>",
							Mentions: []fakegraph.Mention{{
								ID: 0, Text: "Alice Example", UserID: "user-1",
								UserDisplayName: "Alice Example", UserIdentityType: "aadUser",
							}},
						},
					},
				},
			},
		}},
		Chats: []fakegraph.Chat{
			{
				ID: "19:bob@thread.v2", ChatType: fakegraph.ChatTypeOneOnOne,
				Members: []fakegraph.Member{{UserID: "user-1"}, {UserID: "user-2"}},
				// Read: the newest message (-3h) is older than the watermark.
				LastRead: now.Add(-time.Hour),
				Messages: []fakegraph.Message{
					{ID: "c-1", AuthorID: "user-2", Created: now.Add(-4 * time.Hour), Body: "<p>lunch?</p>"},
					{ID: "c-2", AuthorID: "user-1", Created: now.Add(-3 * time.Hour), Body: "<p>sure, at 12</p>"},
				},
			},
			{
				// Unread but stale: a meeting chat whose read state Teams never sets.
				// It is what an unbounded inbox fills up with, so the window is asserted
				// against it.
				ID: "19:old-meeting@thread.v2", ChatType: fakegraph.ChatTypeMeeting, Topic: "Old meeting",
				Members: []fakegraph.Member{{UserID: "user-1"}, {UserID: "user-2"}},
				// No read watermark at all, which is what makes a meeting chat count as
				// unread forever.
				Messages: []fakegraph.Message{
					{ID: "c-old", AuthorID: "user-2", Created: now.AddDate(0, 0, -400), Body: "<p>ancient</p>"},
				},
			},
			{
				ID: "19:release-train@thread.v2", ChatType: fakegraph.ChatTypeGroup, Topic: "Release train",
				Members: []fakegraph.Member{{UserID: "user-1"}, {UserID: "user-2"}, {UserID: "user-3"}},
				// Unread: the newest message (-45m) is newer than the watermark.
				LastRead: now.Add(-60 * time.Minute),
				Messages: []fakegraph.Message{
					{ID: "c-3", AuthorID: "user-3", Created: now.Add(-45 * time.Minute), Body: "<p>ship it</p>"},
				},
			},
		},
		Drives: []fakegraph.Drive{{
			ID: "drive-eng",
			Items: []fakegraph.DriveItem{
				{ID: "item-1", Name: "deploy-notes.md", ParentID: "folder-general", Content: []byte("# deploy\n"), ContentType: "text/markdown", Created: now.Add(-4 * time.Hour)},
				{ID: "item-2", Name: "archive", ParentID: "folder-general", Folder: true, Created: now.Add(-40 * time.Hour)},
			},
		}},
	}
}

// scriptsToken is the scope set the scripts' tokens carry: every read scope
// Phase 3 needs, including the admin-consent ones the fake grants by default.
const scriptsToken = "User.Read User.ReadBasic.All Team.ReadBasic.All TeamMember.Read.All Channel.ReadBasic.All ChannelMessage.Read.All Chat.Read Chat.ReadBasic Files.Read.All People.Read"

// scriptsWriteScopes is what the Phase 4 scripts need on top of the read set:
// the send/read-write scopes per container, the file scopes for an upload, and
// Chat.ManageDeletion.All, which `chat delete` requests incrementally and the
// fake grants because its admin-consent default is permissive
// (internal/testing/fakegraph, Options.AdminConsentedScopes).
const scriptsWriteScopes = scriptsToken + " ChannelMessage.Send ChannelMessage.ReadWrite Chat.ReadWrite ChatMessage.Send Chat.ManageDeletion.All Files.ReadWrite Files.ReadWrite.All"

// scriptsWriteToken mints the token the Phase 4 scripts use. It carries the
// documented claims the CLI's decode-only check validates (aud, exp) and the
// write scopes in scp, exactly like the inline tokens the read scripts carry.
func scriptsWriteToken() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"aud":                "00000003-0000-0000-c000-000000000000",
		"tid":                "11111111-2222-3333-4444-555555555555",
		"preferred_username": "alice@example.com",
		"scp":                scriptsWriteScopes,
		"exp":                1893456000,
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(claims) + "."
}

// ensureUTF8 is a compile-time guard that the seed's HTML carries no invalid
// runes, which would make the scripted output depend on the locale.
var _ = func() bool { return utf8.ValidString(scriptsToken) }
