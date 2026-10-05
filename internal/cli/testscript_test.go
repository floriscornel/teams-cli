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

	// The scripts pin TZ, and the child process has to resolve that name: the
	// zone database travels with the built binary (cmd/teams/main.go) but not with
	// a test binary, so Windows would otherwise fall back to UTC and every
	// rendered time in a calendar script would be wrong.
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	_ "time/tzdata"
	"unicode/utf8"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/floriscornel/teams-cli/internal/cli"
	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// Environment variables the parent process uses to hand the fakes to the child.
const (
	envGraphURL  = "TEAMS_TEST_GRAPH_URL"
	envAuthority = "TEAMS_TEST_AUTHORITY"
	// envUpdateURL points the update check at the fake GitHub Releases API. Like
	// the others it is read by the test harness, never by the CLI itself.
	envUpdateURL = "TEAMS_TEST_UPDATE_URL"
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
		UpdateBaseURL:            os.Getenv(envUpdateURL),
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
	// One fake GitHub Releases endpoint for every script: the update check is
	// the only outbound call the CLI makes that is not Graph, so no script may
	// reach the real api.github.com.
	releases := newReleasesServer()
	t.Cleanup(releases.Close)

	testscript.Run(t, testscript.Params{
		Dir: filepath.Join("testdata", "script"),
		Setup: func(e *testscript.Env) error {
			graph := fakegraph.NewServer(fakegraph.Options{
				Model: scriptsModel(clock()),
				// Layer 6: every request and response these scripts produce is
				// checked against the trimmed Graph OpenAPI description.
				Contract: fakegraph.WithContract(t),
				// The scripts set TEAMS_ACCESS_TOKEN, whose scp claim is what a
				// user's real token carries; honouring only that is what makes
				// calendar_scope.txtar exercise a genuine missing-scope refusal
				// rather than the fake's permissive default grant.
				ScopesFromTokenOnly: true,
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
			e.Setenv(envUpdateURL, releases.URL)
			// The write scripts need a token that carries the Phase 4 scopes;
			// the read scripts keep their own inline token.
			e.Setenv("TEAMS_TEST_WRITE_TOKEN", scriptsWriteToken())
			// The calendar scripts need the Phase 6 read scopes, which no
			// preset carries.
			e.Setenv("TEAMS_TEST_CALENDAR_TOKEN", scriptsCalendarToken())
			e.Setenv("TEAMS_TEST_CALENDAR_NOCHAT_TOKEN", scriptsTokenWith(scriptsCalendarNoChatScopes))
			e.Setenv("TEAMS_TEST_READ_TOKEN", scriptsTokenWith(scriptsToken))
			e.Setenv("TEAMS_TEST_CALENDAR_ONLY_TOKEN", scriptsTokenWith(scriptsCalendarOnlyScopes))
			e.Setenv("TEAMS_TEST_CALENDAR_WRITE_TOKEN", scriptsTokenWith(scriptsCalendarWriteScopes))
			e.Setenv("TEAMS_TEST_CALENDAR_NOCHAT_TOKEN", scriptsTokenWith(scriptsCalendarNoChatScopes))
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

// newReleasesServer serves the GitHub releases answer the update check reads.
// The tag is deliberately far ahead of anything this repository will build, so
// a script that clears TEAMS_NO_UPDATE_CHECK always sees "a newer release".
func newReleasesServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/repos/floriscornel/teams-cli/releases/latest" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9","html_url":"https://example.test/v9.9.9","published_at":"2026-10-01T00:00:00Z"}`))
	}))
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
			// A fifth user with no Exchange Online mailbox, so the calendar
			// scripts can exercise the 404 MailboxNotEnabledForRESTAPI path.
			{ID: "user-5", DisplayName: "Dave Kim", UserPrincipalName: "dave@example.com", Mail: "dave@example.com", NoMailbox: true},
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
		CalendarEvents: calendarScriptEvents(now),
		// The calendar scripts list another user with each access level: Bob's
		// calendar is shared in full, Yuki's as free/busy only, and Carol's is
		// not shared at all (403 ErrorAccessDenied). Dave has no mailbox.
		CalendarAccess: map[string]string{
			"user-2": fakegraph.CalendarAccessRead,
			"user-3": fakegraph.CalendarAccessFreeBusy,
		},
	}
}

// calendarScriptEvents seeds the calendar events the scripts list. Every instant
// is a fixed Tokyo wall clock time on the Tokyo day that is current when the
// test starts, and the scripts pin TZ=Asia/Tokyo, so the rendered times are the
// same whatever the wall clock says (scriptsModel takes a relative `now` for the
// same reason).
func calendarScriptEvents(now time.Time) []fakegraph.CalendarEvent {
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		// Without the zone database the scripts cannot assert on times; the
		// scripts' TZ would not resolve either, so this is not silent.
		panic("Asia/Tokyo is unavailable: " + err.Error())
	}
	today := now.In(tokyo)
	at := func(hour, minute int) time.Time {
		return time.Date(today.Year(), today.Month(), today.Day(), hour, minute, 0, 0, tokyo)
	}
	return []fakegraph.CalendarEvent{
		{
			ID: "cal-standup", Subject: "Standup", Start: at(9, 0), End: at(9, 30),
			ShowAs: "busy", Teams: true, IsOrganizer: true, Location: "Teams",
			JoinURL: "https://teams.microsoft.com/l/meetup-join/19%3ameeting_standup%40thread.v2/0?context=%7b%22Tid%22%3a%22t%22%7d",
		},
		{
			ID: "cal-review", Subject: "Design review", Start: at(14, 0), End: at(15, 0),
			ShowAs: "tentative", OrganizerName: "Bob Builder", Location: "Room 4",
			Response: "tentativelyAccepted",
		},
		{
			ID: "cal-holiday", Subject: "Company holiday", Kind: fakegraph.CalendarEventAllDay,
			Start: at(0, 0), Days: 1, ShowAs: "oof", IsOrganizer: true,
		},
		{
			ID: "cal-cancelled", Subject: "Cancelled sync", Start: at(16, 0), End: at(16, 30),
			IsCancelled: true, IsOrganizer: true,
		},
		{
			// Yesterday in Tokyo, so it must not appear in a listing of today
			// even though the server window is widened by a day.
			ID: "cal-yesterday", Subject: "Yesterday standup", Start: at(9, 0).AddDate(0, 0, -1), End: at(9, 30).AddDate(0, 0, -1),
			ShowAs: "busy", IsOrganizer: true,
		},
		{
			// Tomorrow in Tokyo, for the --date tomorrow assertion.
			ID: "cal-tomorrow", Subject: "Planning", Start: at(11, 0).AddDate(0, 0, 1), End: at(12, 0).AddDate(0, 0, 1),
			ShowAs: "busy", OrganizerName: "Yuki Tanaka", IsOrganizer: true,
		},
		{
			// Yesterday's UTC date, which is what an all-day event matched as
			// UTC midnight to midnight would drag into today's window
			// (plans/calendar.md §3, F5). It belongs to yesterday in Tokyo and
			// must be filtered out.
			ID: "cal-utc-yesterday", Subject: "UTC yesterday", Kind: fakegraph.CalendarEventAllDay,
			Start: at(0, 0).AddDate(0, 0, -1), Days: 1, ShowAs: "busy", IsOrganizer: true,
		},
		{
			// An event the signed-in user was invited to, so the response
			// pre-checks have something they are allowed to act on. It refuses
			// proposed times, which `--propose` has to notice.
			ID: "cal-invited", Subject: "Invited meeting", Start: at(15, 0), End: at(16, 0),
			ShowAs: "tentative", OrganizerName: "Bob Builder", IsOrganizer: false,
			AllowNewTimeProposals: boolPtr(false),
		},
		{
			ID: "cal-bob-1", OwnerID: "user-2", Subject: "Bob review", Start: at(10, 0), End: at(11, 0),
			OrganizerName: "Bob Builder", IsOrganizer: true,
		},
		{
			ID: "cal-yuki-busy", OwnerID: "user-3", Subject: "Yuki interview", Start: at(13, 0), End: at(14, 0),
			ShowAs: "busy", IsOrganizer: true,
		},
	}
}

// scriptsCalendarScopes is what the calendar scripts need: the read scope, the
// shared scope for another user's calendar, and OnlineMeetings.Read for
// `list --chat` and `show`.
// User.Read is in the set because every calendar command resolves the signed-in
// user first, to tell your own calendar from a colleague's.
const scriptsCalendarScopes = "User.Read Calendars.Read Calendars.Read.Shared Calendars.ReadWrite OnlineMeetings.Read"

// boolPtr is a small helper for the seed's optional booleans.
func boolPtr(v bool) *bool { return &v }

// scriptsCalendarNoChatScopes is the calendar read set WITHOUT OnlineMeetings.Read,
// so a script can exercise `show`'s degraded chat note.
const scriptsCalendarNoChatScopes = "User.Read Calendars.Read Calendars.Read.Shared Calendars.ReadWrite"

// scriptsCalendarWriteScopes is what the calendar write scripts need: the reads,
// the write scope, and the chat scope a --teams create wants.
const scriptsCalendarWriteScopes = "User.Read Calendars.Read Calendars.Read.Shared Calendars.ReadWrite OnlineMeetings.Read"

// scriptsCalendarOnlyScopes is the calendar read set a fully consented profile
// carries: the reads plus the chat scope.
const scriptsCalendarOnlyScopes = "User.Read Calendars.Read Calendars.Read.Shared Calendars.ReadWrite OnlineMeetings.Read"

// scriptsCalendarToken mints the token the calendar scripts use.
func scriptsCalendarToken() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"aud":                "00000003-0000-0000-c000-000000000000",
		"tid":                "11111111-2222-3333-4444-555555555555",
		"preferred_username": "alice@example.com",
		"scp":                scriptsCalendarScopes,
		"exp":                1893456000,
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(claims) + "."
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

// scriptsTokenWith mints a token carrying exactly the given scopes, for a script
// that needs to leave one out.
func scriptsTokenWith(scp string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	claims, _ := json.Marshal(map[string]any{
		"aud":                "00000003-0000-0000-c000-000000000000",
		"tid":                "11111111-2222-3333-4444-555555555555",
		"preferred_username": "alice@example.com",
		"scp":                scp,
		"exp":                1893456000,
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(claims) + "."
}

// ensureUTF8 is a compile-time guard that the seed's HTML carries no invalid
// runes, which would make the scripted output depend on the locale.
var _ = func() bool { return utf8.ValidString(scriptsToken) }
