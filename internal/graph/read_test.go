// Tests for the Phase 3 read wrappers: teams.go, chats.go, messages.go,
// search.go, users.go and files.go.
//
// Every call goes through the real client against the real fake Graph server
// (internal/testing/fakegraph), which serves the documented query limits with
// the live-observed corrections in docs/spike/phase1.md, and each JSON call is
// additionally validated against the vendored OpenAPI subset by installing
// fakegraph.WithContract (PLAN.md "Contract tests against Microsoft's OpenAPI
// spec"). The only calls that run without the hook are the ones that fetch
// bytes: the contract validator injects Content-Type: application/json, so a
// binary stream cannot be checked against the spec
// (internal/testing/fakegraph/contract_test.go, binaryResponse).
package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// Timestamps. The fake's clock is frozen at readFrozen (objects created through
// the API are then newer than every seeded one) and every seeded timestamp is a
// constant, so a failing assertion can be read straight off the source.
var (
	readFrozen = time.Date(2026, 1, 6, 12, 0, 0, 0, time.UTC)
	readT1     = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	readT2     = time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	readT3     = time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)
	readT4     = time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)
	readT5     = time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
)

// Seed ids, kept as constants so a failure names the same value the fake was
// seeded with.
const (
	readMe    = "u-me"
	readAlice = "u-alice"
	readBob   = "u-bob"
	readCarol = "u-carol"
	readDev   = "u-dev"
	readFrank = "u-frank"
	readSofia = "u-sofia"

	readTeamID    = "t-eng"
	readChannelID = "c-general"
	readOrderCh   = "c-order"
	readWindowCh  = "c-window"
	readPrivateCh = "c-private"
	readReplyCh   = "c-reply-driven"

	readDriveID     = "drive-t-eng"
	readFilesFolder = "folder-c-general"

	readHostedMsg     = "cm-host"
	readHostedContent = "hx"

	readChatOneOnOne = "chat-1on1"
	readChatGroup    = "chat-group"
	readChatFrank    = "chat-frank"

	readHostedChatMsg     = "gm-hc"
	readHostedChatContent = "hc-1"
)

// readServer starts a fake Graph server seeded with m. The contract hook is on
// unless withoutContract is passed.
func readServer(t *testing.T, m fakegraph.Model, mutate ...func(*fakegraph.Options)) *fakegraph.Server {
	t.Helper()
	opts := fakegraph.Options{
		Model:    m,
		Clock:    clock.NewFake(readFrozen),
		Contract: fakegraph.WithContract(t),
	}
	for _, fn := range mutate {
		fn(&opts)
	}
	return fakegraph.New(t, opts)
}

// withoutContract turns the Layer 6 hook off for a server whose calls fetch
// bytes rather than JSON.
func withoutContract() func(*fakegraph.Options) {
	return func(o *fakegraph.Options) { o.Contract = nil }
}

// readClient builds the real internal/graph client against a fake server, with
// retries and sleeping off so no test waits.
func readClient(t *testing.T, srv *fakegraph.Server, mutate ...func(*Options)) *Client {
	t.Helper()
	opts := Options{
		BaseURL:    srv.URL(),
		HTTPClient: srv.Client(),
		Token:      TokenSourceFunc(func(context.Context) (string, error) { return "fakegraph-token", nil }),
		Sleeper:    func(context.Context, time.Duration) error { return nil },
		MaxRetries: -1,
	}
	for _, fn := range mutate {
		fn(&opts)
	}
	c, err := New(opts)
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}
	return c
}

// readSetup is the common pair: a server seeded with readModel and a client.
func readSetup(t *testing.T, mutate ...func(*fakegraph.Options)) (*fakegraph.Server, *Client) {
	t.Helper()
	srv := readServer(t, readModel(), mutate...)
	return srv, readClient(t, srv)
}

// readCalls returns the recorded requests for one method and Graph path, and
// fails when there are none.
func readCalls(t *testing.T, srv *fakegraph.Server, method, path string) []*fakegraph.RecordedRequest {
	t.Helper()
	calls := srv.RequestsFor(method, path)
	if len(calls) == 0 {
		t.Fatalf("no %s %s request was recorded; recorded: %s", method, path, readTraffic(srv))
	}
	return calls
}

// readTraffic renders what the fake saw, for a failure message.
func readTraffic(srv *fakegraph.Server) string {
	var parts []string
	for _, rec := range srv.Requests() {
		parts = append(parts, rec.Method+" "+rec.Path+"?"+rec.Query.Encode())
	}
	return strings.Join(parts, ", ")
}

// readIDs maps messages to their ids, which is what the order assertions use.
func readIDs(msgs []Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}

// readUsage checks that err is the CLI's usage error (exit code 2, AGENTS.md
// "Exit codes").
func readUsage(t *testing.T, err error) *UsageError {
	t.Helper()
	if err == nil {
		t.Fatal("want a usage error, got nil")
	}
	var ue *UsageError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v (%T), want a *UsageError", err, err)
	}
	if got := output.CodeOf(err); got != output.CodeUsage {
		t.Errorf("exit code = %d, want %d", got, output.CodeUsage)
	}
	return ue
}

// readWireRecorder records the request line as it goes on the wire, which
// includes the percent-encoding. fakegraph.RecordedRequest.Path is the decoded
// path, so it cannot show that a "#" became %23.
type readWireRecorder struct{ lines []string }

func (r *readWireRecorder) RecordRequest(req *http.Request, _ []byte) {
	r.lines = append(r.lines, req.Method+" "+req.URL.EscapedPath())
}

func (r *readWireRecorder) RecordResponse(*http.Request, int, []byte) {}

// readQuote is a double quote spelled without an escape sequence, so the
// fixtures that need one stay printable in a plain string literal.
var readQuote = string('"')

// readHasLine reports whether the recorder saw an exact request line.
func readHasLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}

// readEqual reports whether got and want are the same sequence.
func readEqual(got, want []string) bool { return slices.Equal(got, want) }

// readModel is the graph the read tests share: one team with a general channel,
// an ordering channel, a windowing channel and a private channel, three chats
// (a one-on-one, a group chat with a topic and one whose topic carries an
// apostrophe) and one drive holding the channel's files.
func readModel() fakegraph.Model {
	users := []fakegraph.User{
		{ID: readMe, DisplayName: "Me Myself", UserPrincipalName: "me@contoso.example", Mail: "me@contoso.example"},
		{ID: readAlice, DisplayName: "Alice Example", UserPrincipalName: "alice@contoso.example", Mail: "alice@contoso.example", JobTitle: "Engineer", Relevance: 1},
		{ID: readBob, DisplayName: "Bob Builder", UserPrincipalName: "bob@contoso.example", Mail: "bob@contoso.example", Relevance: 5},
		{ID: readCarol, DisplayName: "Carol Jones", UserPrincipalName: "carol@contoso.example", Mail: "carol@contoso.example"},
		// Dev's userPrincipalName — not his display name — starts with "zenith",
		// and only Sofia's mail starts with "smarino", so SearchUsers reaches its
		// second and third attempts for those queries (PLAN.md:167).
		{ID: readDev, DisplayName: "Dev Patel", UserPrincipalName: "zenith.dev@contoso.example", Mail: "dev.patel@contoso.example"},
		{ID: readFrank, DisplayName: "Frank O'Neil", UserPrincipalName: "frank@contoso.example", Mail: "frank@contoso.example"},
		// Sofia is the most relevant person, so /me/people puts her first even
		// though "Alice" sorts before "Sofia" in the alphabet
		// (refs/graph/api-reference/v1.0/api/user-list-people.md:15, PLAN.md:167).
		{ID: readSofia, DisplayName: "Sofia Marino", UserPrincipalName: "sofia.marino@contoso.example", Mail: "smarino@partner.example", Relevance: 9},
	}
	teamMembers := []fakegraph.Member{
		{UserID: readMe, Roles: []string{"owner"}},
		{UserID: readAlice},
		{UserID: readBob},
		{UserID: readCarol},
		{UserID: readDev},
		{UserID: readFrank},
		{UserID: readSofia},
	}
	return fakegraph.Model{
		Me:    readMe,
		Users: users,
		Teams: []fakegraph.Team{{
			ID:          readTeamID,
			DisplayName: "Engineering",
			Description: "Builds things",
			Created:     readT1,
			Members:     teamMembers,
			Channels: []fakegraph.Channel{
				{ID: readChannelID, DisplayName: "General", Created: readT1, Messages: readGeneralMessages()},
				{ID: readOrderCh, DisplayName: "Ordering", Created: readT1, Messages: readOrderMessages()},
				{ID: readWindowCh, DisplayName: "Window", Created: readT1, Messages: readWindowMessages()},
				{ID: readReplyCh, DisplayName: "Reply driven", Created: readT1, Messages: readReplyDrivenMessages()},
				{
					ID: readPrivateCh, DisplayName: "Private Ops", MembershipType: fakegraph.ChannelPrivate, Created: readT2,
					Members: []fakegraph.Member{{UserID: readMe, Roles: []string{"owner"}}, {UserID: readAlice}},
				},
			},
		}},
		Chats: []fakegraph.Chat{
			{
				ID: readChatOneOnOne, ChatType: fakegraph.ChatTypeOneOnOne, Created: readT1, Updated: readT4, LastRead: readT2,
				Members: []fakegraph.Member{{UserID: readMe, Roles: []string{"owner"}}, {UserID: readAlice}},
				// The seed order is deliberately not created-order, so a listing
				// that is not sorted client-side comes back in the fake's
				// documented "neither created- nor modified-sorted" order
				// (docs/spike/phase1.md:60).
				Messages: []fakegraph.Message{
					{ID: "gm-a", AuthorID: readMe, Body: "<p>gm a</p>", Created: readT2},
					{ID: "gm-b", AuthorID: readBob, Body: "<p>gm b hello</p>", Created: readT4},
					{ID: "gm-c", AuthorID: readMe, Body: "<p>gm c</p>", Created: readT3},
					// Edited long after it was written: a --since window on
					// lastModifiedDateTime returns it and the client drops it again
					// on createdDateTime (PLAN.md:183).
					{ID: "gm-edited-old", AuthorID: readMe, Body: "<p>gm edited</p>", Created: readT1, Modified: readT5},
					{
						ID: readHostedChatMsg, AuthorID: readMe, Created: readT2,
						Body:           "<p>inline</p><img src=\"../hostedContents/hc-1/$value\">",
						HostedContents: []fakegraph.HostedContent{{ID: readHostedChatContent, ContentType: "image/png", Content: []byte("png-bytes")}},
					},
				},
			},
			{
				ID: readChatGroup, ChatType: fakegraph.ChatTypeGroup, Topic: "Release train", Created: readT2, Updated: readT2,
				Members: []fakegraph.Member{{UserID: readMe, Roles: []string{"owner"}}, {UserID: readAlice}, {UserID: readBob}},
			},
			{
				ID: readChatFrank, ChatType: fakegraph.ChatTypeGroup, Topic: "Frank's train", Created: readT3, Updated: readT3,
				Members: []fakegraph.Member{{UserID: readMe, Roles: []string{"owner"}}, {UserID: readFrank}},
			},
		},
		Drives: []fakegraph.Drive{{
			ID: readDriveID,
			Items: []fakegraph.DriveItem{
				{
					ID: "file-1", Name: "spec.pdf", ParentID: readFilesFolder, Content: []byte("%PDF-1.7 fake"),
					ContentType: "application/pdf", Created: readT1, Modified: readT2,
				},
				{
					ID: "file-2", Name: "notes.txt", ParentID: readFilesFolder, Content: []byte("notes"),
					ContentType: "text/plain", Created: readT2, Modified: readT3,
				},
			},
		}},
	}
}

// readGeneralMessages is the channel used by the plain reads, the reply reads
// and search: cm-2's chain is the newest (its newest reply is readT5), then the
// two readT3 roots, then the older ones.
func readGeneralMessages() []fakegraph.Message {
	return []fakegraph.Message{
		{ID: "cm-1", AuthorID: readAlice, Body: "<p>hello world</p>", Created: readT2},
		{
			ID: "cm-2", AuthorID: readBob, Body: "<p>deploy is done</p>", Subject: "Deploy", Created: readT4,
			Replies: []fakegraph.Message{
				{ID: "cr-1", AuthorID: readAlice, Body: "<p>thanks!</p>", Created: readT2},
				{ID: "cr-3", AuthorID: readCarol, Body: "<p>nice</p>", Created: readT3},
				{ID: "cr-2", AuthorID: readMe, Body: "<p>shipped</p>", Created: readT5},
			},
		},
		{ID: "cm-sys", AuthorID: readMe, Body: "<p>member added</p>", Created: readT3, MessageType: "systemEventMessage"},
		{
			ID: readHostedMsg, AuthorID: readAlice, Created: readT3,
			Body:           "<p>inline image</p><img src=\"../hostedContents/hx/$value\">",
			HostedContents: []fakegraph.HostedContent{{ID: readHostedContent, ContentType: "image/png", Content: []byte("png-bytes")}},
		},
		{ID: "cm-old", AuthorID: readBob, Body: "<p>old news</p>", Created: readT1},
	}
}

// readOrderMessages is the channel that separates the client's ordering key
// from the server's: or-chain's reply was edited at readT5, well after the root
// was written, and the server's chain key only looks at the reply's
// createdDateTime.
func readOrderMessages() []fakegraph.Message {
	return []fakegraph.Message{
		{ID: "or-tie-a", AuthorID: readMe, Body: "<p>tie a</p>", Created: readT4},
		{ID: "or-old", AuthorID: readBob, Body: "<p>old</p>", Created: readT1},
		{ID: "or-tie-b", AuthorID: readAlice, Body: "<p>tie b</p>", Created: readT4},
		{
			ID: "or-chain", AuthorID: readCarol, Body: "<p>chain</p>", Created: readT2,
			Replies: []fakegraph.Message{
				{ID: "or-chain-r1", AuthorID: readMe, Body: "<p>edited reply</p>", Created: readT2, Modified: readT5},
			},
		},
	}
}

// readWindowMessages is the channel the --since stop is measured on: with a
// $top of 3 the first page ends on a chain that is older than the window, so
// the second page must never be requested.
func readWindowMessages() []fakegraph.Message {
	return []fakegraph.Message{
		{ID: "w-new", AuthorID: readAlice, Body: "<p>new</p>", Created: readT4},
		{ID: "w-mid", AuthorID: readBob, Body: "<p>mid</p>", Created: readT3},
		{ID: "w-old", AuthorID: readCarol, Body: "<p>old</p>", Created: readT1},
		{ID: "w-older", AuthorID: readMe, Body: "<p>older</p>", Created: readT1.Add(-time.Hour)},
	}
}

// pagingModel seeds nTeams joined teams, one team with nChannels channels and
// one team with nMembers members (every member needs its own directory user),
// so the shared pager has to follow @odata.nextLink.
// readReplyDrivenMessages is the channel whose newest root takes its place in
// the chain order from a reply rather than from its own timestamp: the service
// orders rd-reply-new first (its reply was created at readT5) although the root
// itself was written at readT2.
func readReplyDrivenMessages() []fakegraph.Message {
	return []fakegraph.Message{
		{ID: "rd-tie-a", AuthorID: readMe, Body: "<p>rd tie a</p>", Created: readT4},
		{ID: "rd-old", AuthorID: readBob, Body: "<p>rd old</p>", Created: readT1},
		{ID: "rd-tie-b", AuthorID: readAlice, Body: "<p>rd tie b</p>", Created: readT4},
		{
			ID: "rd-reply-new", AuthorID: readCarol, Body: "<p>rd reply driven</p>", Created: readT2,
			Replies: []fakegraph.Message{
				{ID: "rd-1", AuthorID: readMe, Body: "<p>rd reply</p>", Created: readT5},
			},
		},
	}
}

func pagingModel(nTeams, nChannels, nMembers int) fakegraph.Model {
	users := []fakegraph.User{{ID: readMe, DisplayName: "Me Myself"}}
	memberIDs := []string{readMe}
	for i := 0; i < nMembers-1; i++ {
		id := fmt.Sprintf("u-mem-%03d", i)
		users = append(users, fakegraph.User{ID: id, DisplayName: "Member " + id})
		memberIDs = append(memberIDs, id)
	}
	members := make([]fakegraph.Member, 0, len(memberIDs))
	for i, id := range memberIDs {
		if i == 0 {
			members = append(members, fakegraph.Member{UserID: id, Roles: []string{"owner"}})
			continue
		}
		members = append(members, fakegraph.Member{UserID: id})
	}
	teams := make([]fakegraph.Team, 0, nTeams+2)
	for i := 0; i < nTeams; i++ {
		teams = append(teams, fakegraph.Team{ID: fmt.Sprintf("t-pick-%04d", i), DisplayName: fmt.Sprintf("Team %04d", i)})
	}
	channels := make([]fakegraph.Channel, 0, nChannels)
	for i := 0; i < nChannels; i++ {
		channels = append(channels, fakegraph.Channel{ID: fmt.Sprintf("read-c-%03d", i), DisplayName: fmt.Sprintf("Channel %03d", i)})
	}
	teams = append(teams,
		fakegraph.Team{ID: "t-many-channels", DisplayName: "Many Channels", Channels: channels},
		fakegraph.Team{ID: "t-many-members", DisplayName: "Many Members", Members: members},
	)
	return fakegraph.Model{Me: readMe, Users: users, Teams: teams}
}

// ---------------------------------------------------------------------------
// teams.go
// ---------------------------------------------------------------------------

// TestListJoinedTeamsPages follows the pager across the collection's page
// window. The client asks for the documented maximum window of a directory
// collection, 999 (refs/graph/api-reference/v1.0/api/team-list-members.md:45),
// and follows the @odata.nextLink the service returns — the spike confirmed
// Teams collections do hand one back (docs/spike/phase1.md:49-50).
func TestListJoinedTeamsPages(t *testing.T) {
	srv := readServer(t, pagingModel(1001, 1, 1))
	c := readClient(t, srv)

	teams, err := c.ListJoinedTeams(context.Background())
	if err != nil {
		t.Fatalf("ListJoinedTeams: %v", err)
	}
	if len(teams) != 1003 {
		t.Fatalf("teams = %d, want 1003", len(teams))
	}
	if teams[0].ID != "t-pick-0000" || teams[1000].ID != "t-pick-1000" || teams[1002].ID != "t-many-members" {
		t.Errorf("team order = %s, %s, %s", teams[0].ID, teams[1000].ID, teams[1002].ID)
	}
	if teams[0].DisplayName != "Team 0000" {
		t.Errorf("displayName = %q, want %q", teams[0].DisplayName, "Team 0000")
	}
	// /me/joinedTeams documents no query options at all
	// (refs/graph/api-reference/v1.0/api/user-list-joinedteams.md:39), so the client
	// pages with the link alone: no $top anywhere, and a paging token from page two
	// on. A live tenant answers 400 "Query option 'Top' is not allowed" to $top here.
	calls := readCalls(t, srv, http.MethodGet, "/me/joinedTeams")
	if len(calls) < 2 {
		t.Fatalf("requests = %d, want the listing to page", len(calls))
	}
	for i, call := range calls {
		if got := call.Query.Get("$top"); got != "" {
			t.Errorf("request %d sent $top = %q; the endpoint documents no query options", i+1, got)
		}
		if i > 0 && call.Query.Get("$skiptoken") == "" {
			t.Errorf("request %d carries no paging token: %v", i+1, call.Query)
		}
	}
}

// TestGetTeamReadsTheTeamResource covers GET /teams/{team-id}
// (refs/graph/api-reference/v1.0/api/team-get.md:38).
func TestGetTeamReadsTheTeamResource(t *testing.T) {
	_, c := readSetup(t)

	team, err := c.GetTeam(context.Background(), readTeamID)
	if err != nil {
		t.Fatalf("GetTeam: %v", err)
	}
	if team.ID != readTeamID || team.DisplayName != "Engineering" || team.Description != "Builds things" {
		t.Errorf("team = %+v", team)
	}
	if team.Visibility != "public" {
		t.Errorf("visibility = %q, want the seeded default public", team.Visibility)
	}
	if team.TenantID == "" {
		t.Error("tenantId is empty")
	}
	if _, err := c.GetTeam(context.Background(), "t-missing"); !IsNotFound(err) {
		t.Errorf("missing team = %v, want a not-found error", err)
	}
}

// TestListChannelsPages pages the collection even though the wrapper sends no
// $top: the service's own page size applies and the response carries
// @odata.nextLink when the result spans pages
// (refs/graph/api-reference/v1.0/api/channel-list.md:44,63).
func TestListChannelsPages(t *testing.T) {
	srv := readServer(t, pagingModel(1, 101, 1))
	c := readClient(t, srv)

	channels, err := c.ListChannels(context.Background(), "t-many-channels")
	if err != nil {
		t.Fatalf("ListChannels: %v", err)
	}
	if len(channels) != 101 {
		t.Fatalf("channels = %d, want 101", len(channels))
	}
	if channels[0].ID != "read-c-000" || channels[100].ID != "read-c-100" {
		t.Errorf("order = %s ... %s", channels[0].ID, channels[100].ID)
	}
	calls := readCalls(t, srv, http.MethodGet, "/teams/t-many-channels/channels")
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	if len(calls[0].Query) != 0 {
		t.Errorf("first request query = %v, want none (the server default page applies)", calls[0].Query)
	}
	if got := calls[1].Query.Get("$skiptoken"); got != "100" {
		t.Errorf("second request $skiptoken = %q, want 100", got)
	}
}

// TestGetChannelReadsMembershipType covers GET
// /teams/{team-id}/channels/{channel-id}
// (refs/graph/api-reference/v1.0/api/channel-get.md:38) and the membershipType
// vocabulary that tells a standard channel from a private one
// (refs/graph/api-reference/v1.0/resources/channel.md:77).
func TestGetChannelReadsMembershipType(t *testing.T) {
	srv, c := readSetup(t)

	standard, err := c.GetChannel(context.Background(), readTeamID, readChannelID)
	if err != nil {
		t.Fatalf("GetChannel: %v", err)
	}
	if standard.ID != readChannelID || standard.DisplayName != "General" {
		t.Errorf("channel = %+v", standard)
	}
	if standard.MembershipType != "standard" {
		t.Errorf("membershipType = %q, want standard", standard.MembershipType)
	}
	private, err := c.GetChannel(context.Background(), readTeamID, readPrivateCh)
	if err != nil {
		t.Fatalf("GetChannel(private): %v", err)
	}
	if private.MembershipType != "private" {
		t.Errorf("private membershipType = %q, want private", private.MembershipType)
	}
	calls := readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general")
	if len(calls) != 1 {
		t.Fatalf("requests = %d, want 1", len(calls))
	}
	if len(calls[0].Query) != 0 {
		t.Errorf("query = %v, want none", calls[0].Query)
	}
}

// TestListTeamMembersPages checks the 100-item default window and that member
// lists really do page, which the spike confirmed at $top=5 where the docs
// imply no paging (refs/graph/api-reference/v1.0/api/team-list-members.md:45;
// docs/spike/phase1.md:50).
func TestListTeamMembersPages(t *testing.T) {
	// This test runs without the Layer 6 hook:
	// refs/graph/api-reference/v1.0/api/team-list-members.md:41 documents
	// GET /teams/{team-id}/members, but neither the committed route list nor the
	// trimmed description (internal/testing/testdata/openapi/) carries that
	// operation, so the hook has nothing to check it against. The request shape
	// is asserted directly instead.
	srv := readServer(t, pagingModel(1, 1, 101), withoutContract())
	c := readClient(t, srv)

	members, err := c.ListTeamMembers(context.Background(), "t-many-members")
	if err != nil {
		t.Fatalf("ListTeamMembers: %v", err)
	}
	if len(members) != 101 {
		t.Fatalf("members = %d, want 101", len(members))
	}
	if members[0].UserID != readMe || members[0].DisplayName != "Me Myself" {
		t.Errorf("first member = %+v", members[0])
	}
	if members[100].UserID != "u-mem-099" {
		t.Errorf("last member = %+v", members[100])
	}
	if len(members[0].Roles) != 1 || members[0].Roles[0] != "owner" {
		t.Errorf("first member roles = %v, want [owner]", members[0].Roles)
	}
	calls := readCalls(t, srv, http.MethodGet, "/teams/t-many-members/members")
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	if got := calls[0].Query.Get("$top"); got != "100" {
		t.Errorf("$top = %q, want the documented default of 100", got)
	}
	if got := calls[1].Query.Get("$skiptoken"); got != "100" {
		t.Errorf("$skiptoken = %q, want 100", got)
	}
}

// ---------------------------------------------------------------------------
// chats.go
// ---------------------------------------------------------------------------

// TestListChatsQueryBuilding pins the exact query the wrapper sends. The
// documented surface is $expand=members|lastMessagePreview, a $top ceiling of
// 50, a pass-through $filter and $orderby=lastMessagePreview/createdDateTime
// descending only (refs/graph/api-reference/v1.0/api/chat-list.md:62-65).
func TestListChatsQueryBuilding(t *testing.T) {
	srv, c := readSetup(t)
	cases := []struct {
		name string
		q    ChatQuery
		want url.Values
	}{
		{"the zero query takes the documented 50", ChatQuery{}, url.Values{"$top": {"50"}}},
		{"a larger $top is clamped to 50", ChatQuery{Top: 500}, url.Values{"$top": {"50"}}},
		{"a smaller $top is kept", ChatQuery{Top: 5}, url.Values{"$top": {"5"}}},
		{"members", ChatQuery{Members: true}, url.Values{"$top": {"50"}, "$expand": {"members"}}},
		{"preview", ChatQuery{LastMessagePreview: true}, url.Values{"$top": {"50"}, "$expand": {"lastMessagePreview"}}},
		{
			"both expansions share one $expand",
			ChatQuery{Members: true, LastMessagePreview: true},
			url.Values{"$top": {"50"}, "$expand": {"members,lastMessagePreview"}},
		},
		{
			"the documented ordering",
			ChatQuery{OrderByLastMessage: true},
			url.Values{"$top": {"50"}, "$orderby": {"lastMessagePreview/createdDateTime desc"}},
		},
		{
			"a raw $filter is passed through",
			ChatQuery{Filter: "topic eq 'Release train'"},
			url.Values{"$top": {"50"}, "$filter": {"topic eq 'Release train'"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv.ResetRequests()
			if _, err := c.ListChats(context.Background(), tc.q); err != nil {
				t.Fatalf("ListChats(%+v): %v", tc.q, err)
			}
			calls := readCalls(t, srv, http.MethodGet, "/me/chats")
			if got := calls[0].Query.Encode(); got != tc.want.Encode() {
				t.Errorf("query = %v, want %v", calls[0].Query, tc.want)
			}
		})
	}
}

// readChatIDs maps chats to their ids.
func readChatIDs(chats []Chat) []string {
	out := make([]string, 0, len(chats))
	for _, chat := range chats {
		out = append(out, chat.ID)
	}
	return out
}

// TestListChatsDecodesMembersPreviewAndViewpoint reads the documented expanded
// properties: the members of $expand=members, the newest message of
// $expand=lastMessagePreview and the delegated-only viewpoint
// (refs/graph/api-reference/v1.0/api/chat-list.md:20,62;
// refs/graph/api-reference/v1.0/resources/chatviewpoint.md).
func TestListChatsDecodesMembersPreviewAndViewpoint(t *testing.T) {
	_, c := readSetup(t)

	chats, err := c.ListChats(context.Background(), ChatQuery{
		Top: 5, Members: true, LastMessagePreview: true, OrderByLastMessage: true,
	})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 3 {
		t.Fatalf("chats = %d, want 3", len(chats))
	}
	// $orderby=lastMessagePreview/createdDateTime desc orders by the newest
	// message: chat-1on1's newest message is readT4, chat-frank has no messages
	// so its own createdDateTime (readT3) applies, chat-group readT2.
	if ids := readChatIDs(chats); !readEqual(ids, []string{readChatOneOnOne, readChatFrank, readChatGroup}) {
		t.Fatalf("chat order = %v", ids)
	}
	chat := chats[0]
	if chat.ChatType != "oneOnOne" {
		t.Errorf("chatType = %q", chat.ChatType)
	}
	if chat.Topic != nil && *chat.Topic != "" {
		t.Errorf("topic = %q, want nil for a one-on-one chat", *chat.Topic)
	}
	if len(chat.Members) != 2 {
		t.Fatalf("members = %+v, want 2", chat.Members)
	}
	if chat.Members[1].UserID != readAlice || chat.Members[1].DisplayName != "Alice Example" {
		t.Errorf("member = %+v", chat.Members[1])
	}
	if chat.Members[1].Email != "alice@contoso.example" {
		t.Errorf("member email = %q", chat.Members[1].Email)
	}
	if chat.LastMessagePreview == nil {
		t.Fatal("lastMessagePreview is missing")
	}
	if chat.LastMessagePreview.ID != "gm-b" || !strings.Contains(chat.LastMessagePreview.Body.Content, "hello") {
		t.Errorf("lastMessagePreview = %+v", chat.LastMessagePreview)
	}
	if chat.Viewpoint == nil || chat.Viewpoint.LastMessageReadDateTime == nil {
		t.Fatalf("viewpoint = %+v", chat.Viewpoint)
	}
	if !chat.Viewpoint.LastMessageReadDateTime.Equal(readT2) {
		t.Errorf("lastMessageReadDateTime = %v, want %v", chat.Viewpoint.LastMessageReadDateTime, readT2)
	}
	// The group chat's topic is a pointer, not the empty string.
	if chats[2].Topic == nil || *chats[2].Topic != "Release train" {
		t.Errorf("group topic = %v", chats[2].Topic)
	}
}

// TestListChatsHonoursLimit stops after Limit chats even when another page
// exists, and never asks for more than 50 per page
// (refs/graph/api-reference/v1.0/api/chat-list.md:63,69-70).
func TestListChatsHonoursLimit(t *testing.T) {
	srv, c := readSetup(t)

	chats, err := c.ListChats(context.Background(), ChatQuery{Top: 2, Limit: 3})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if ids := readChatIDs(chats); !readEqual(ids, []string{readChatOneOnOne, readChatGroup, readChatFrank}) {
		t.Errorf("chats = %v, want the first three in seed order", ids)
	}
	calls := readCalls(t, srv, http.MethodGet, "/me/chats")
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2 (the limit is reached on the second page)", len(calls))
	}
	if got := calls[0].Query.Get("$top"); got != "2" {
		t.Errorf("$top = %q, want 2", got)
	}
}

// TestGetChatExpandsMembersOnRequest covers GET /me/chats/{chat-id}, including
// the documented $expand=members form
// (refs/graph/api-reference/v1.0/api/chat-get.md:222).
func TestGetChatExpandsMembersOnRequest(t *testing.T) {
	srv, c := readSetup(t)

	expanded, err := c.GetChat(context.Background(), readChatGroup, true)
	if err != nil {
		t.Fatalf("GetChat: %v", err)
	}
	if len(expanded.Members) != 3 {
		t.Fatalf("members = %+v, want 3", expanded.Members)
	}
	if expanded.Members[0].UserID != readMe || expanded.Members[0].DisplayName != "Me Myself" {
		t.Errorf("first member = %+v", expanded.Members[0])
	}
	calls := readCalls(t, srv, http.MethodGet, "/me/chats/chat-group")
	if got := calls[0].Query.Get("$expand"); got != "members" {
		t.Errorf("$expand = %q, want members", got)
	}

	srv.ResetRequests()
	plain, err := c.GetChat(context.Background(), readChatGroup, false)
	if err != nil {
		t.Fatalf("GetChat(plain): %v", err)
	}
	if len(plain.Members) != 0 {
		t.Errorf("members = %+v, want none without $expand", plain.Members)
	}
	calls = readCalls(t, srv, http.MethodGet, "/me/chats/chat-group")
	if len(calls[0].Query) != 0 {
		t.Errorf("query = %v, want none", calls[0].Query)
	}
}

// TestListChatMembers covers GET /chats/{chat-id}/members, which the
// api-reference says takes no OData query parameters
// (refs/graph/api-reference/v1.0/api/chat-list-members.md:42) and which the
// spike saw return the whole 37-member roster in one response
// (docs/spike/phase1.md:68).
func TestListChatMembers(t *testing.T) {
	// The GET of this path is documented
	// (refs/graph/api-reference/v1.0/api/chat-list-members.md:36) and the fake
	// serves it, but the committed route list carries only the POST for it
	// (internal/testing/testdata/openapi/routes.txt:14, chat-post-members.md) and
	// the trimmed description declares no GET for /chats/{chat-id}/members, so the
	// Layer 6 hook has no operation to check. The call runs without the hook and
	// the request shape is asserted directly.
	srv := readServer(t, readModel(), withoutContract())
	c := readClient(t, srv)

	members, err := c.ListChatMembers(context.Background(), readChatGroup)
	if err != nil {
		t.Fatalf("ListChatMembers: %v", err)
	}
	if len(members) != 3 {
		t.Fatalf("members = %d, want 3", len(members))
	}
	var alice *ConversationMember
	for i := range members {
		if members[i].UserID == readAlice {
			alice = &members[i]
		}
	}
	if alice == nil {
		t.Fatalf("alice is missing from %+v", members)
	}
	if alice.DisplayName != "Alice Example" || alice.Email != "alice@contoso.example" {
		t.Errorf("alice = %+v", alice)
	}
	if alice.ODataType != "#microsoft.graph.aadUserConversationMember" {
		t.Errorf("@odata.type = %q", alice.ODataType)
	}
	calls := readCalls(t, srv, http.MethodGet, "/chats/chat-group/members")
	if len(calls) != 1 || len(calls[0].Query) != 0 {
		t.Errorf("requests = %d with query %v, want one request and no query", len(calls), calls[0].Query)
	}
}

// TestChatFilterQuoting pins the OData literal escaping a $filter needs — a
// single quote inside a literal is doubled and there is no backslash escape
// (refs/graph/concepts/query-parameters.md, "Filter parameter"; PLAN.md "Escape
// ' in OData $filter user search by doubling it") — and shows the plain forms
// being applied by the service.
func TestChatFilterQuoting(t *testing.T) {
	if got := ChatFilterTopic("Release train"); got != "topic eq 'Release train'" {
		t.Errorf("ChatFilterTopic = %q", got)
	}
	if got := ChatFilterTopic("Frank's train"); got != "topic eq 'Frank''s train'" {
		t.Errorf("ChatFilterTopic with a quote = %q, want the quote doubled", got)
	}
	if got := ChatFilterType("oneOnOne"); got != "chatType eq 'oneOnOne'" {
		t.Errorf("ChatFilterType = %q", got)
	}

	_, c := readSetup(t)
	chats, err := c.ListChats(context.Background(), ChatQuery{Filter: ChatFilterTopic("Release train")})
	if err != nil {
		t.Fatalf("ListChats(topic): %v", err)
	}
	if ids := readChatIDs(chats); !readEqual(ids, []string{readChatGroup}) {
		t.Errorf("filtered chats = %v, want only chat-group", ids)
	}
	chats, err = c.ListChats(context.Background(), ChatQuery{Filter: ChatFilterType("oneOnOne")})
	if err != nil {
		t.Fatalf("ListChats(chatType): %v", err)
	}
	if ids := readChatIDs(chats); !readEqual(ids, []string{readChatOneOnOne}) {
		t.Errorf("filtered chats = %v, want only the one-on-one chat", ids)
	}
}

// ---------------------------------------------------------------------------
// messages.go — channel messages
// ---------------------------------------------------------------------------

// TestListChannelMessagesQueryBuilding pins the two documented parameters of
// the channel message list: $top defaults to 20 and caps at 50, $expand=replies
// is what carries a root's replies, and the other OData parameters "aren't
// currently supported" — the live service answers 400 for $filter instead of
// ignoring it (refs/graph/api-reference/v1.0/api/channel-list-messages.md:48-51;
// docs/spike/phase1.md:52-53).
func TestListChannelMessagesQueryBuilding(t *testing.T) {
	srv, c := readSetup(t)
	cases := []struct {
		name string
		q    MessageQuery
		want url.Values
	}{
		{"the zero query takes the documented 20", MessageQuery{}, url.Values{"$top": {"20"}}},
		{"a larger $top is clamped to 50", MessageQuery{Top: 500}, url.Values{"$top": {"50"}}},
		{"Replies asks for the expansion", MessageQuery{Replies: true}, url.Values{"$top": {"20"}, "$expand": {"replies"}}},
		{"a --since window needs the replies", MessageQuery{Since: readT3}, url.Values{"$top": {"20"}, "$expand": {"replies"}}},
		{"an --until window needs the replies", MessageQuery{Until: readT3}, url.Values{"$top": {"20"}, "$expand": {"replies"}}},
		{"a window keeps a smaller $top", MessageQuery{Top: 5, Since: readT3}, url.Values{"$top": {"5"}, "$expand": {"replies"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv.ResetRequests()
			if _, err := c.ListChannelMessages(context.Background(), readTeamID, readChannelID, tc.q); err != nil {
				t.Fatalf("ListChannelMessages(%+v): %v", tc.q, err)
			}
			calls := readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/messages")
			if got := calls[0].Query.Encode(); got != tc.want.Encode() {
				t.Errorf("query = %v, want %v", calls[0].Query, tc.want)
			}
			if calls[0].Query.Get("$filter") != "" || calls[0].Query.Get("$orderby") != "" {
				t.Errorf("query = %v, want no $filter and no $orderby", calls[0].Query)
			}
		})
	}
}

// TestListChannelMessagesStopsAtTheFirstOldChain walks the listing in the
// documented order — "sorted by the last modified date of the entire reply
// chain" (refs/graph/api-reference/v1.0/api/channel-list-messages.md:65) — and
// stops paging at the first chain older than --since, which is the consequence
// the spike's ordering observation prescribes (docs/spike/phase1.md:55,109).
func TestListChannelMessagesStopsAtTheFirstOldChain(t *testing.T) {
	srv, c := readSetup(t)

	msgs, err := c.ListChannelMessages(context.Background(), readTeamID, readWindowCh, MessageQuery{Top: 3, Since: readT3})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	if ids := readIDs(msgs); !readEqual(ids, []string{"w-new", "w-mid"}) {
		t.Errorf("messages = %v, want the two chains at or after the window", ids)
	}
	calls := readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-window/messages")
	if len(calls) != 1 {
		t.Fatalf("requests = %d, want 1: the first page already ended on a chain older than the window", len(calls))
	}
	if got := calls[0].Query.Get("$top"); got != "3" {
		t.Errorf("$top = %q, want 3", got)
	}
}

// TestListChannelMessagesSortsByChainActivity proves the result is ordered by
// the whole reply chain's last activity — a root's own lastModifiedDateTime
// misses reply activity, which the spike saw for 8 of 20 roots
// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:65;
// docs/spike/phase1.md:55) — with the message id as the tie-break. The
// re-sort needs the reply timestamps the expansion carries, so this is the
// request that asks for $expand=replies
// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:49).
func TestListChannelMessagesSortsByChainActivity(t *testing.T) {
	srv, c := readSetup(t)

	msgs, err := c.ListChannelMessages(context.Background(), readTeamID, readOrderCh, MessageQuery{Replies: true})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	// or-chain's reply was edited at readT5, so the client's chain activity is
	// readT5; the server orders by the reply's createdDateTime (readT2) and
	// therefore sends or-chain last. or-tie-a and or-tie-b share readT4, so the
	// id breaks the tie descending.
	want := []string{"or-chain", "or-tie-b", "or-tie-a", "or-old"}
	if ids := readIDs(msgs); !readEqual(ids, want) {
		t.Errorf("messages = %v, want %v", ids, want)
	}
	var page Page[Message]
	calls := readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-order/messages")
	if err := json.Unmarshal(calls[0].Response, &page); err != nil {
		t.Fatalf("decode the recorded response: %v", err)
	}
	if ids := readIDs(page.Value); readEqual(ids, want) {
		t.Errorf("the service sent %v, which is already the result order: the test no longer proves the client sorts", ids)
	}
}

// TestListChannelMessagesKeepsTheServerOrderWithoutReplies is the companion of
// the sort test: without $expand=replies the client holds no reply timestamps, so
// it must leave the page in the order the service sent — which is already
// "the last modified date of the entire reply chain"
// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:65). A root whose
// activity comes entirely from a reply is the case that tells the two orders
// apart: the spike saw the root's own timestamp and the chain activity disagree
// for 8 of 20 roots (docs/spike/phase1.md:55), so re-sorting on the root's
// timestamp alone would push that thread down the listing.
func TestListChannelMessagesKeepsTheServerOrderWithoutReplies(t *testing.T) {
	srv, c := readSetup(t)

	msgs, err := c.ListChannelMessages(context.Background(), readTeamID, readReplyCh, MessageQuery{})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	calls := readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-reply-driven/messages")
	if got := calls[0].Query.Get("$expand"); got != "" {
		t.Errorf("$expand = %q, want none: no replies were asked for", got)
	}
	var sent Page[Message]
	if err := json.Unmarshal(calls[0].Response, &sent); err != nil {
		t.Fatalf("decode the recorded response: %v", err)
	}
	sentIDs := readIDs(sent.Value)
	want := []string{"rd-reply-new", "rd-tie-b", "rd-tie-a", "rd-old"}
	if !readEqual(sentIDs, want) {
		t.Fatalf("the service sent %v, want %v", sentIDs, want)
	}
	// rd-reply-new's own lastModifiedDateTime (readT2) is older than the reply
	// that gives it its place at the head of the chain order.
	if got := readIDs(msgs); !readEqual(got, sentIDs) {
		t.Errorf("messages = %v, want the service order %v", got, sentIDs)
	}
}

// TestListChannelMessagesHonoursLimit takes the newest Limit chains of a
// thread-view request ($expand=replies) and stops there.
func TestListChannelMessagesHonoursLimit(t *testing.T) {
	srv, c := readSetup(t)

	msgs, err := c.ListChannelMessages(context.Background(), readTeamID, readChannelID,
		MessageQuery{Top: 50, Limit: 2, Replies: true})
	if err != nil {
		t.Fatalf("ListChannelMessages: %v", err)
	}
	if ids := readIDs(msgs); !readEqual(ids, []string{"cm-2", "cm-sys"}) {
		t.Errorf("messages = %v, want the two newest chains", ids)
	}
	calls := readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/messages")
	if len(calls) != 1 {
		t.Fatalf("requests = %d, want 1", len(calls))
	}
	if got := calls[0].Query.Get("$expand"); got != "replies" {
		t.Errorf("$expand = %q, want replies", got)
	}
}

// TestListChannelReplies caps $top at the documented 50, sends no other OData
// parameter and applies the timestamp window on the client, because the
// endpoint supports only $top
// (refs/graph/api-reference/v1.0/api/chatmessage-list-replies.md:41-42).
func TestListChannelReplies(t *testing.T) {
	srv, c := readSetup(t)
	ctx := context.Background()

	all, err := c.ListChannelReplies(ctx, readTeamID, readChannelID, "cm-2", MessageQuery{Top: 500})
	if err != nil {
		t.Fatalf("ListChannelReplies: %v", err)
	}
	if ids := readIDs(all); !readEqual(ids, []string{"cr-2", "cr-3", "cr-1"}) {
		t.Errorf("replies = %v, want them newest first", ids)
	}
	calls := readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/messages/cm-2/replies")
	if got := calls[0].Query.Get("$top"); got != "50" {
		t.Errorf("$top = %q, want the documented maximum of 50", got)
	}
	if len(calls[0].Query) != 1 {
		t.Errorf("query = %v, want only $top", calls[0].Query)
	}

	srv.ResetRequests()
	windowed, err := c.ListChannelReplies(ctx, readTeamID, readChannelID, "cm-2", MessageQuery{Since: readT3, Until: readT5})
	if err != nil {
		t.Fatalf("ListChannelReplies(window): %v", err)
	}
	if ids := readIDs(windowed); !readEqual(ids, []string{"cr-2", "cr-3"}) {
		t.Errorf("windowed replies = %v, want cr-2 and cr-3", ids)
	}
	calls = readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/messages/cm-2/replies")
	if calls[0].Query.Get("$filter") != "" || calls[0].Query.Get("$orderby") != "" {
		t.Errorf("query = %v, want the window applied on the client", calls[0].Query)
	}
}

// TestMessageReadsSendPreferUnknownEnumMembers asserts the header on every
// message read, because without it Graph collapses a systemEventMessage to
// unknownFutureValue (refs/graph/api-reference/v1.0/resources/chatmessage.md:81;
// docs/spike/phase1.md:54) and the parsed MessageType would be wrong.
func TestMessageReadsSendPreferUnknownEnumMembers(t *testing.T) {
	srv, c := readSetup(t)
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
		path string
	}{
		{"GetChannelMessage", func() error {
			_, err := c.GetChannelMessage(ctx, readTeamID, readChannelID, "cm-sys")
			return err
		}, "/teams/t-eng/channels/c-general/messages/cm-sys"},
		{"GetChannelReply", func() error {
			_, err := c.GetChannelReply(ctx, readTeamID, readChannelID, "cm-2", "cr-1")
			return err
		}, "/teams/t-eng/channels/c-general/messages/cm-2/replies/cr-1"},
		{"GetChatMessage", func() error {
			_, err := c.GetChatMessage(ctx, readChatOneOnOne, readHostedChatMsg)
			return err
		}, "/chats/chat-1on1/messages/gm-hc"},
		{"ListChannelMessages", func() error {
			_, err := c.ListChannelMessages(ctx, readTeamID, readChannelID, MessageQuery{})
			return err
		}, "/teams/t-eng/channels/c-general/messages"},
		{"ListChannelReplies", func() error {
			_, err := c.ListChannelReplies(ctx, readTeamID, readChannelID, "cm-2", MessageQuery{})
			return err
		}, "/teams/t-eng/channels/c-general/messages/cm-2/replies"},
		{"ListChatMessages", func() error {
			_, err := c.ListChatMessages(ctx, readChatOneOnOne, MessageQuery{})
			return err
		}, "/chats/chat-1on1/messages"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv.ResetRequests()
			if err := tc.call(); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			calls := readCalls(t, srv, http.MethodGet, tc.path)
			for _, rec := range calls {
				if got := rec.Header.Get("Prefer"); got != PreferUnknownEnumMembers {
					t.Errorf("%s %s: Prefer = %q, want %q", rec.Method, rec.Path, got, PreferUnknownEnumMembers)
				}
			}
		})
	}

	// The payoff: the evolvable enum member survives decoding.
	sys, err := c.GetChannelMessage(ctx, readTeamID, readChannelID, "cm-sys")
	if err != nil {
		t.Fatalf("GetChannelMessage(cm-sys): %v", err)
	}
	if sys.MessageType != "systemEventMessage" || !sys.IsSystemEvent() {
		t.Errorf("messageType = %q, want systemEventMessage", sys.MessageType)
	}
}

// TestGetChannelMessageReadsTheMessage covers GET
// /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}
// (refs/graph/api-reference/v1.0/api/chatmessage-get.md).
func TestGetChannelMessageReadsTheMessage(t *testing.T) {
	srv, c := readSetup(t)

	msg, err := c.GetChannelMessage(context.Background(), readTeamID, readChannelID, "cm-2")
	if err != nil {
		t.Fatalf("GetChannelMessage: %v", err)
	}
	if msg.ID != "cm-2" || msg.Subject != "Deploy" || !strings.Contains(msg.Body.Content, "deploy is done") {
		t.Errorf("message = %+v", msg)
	}
	if !msg.CreatedDateTime.Equal(readT4) {
		t.Errorf("createdDateTime = %v, want %v", msg.CreatedDateTime, readT4)
	}
	if msg.From == nil || msg.From.User == nil || msg.From.User.DisplayName != "Bob Builder" {
		t.Errorf("from = %+v", msg.From)
	}
	if msg.ChannelIdentity == nil || msg.ChannelIdentity.TeamID != readTeamID || msg.ChannelIdentity.ChannelID != readChannelID {
		t.Errorf("channelIdentity = %+v", msg.ChannelIdentity)
	}
	if msg.IsReply() {
		t.Error("a root message reports itself as a reply")
	}
	if calls := readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/messages/cm-2"); len(calls) != 1 {
		t.Fatalf("requests = %d, want 1", len(calls))
	}
}

// TestGetChatMessageReadsTheChatMessage covers GET
// /chats/{chat-id}/messages/{chatMessage-id}
// (refs/graph/api-reference/v1.0/api/chat-list-messages.md:38).
func TestGetChatMessageReadsTheChatMessage(t *testing.T) {
	srv, c := readSetup(t)

	msg, err := c.GetChatMessage(context.Background(), readChatOneOnOne, readHostedChatMsg)
	if err != nil {
		t.Fatalf("GetChatMessage: %v", err)
	}
	if msg.ID != readHostedChatMsg || msg.ChatID != readChatOneOnOne {
		t.Errorf("message = %+v", msg)
	}
	if !strings.Contains(msg.Body.Content, "inline") {
		t.Errorf("body = %q", msg.Body.Content)
	}
	if calls := readCalls(t, srv, http.MethodGet, "/chats/chat-1on1/messages/gm-hc"); len(calls) != 1 {
		t.Fatalf("requests = %d, want 1", len(calls))
	}
}

// TestGetChannelReplyReadsTheReply covers GET
// /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}
// (refs/graph/api-reference/v1.0/api/chatmessage-get.md).
func TestGetChannelReplyReadsTheReply(t *testing.T) {
	srv, c := readSetup(t)

	reply, err := c.GetChannelReply(context.Background(), readTeamID, readChannelID, "cm-2", "cr-2")
	if err != nil {
		t.Fatalf("GetChannelReply: %v", err)
	}
	if reply.ID != "cr-2" || reply.ReplyToID != "cm-2" || !reply.IsReply() {
		t.Errorf("reply = %+v", reply)
	}
	if !reply.CreatedDateTime.Equal(readT5) {
		t.Errorf("createdDateTime = %v, want %v", reply.CreatedDateTime, readT5)
	}
	readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/messages/cm-2/replies/cr-2")
}

// TestListChatMessagesSince covers the documented --since shape: $orderby on
// lastModifiedDateTime descending plus a matching $filter, because a $filter is
// ignored unless the request orders by the same property
// (refs/graph/api-reference/v1.0/api/chat-list-messages.md:48-49; PLAN.md:182-183).
// The filter returns a superset — an edited message has a newer modified time —
// so the client re-checks createdDateTime and drops an edited-but-old message
// the service returned.
func TestListChatMessagesSince(t *testing.T) {
	srv, c := readSetup(t)
	since := readT3

	msgs, err := c.ListChatMessages(context.Background(), readChatOneOnOne, MessageQuery{Since: since})
	if err != nil {
		t.Fatalf("ListChatMessages: %v", err)
	}
	calls := readCalls(t, srv, http.MethodGet, "/chats/chat-1on1/messages")
	if got := calls[0].Query.Get("$orderby"); got != "lastModifiedDateTime desc" {
		t.Errorf("$orderby = %q", got)
	}
	wantFilter := "lastModifiedDateTime gt " + since.UTC().Format(time.RFC3339)
	if got := calls[0].Query.Get("$filter"); got != wantFilter {
		t.Errorf("$filter = %q, want %q", got, wantFilter)
	}
	// gm-edited-old was written at readT1 and edited at readT5, so the service
	// returns it; the client's createdDateTime re-check drops it again.
	if !strings.Contains(string(calls[0].Response), "gm-edited-old") {
		t.Fatalf("the recorded response does not carry the edited message: %s", calls[0].Response)
	}
	if ids := readIDs(msgs); !readEqual(ids, []string{"gm-b"}) {
		t.Errorf("messages = %v, want only gm-b (the edited message predates the window)", ids)
	}
}

// TestListChatMessagesUntil covers the --until shape: createdDateTime
// descending with the matching lt filter, which the docs allow and which is
// ignored by the service without that $orderby (docs/spike/phase1.md:63).
func TestListChatMessagesUntil(t *testing.T) {
	srv, c := readSetup(t)
	until := readT4

	msgs, err := c.ListChatMessages(context.Background(), readChatOneOnOne, MessageQuery{Until: until})
	if err != nil {
		t.Fatalf("ListChatMessages: %v", err)
	}
	calls := readCalls(t, srv, http.MethodGet, "/chats/chat-1on1/messages")
	if got := calls[0].Query.Get("$orderby"); got != "createdDateTime desc" {
		t.Errorf("$orderby = %q", got)
	}
	wantFilter := "createdDateTime lt " + until.UTC().Format(time.RFC3339)
	if got := calls[0].Query.Get("$filter"); got != wantFilter {
		t.Errorf("$filter = %q, want %q", got, wantFilter)
	}
	if ids := readIDs(msgs); !readEqual(ids, []string{"gm-c", "gm-hc", "gm-a", "gm-edited-old"}) {
		t.Errorf("messages = %v, want the three older ones newest first", ids)
	}
}

// TestListChatMessagesNeverFiltersWithoutOrderBy asserts the documented rule
// directly on the wire, for every window the wrapper can build
// (refs/graph/api-reference/v1.0/api/chat-list-messages.md:49).
func TestListChatMessagesNeverFiltersWithoutOrderBy(t *testing.T) {
	srv, c := readSetup(t)
	cases := []struct {
		name       string
		q          MessageQuery
		wantFilter bool
	}{
		{"no window", MessageQuery{}, false},
		{"since", MessageQuery{Since: readT3}, true},
		{"until", MessageQuery{Until: readT4}, true},
		{"a limit alone", MessageQuery{Limit: 2}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv.ResetRequests()
			if _, err := c.ListChatMessages(context.Background(), readChatOneOnOne, tc.q); err != nil {
				t.Fatalf("ListChatMessages(%+v): %v", tc.q, err)
			}
			calls := readCalls(t, srv, http.MethodGet, "/chats/chat-1on1/messages")
			for _, rec := range calls {
				filter, order := rec.Query.Get("$filter"), rec.Query.Get("$orderby")
				if filter != "" && order == "" {
					t.Errorf("query = %v: a $filter without a matching $orderby is ignored by the service", rec.Query)
				}
				if (filter != "") != tc.wantFilter {
					t.Errorf("query = %v: filter presence = %v, want %v", rec.Query, filter != "", tc.wantFilter)
				}
			}
		})
	}
}

// TestListChatMessagesSortsTheUnsortedListing: the default chat message
// listing is sorted by neither date property, so the client must sort
// (refs/graph/api-reference/v1.0/api/chat-list-messages.md:48;
// docs/spike/phase1.md:60).
func TestListChatMessagesSortsTheUnsortedListing(t *testing.T) {
	srv, c := readSetup(t)

	msgs, err := c.ListChatMessages(context.Background(), readChatOneOnOne, MessageQuery{Top: 50})
	if err != nil {
		t.Fatalf("ListChatMessages: %v", err)
	}
	want := []string{"gm-b", "gm-c", "gm-hc", "gm-a", "gm-edited-old"}
	if ids := readIDs(msgs); !readEqual(ids, want) {
		t.Errorf("messages = %v, want %v", ids, want)
	}
	calls := readCalls(t, srv, http.MethodGet, "/chats/chat-1on1/messages")
	if calls[0].Query.Get("$orderby") != "" || calls[0].Query.Get("$filter") != "" {
		t.Errorf("query = %v, want the default listing requested as-is", calls[0].Query)
	}
	var page Page[Message]
	if err := json.Unmarshal(calls[0].Response, &page); err != nil {
		t.Fatalf("decode the recorded response: %v", err)
	}
	if ids := readIDs(page.Value); readEqual(ids, want) {
		t.Errorf("the service sent %v, which is already sorted: the test no longer proves the client sorts", ids)
	}
}

// TestListMessageHostedContentsAndGetHostedContentValue covers the two inline
// image routes. The listing carries the ids (the api-reference notes that
// contentBytes and contentType are null in a listing response,
// refs/graph/api-reference/v1.0/api/chatmessage-list-hostedcontents.md:127) and
// the bytes come from .../hostedContents/{id}/$value
// (refs/graph/api-reference/v1.0/api/chatmessagehostedcontent-get.md:160).
func TestListMessageHostedContentsAndGetHostedContentValue(t *testing.T) {
	ctx := context.Background()
	chatPath := ChatMessagePath(readChatOneOnOne, readHostedChatMsg)

	// The listing is JSON, so it runs with the Layer 6 hook installed.
	jsonSrv := readServer(t, readModel())
	jsonClient := readClient(t, jsonSrv)
	contents, err := jsonClient.ListMessageHostedContents(ctx, chatPath)
	if err != nil {
		t.Fatalf("ListMessageHostedContents: %v", err)
	}
	if len(contents) != 1 || contents[0].ID != readHostedChatContent {
		t.Fatalf("hostedContents = %+v", contents)
	}
	readCalls(t, jsonSrv, http.MethodGet, "/chats/chat-1on1/messages/gm-hc/hostedContents")

	// The channel listing and the channel form of the byte fetch run on the same
	// hook-installed client: the api-reference page documents the container
	// without the /$value suffix
	// (refs/graph/api-reference/v1.0/api/chatmessagehostedcontent-get.md:160) and
	// fakegraph's hook exempts that one shape
	// (internal/testing/fakegraph/contract.go).
	channelPath := ChannelMessagePath(readTeamID, readChannelID, readHostedMsg)
	channelContents, err := jsonClient.ListMessageHostedContents(ctx, channelPath)
	if err != nil {
		t.Fatalf("ListMessageHostedContents(channel): %v", err)
	}
	if len(channelContents) != 1 || channelContents[0].ID != readHostedContent {
		t.Fatalf("channel hostedContents = %+v", channelContents)
	}
	channelBytes, err := jsonClient.GetHostedContentValue(ctx, channelPath, readHostedContent)
	if err != nil {
		t.Fatalf("GetHostedContentValue(channel): %v", err)
	}
	if string(channelBytes.Bytes) != "png-bytes" || channelBytes.ContentType != "image/png" {
		t.Errorf("channel image = %q, %q", channelBytes.Bytes, channelBytes.ContentType)
	}
	readCalls(t, jsonSrv, http.MethodGet, "/teams/t-eng/channels/c-general/messages/cm-host/hostedContents/hx/$value")
	if _, err := jsonClient.GetHostedContentValue(ctx, channelPath, "hx-missing"); !IsNotFound(err) {
		t.Errorf("missing channel content = %v, want a not-found error", err)
	}

	// The chat form answers a binary stream, which the contract validator cannot
	// check (it injects Content-Type: application/json), so that call runs on a
	// server without the hook.
	srv := readServer(t, readModel(), withoutContract())
	c := readClient(t, srv)
	dl, err := c.GetHostedContentValue(ctx, chatPath, readHostedChatContent)
	if err != nil {
		t.Fatalf("GetHostedContentValue: %v", err)
	}
	if string(dl.Bytes) != "png-bytes" {
		t.Errorf("bytes = %q", dl.Bytes)
	}
	if dl.ContentType != "image/png" {
		t.Errorf("contentType = %q, want image/png", dl.ContentType)
	}
	readCalls(t, srv, http.MethodGet, "/chats/chat-1on1/messages/gm-hc/hostedContents/hc-1/$value")

	if _, err := c.GetHostedContentValue(ctx, chatPath, "hc-missing"); !IsNotFound(err) {
		t.Errorf("missing content = %v, want a not-found error", err)
	}
}

// TestMessagePathEscaping pins the two exported path builders: a Graph id is
// URL-safe apart from the characters that would otherwise change the path
// shape. A Teams chat id keeps its documented separators — the api-reference
// examples use the 19:...@thread.v2 form
// (refs/graph/api-reference/v1.0/api/chat-list-messages.md:36-38).
func TestMessagePathEscaping(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"a plain channel path", ChannelMessagePath("t1", "c1", "m1"), "/teams/t1/channels/c1/messages/m1"},
		{
			"a Teams chat id keeps its separators", ChatMessagePath("19:abc@thread.tacv2", "1616989510408"),
			"/chats/19:abc@thread.tacv2/messages/1616989510408",
		},
		{"spaces are escaped", ChannelMessagePath("t 1", "c 1", "m 1"), "/teams/t%201/channels/c%201/messages/m%201"},
		{"a slash cannot become another segment", ChatMessagePath("19:a/b", "m/1"), "/chats/19:a%2Fb/messages/m%2F1"},
		{"a fragment marker is escaped", ChatMessagePath("19:a#b", "m1"), "/chats/19:a%23b/messages/m1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("path = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestReadMessagesReachTheMessagePath shows the escaped paths really resolve:
// the same ids the path builders are asserted on reach their message.
func TestReadMessagesReachTheMessagePath(t *testing.T) {
	srv, c := readSetup(t)
	ctx := context.Background()

	msg, err := c.GetChatMessage(ctx, readChatOneOnOne, "gm-b")
	if err != nil {
		t.Fatalf("GetChatMessage: %v", err)
	}
	if msg.ID != "gm-b" {
		t.Errorf("message = %+v", msg)
	}
	root, err := c.GetChannelMessage(ctx, readTeamID, readChannelID, "cm-2")
	if err != nil {
		t.Fatalf("GetChannelMessage: %v", err)
	}
	if root.ID != "cm-2" {
		t.Errorf("message = %+v", root)
	}
	readCalls(t, srv, http.MethodGet, "/chats/chat-1on1/messages/gm-b")
	readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/messages/cm-2")
}

// ---------------------------------------------------------------------------
// search.go
// ---------------------------------------------------------------------------

// searchEnvelopeWire is the request body of POST /search/query, decoded from
// the recorder to assert the exact payload the wrapper sends.
type searchEnvelopeWire struct {
	Requests []struct {
		EntityTypes []string `json:"entityTypes"`
		Query       struct {
			QueryString string `json:"queryString"`
		} `json:"query"`
		From   *int     `json:"from"`
		Size   *int     `json:"size"`
		Fields []string `json:"fields,omitempty"`
	} `json:"requests"`
}

// TestSearchMessagesSendsOneChatMessageRequest covers the documented shape of
// POST /search/query: a body whose requests collection carries a queryString,
// from and size, where chatMessage cannot be mixed with another entity type and
// only one searchRequest is supported at a time
// (refs/graph/api-reference/v1.0/api/search-query.md:46;
// refs/graph/api-reference/v1.0/resources/search-api-overview.md:183,191) and
// the default page size is 25 (search-api-overview.md:63).
func TestSearchMessagesSendsOneChatMessageRequest(t *testing.T) {
	srv, c := readSetup(t)

	result, err := c.SearchMessages(context.Background(), SearchQuery{Query: "hello"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	calls := readCalls(t, srv, http.MethodPost, "/search/query")
	var env searchEnvelopeWire
	if err := calls[0].DecodeBody(&env); err != nil {
		t.Fatalf("decode the search body: %v", err)
	}
	if len(env.Requests) != 1 {
		t.Fatalf("requests = %d, want exactly 1", len(env.Requests))
	}
	req := env.Requests[0]
	if len(req.EntityTypes) != 1 || req.EntityTypes[0] != "chatMessage" {
		t.Errorf("entityTypes = %v, want [chatMessage]", req.EntityTypes)
	}
	if req.Query.QueryString != "hello" {
		t.Errorf("queryString = %q", req.Query.QueryString)
	}
	if req.From == nil || *req.From != 0 {
		t.Errorf("from = %v, want 0 on the first page (search-api-overview.md:67)", req.From)
	}
	if req.Size == nil || *req.Size != 25 {
		t.Errorf("size = %v, want the documented default of 25", req.Size)
	}
	if len(req.Fields) != 0 {
		t.Errorf("fields = %v, want none", req.Fields)
	}
	// total is the full match count, which the spike observed and which the docs
	// describe as the page count (docs/spike/phase1.md:77;
	// refs/graph/concepts/search-concept-chat-messages.md:394).
	if result.Total != 2 {
		t.Errorf("total = %d, want 2 (the channel and the chat message)", result.Total)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(result.Hits))
	}
	if len(result.SearchTerms) != 1 || result.SearchTerms[0] != "hello" {
		t.Errorf("searchTerms = %v", result.SearchTerms)
	}
}

// TestSearchHitParsesTheBodylessResource checks the hit's resource, which the
// spike saw carrying no body at all (docs/spike/phase1.md:83): the id, the
// channel identity or chatId, the sender's emailAddress pair, createdDateTime
// and webLink
// (refs/graph/concepts/search-concept-chat-messages.md:361-378).
func TestSearchHitParsesTheBodylessResource(t *testing.T) {
	_, c := readSetup(t)

	result, err := c.SearchMessages(context.Background(), SearchQuery{Query: "hello"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(result.Hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(result.Hits))
	}
	hit := result.Hits[0]
	if hit.HitID != "cm-1" || hit.Rank != 1 {
		t.Errorf("hit = %+v", hit)
	}
	resource := hit.Resource
	if resource.ID != "cm-1" {
		t.Errorf("resource.id = %q", resource.ID)
	}
	if resource.ODataType != "microsoft.graph.chatMessage" {
		t.Errorf("@odata.type = %q", resource.ODataType)
	}
	team, channel, ok := resource.InChannel()
	if !ok || team != readTeamID || channel != readChannelID {
		t.Errorf("InChannel = %q, %q, %v", team, channel, ok)
	}
	if resource.ChatID != "" {
		t.Errorf("chatId = %q, want empty for a channel message", resource.ChatID)
	}
	if !resource.CreatedDateTime.Equal(readT2) {
		t.Errorf("createdDateTime = %v, want %v", resource.CreatedDateTime, readT2)
	}
	if !strings.Contains(resource.WebLink, "/c-general/cm-1") {
		t.Errorf("webLink = %q, want a Teams deep link", resource.WebLink)
	}
	if resource.From == nil || resource.From.DisplayName() != "Alice Example" {
		t.Errorf("from = %+v", resource.From)
	}
	if got := resource.From.EmailAddress.Address; got != "alice@contoso.example" {
		t.Errorf("from.emailAddress.address = %q", got)
	}
	if hit.Summary == "" {
		t.Error("summary is empty")
	}

	chatHit := result.Hits[1]
	if chatHit.Resource.ChatID != readChatOneOnOne {
		t.Errorf("second hit chatId = %q, want %q", chatHit.Resource.ChatID, readChatOneOnOne)
	}
	if _, _, ok := chatHit.Resource.InChannel(); ok {
		t.Error("a chat message hit reports a channel identity")
	}
	if chatHit.Resource.From == nil || chatHit.Resource.From.DisplayName() != "Bob Builder" {
		t.Errorf("second hit from = %+v", chatHit.Resource.From)
	}
}

// TestSearchPagingByFromAndSize walks one page at a time. Search has no
// @odata.nextLink, so the offset is explicit and the first page must start at
// zero (refs/graph/api-reference/v1.0/resources/search-api-overview.md:57-75).
func TestSearchPagingByFromAndSize(t *testing.T) {
	srv, c := readSetup(t)
	ctx := context.Background()

	first, err := c.SearchMessages(ctx, SearchQuery{Query: "hello", Size: 1})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(first.Hits) != 1 || first.Hits[0].HitID != "cm-1" {
		t.Fatalf("first page hits = %+v", first.Hits)
	}
	if first.Total != 2 || !first.MoreResultsAvailable {
		t.Errorf("first page total = %d, more = %v, want 2 and true", first.Total, first.MoreResultsAvailable)
	}

	second, err := c.SearchPage(ctx, "hello", 1, 1)
	if err != nil {
		t.Fatalf("SearchPage: %v", err)
	}
	if len(second.Hits) != 1 || second.Hits[0].HitID != "gm-b" {
		t.Fatalf("second page hits = %+v", second.Hits)
	}
	if second.Hits[0].Rank != 2 {
		t.Errorf("rank = %d, want the offset reflected", second.Hits[0].Rank)
	}
	if second.Total != 2 || second.MoreResultsAvailable {
		t.Errorf("second page total = %d, more = %v, want 2 and false", second.Total, second.MoreResultsAvailable)
	}
	calls := readCalls(t, srv, http.MethodPost, "/search/query")
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	var env searchEnvelopeWire
	if err := calls[1].DecodeBody(&env); err != nil {
		t.Fatal(err)
	}
	if env.Requests[0].From == nil || *env.Requests[0].From != 1 {
		t.Errorf("second request from = %v, want 1", env.Requests[0].From)
	}

	// A page past the end is an empty page, not an error.
	beyond, err := c.SearchPage(ctx, "hello", 2, 1)
	if err != nil {
		t.Fatalf("SearchPage(beyond): %v", err)
	}
	if len(beyond.Hits) != 0 || beyond.Total != 2 {
		t.Errorf("beyond the end = %+v", beyond)
	}
}

// TestSearchMessagesRejectsBadInputBeforeTheRoundTrip keeps the CLI's exit code
// contract intact: an empty query and a negative offset are usage errors
// (exit code 2), and nothing reaches the service.
func TestSearchMessagesRejectsBadInputBeforeTheRoundTrip(t *testing.T) {
	srv, c := readSetup(t)
	cases := []struct {
		name string
		q    SearchQuery
	}{
		{"an empty query", SearchQuery{}},
		{"a whitespace query", SearchQuery{Query: "   	 "}},
		{"a negative offset", SearchQuery{Query: "hello", From: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.SearchMessages(context.Background(), tc.q)
			_ = readUsage(t, err)
			if n := len(srv.RequestsFor(http.MethodPost, "/search/query")); n != 0 {
				t.Errorf("requests = %d, want none before the round trip", n)
			}
		})
	}
}

// TestSearchMessagesSizeIsClampedTo50 sends the largest page the live service
// accepts for chatMessage: the spike saw size=50 work where the docs cap
// mail/event at 25 (docs/spike/phase1.md:75), and the fake answers 400 above
// 50, so a larger request is clamped rather than forwarded.
func TestSearchMessagesSizeIsClampedTo50(t *testing.T) {
	srv, c := readSetup(t)

	if _, err := c.SearchMessages(context.Background(), SearchQuery{Query: "hello", Size: 500}); err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	calls := readCalls(t, srv, http.MethodPost, "/search/query")
	var env searchEnvelopeWire
	if err := calls[0].DecodeBody(&env); err != nil {
		t.Fatal(err)
	}
	if env.Requests[0].Size == nil || *env.Requests[0].Size != MaxTopSearch {
		t.Errorf("size = %v, want the clamp to %d", env.Requests[0].Size, MaxTopSearch)
	}
}

// TestSearchIsReadStillFails pins the observed failure so nobody starts
// depending on the documented-but-broken term: the live service answered 500
// every time, whatever the casing, alone or combined
// (docs/spike/phase1.md:79; refs/graph/concepts/search-concept-chat-messages.md:265).
func TestSearchIsReadStillFails(t *testing.T) {
	srv, c := readSetup(t)

	_, err := c.SearchMessages(context.Background(), SearchQuery{Query: "IsRead:false hello"})
	if err == nil {
		t.Fatal("want the 500 the live service returns for IsRead")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v (%T), want an *APIError", err, err)
	}
	if apiErr.Status != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", apiErr.Status)
	}
	if got := output.CodeOf(err); got != output.CodeError {
		t.Errorf("exit code = %d, want %d", got, output.CodeError)
	}
	calls := readCalls(t, srv, http.MethodPost, "/search/query")
	if len(calls) != 1 {
		t.Errorf("requests = %d, want 1 (retries are off here)", len(calls))
	}
}

// ---------------------------------------------------------------------------
// users.go
// ---------------------------------------------------------------------------

// TestUserFiltersDoubleSingleQuotes asserts the exact OData literal the docs
// require: a single quote inside a literal is doubled and no backslash escape
// exists (refs/graph/concepts/query-parameters.md, "Filter parameter"; PLAN.md
// "Escape ' in OData $filter user search by doubling it").
func TestUserFiltersDoubleSingleQuotes(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{
			"displayName without a quote", UserFilterDisplayName("Ali"),
			"startswith(displayName,'Ali')",
		},
		{
			"displayName with a quote", UserFilterDisplayName("Frank O'Neil"),
			"startswith(displayName,'Frank O''Neil')",
		},
		{
			"a quote-only prefix", UserFilterDisplayName("'"),
			"startswith(displayName,'''')",
		},
		{
			"userPrincipalName with a quote", UserFilterUserPrincipalName("o'neil"),
			"startswith(userPrincipalName,'o''neil')",
		},
		{
			"mail with a quote", UserFilterMail("o'neil@x.example"),
			"startswith(mail,'o''neil@x.example')",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("filter = %q, want %q", tc.got, tc.want)
			}
		})
	}
}

// TestGetUser covers GET /users/{id | userPrincipalName}
// (refs/graph/api-reference/v1.0/api/user-get.md:50), including the documented
// "#" encoding a B2B user principal name needs
// (refs/graph/api-reference/v1.0/api/user-get.md:57-58).
func TestGetUser(t *testing.T) {
	const b2bUPN = "AdeleVance_adatum.com#EXT#@contoso.example"
	srv := readServer(t, fakegraph.Model{
		Me: "u-me",
		Users: []fakegraph.User{
			{ID: "u-me", DisplayName: "Me Myself"},
			{ID: "u-alice", DisplayName: "Alice Example", UserPrincipalName: "alice@contoso.example", Mail: "alice@contoso.example"},
			{ID: "u-b2b", DisplayName: "Adele Vance", UserPrincipalName: b2bUPN, Mail: "adele@contoso.example"},
		},
	})
	rec := &readWireRecorder{}
	c := readClient(t, srv, func(o *Options) { o.Recorder = rec })
	ctx := context.Background()

	byID, err := c.GetUser(ctx, "u-alice")
	if err != nil {
		t.Fatalf("GetUser(id): %v", err)
	}
	if byID.ID != "u-alice" || byID.DisplayName != "Alice Example" {
		t.Errorf("user = %+v", byID)
	}
	byUPN, err := c.GetUser(ctx, "alice@contoso.example")
	if err != nil {
		t.Fatalf("GetUser(upn): %v", err)
	}
	if byUPN.ID != "u-alice" {
		t.Errorf("user = %+v", byUPN)
	}
	if byUPN.Address() != "alice@contoso.example" {
		t.Errorf("address = %q", byUPN.Address())
	}
	b2b, err := c.GetUser(ctx, b2bUPN)
	if err != nil {
		t.Fatalf("GetUser(b2b): %v", err)
	}
	if b2b.ID != "u-b2b" {
		t.Errorf("b2b user = %+v", b2b)
	}
	want := "GET /v1.0/users/AdeleVance_adatum.com%23EXT%23@contoso.example"
	if !readHasLine(rec.lines, want) {
		t.Errorf("wire requests = %v, want %q", rec.lines, want)
	}
	if _, err := c.GetUser(ctx, "u-missing"); !IsNotFound(err) {
		t.Errorf("missing user = %v, want a not-found error", err)
	}
}

// TestListUsersPages covers GET /users: the documented default property set
// (refs/graph/api-reference/v1.0/api/user-list.md:39) returned through the
// 100/999 window (refs/graph/api-reference/v1.0/api/team-list-members.md:45),
// with $filter passed through and a limit honoured.
func TestListUsersPages(t *testing.T) {
	srv := readServer(t, pagingModel(1, 1, 101))
	c := readClient(t, srv)
	ctx := context.Background()

	users, err := c.ListUsers(ctx, "", 0)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if len(users) != 101 {
		t.Fatalf("users = %d, want 101", len(users))
	}
	if users[0].ID != readMe || users[0].DisplayName != "Me Myself" {
		t.Errorf("first user = %+v", users[0])
	}
	if users[100].ID != "u-mem-099" {
		t.Errorf("last user = %+v", users[100])
	}
	calls := readCalls(t, srv, http.MethodGet, "/users")
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want 2", len(calls))
	}
	if got := calls[0].Query.Get("$top"); got != "100" {
		t.Errorf("$top = %q, want 100", got)
	}
	if got := calls[1].Query.Get("$skiptoken"); got != "100" {
		t.Errorf("$skiptoken = %q, want 100", got)
	}

	srv.ResetRequests()
	filtered, err := c.ListUsers(ctx, UserFilterDisplayName("Member"), 5)
	if err != nil {
		t.Fatalf("ListUsers(filter): %v", err)
	}
	if len(filtered) != 5 {
		t.Fatalf("filtered users = %d, want the limit of 5", len(filtered))
	}
	calls = readCalls(t, srv, http.MethodGet, "/users")
	if got, want := calls[0].Query.Get("$filter"), "startswith(displayName,'Member')"; got != want {
		t.Errorf("$filter = %q, want %q", got, want)
	}
	if len(calls) != 1 {
		t.Errorf("requests = %d, want 1 (the limit is reached on the first page)", len(calls))
	}
}

// TestSearchUsersFallsBackFromDisplayNameToUPNToMail covers the three attempts
// PLAN.md:167 prescribes for the /users step: a startswith filter on
// displayName first, then on userPrincipalName, then on mail, stopping at the
// first attempt that finds anybody.
func TestSearchUsersFallsBackFromDisplayNameToUPNToMail(t *testing.T) {
	cases := []struct {
		name    string
		query   string
		wantIDs []string
		want    []string
	}{
		{
			"displayName matches first", "Alice",
			[]string{readAlice},
			[]string{"startswith(displayName,'Alice')"},
		},
		{
			"UPN when the display name does not match", "zenith",
			[]string{readDev},
			[]string{"startswith(displayName,'zenith')", "startswith(userPrincipalName,'zenith')"},
		},
		{
			"mail when neither name nor UPN matches", "smarino",
			[]string{readSofia},
			[]string{"startswith(displayName,'smarino')", "startswith(userPrincipalName,'smarino')", "startswith(mail,'smarino')"},
		},
		{
			"nothing found tries all three", "nobody-here", nil,
			[]string{"startswith(displayName,'nobody-here')", "startswith(userPrincipalName,'nobody-here')", "startswith(mail,'nobody-here')"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := readSetup(t)
			users, err := c.SearchUsers(context.Background(), tc.query, 0)
			if err != nil {
				t.Fatalf("SearchUsers(%q): %v", tc.query, err)
			}
			var ids []string
			for _, u := range users {
				ids = append(ids, u.ID)
			}
			if !readEqual(ids, tc.wantIDs) && len(ids) != 0 {
				t.Fatalf("users = %v, want %v", ids, tc.wantIDs)
			}
			calls := srv.RequestsFor(http.MethodGet, "/users")
			if len(calls) != len(tc.want) {
				t.Fatalf("requests = %d, want %d", len(calls), len(tc.want))
			}
			for i, want := range tc.want {
				if got := calls[i].Query.Get("$filter"); got != want {
					t.Errorf("request %d $filter = %q, want %q", i+1, got, want)
				}
			}
		})
	}
}

// TestSearchUsersRejectsAnEmptyQuery keeps the usage error before the round
// trip, like the search wrapper.
func TestSearchUsersRejectsAnEmptyQuery(t *testing.T) {
	srv, c := readSetup(t)

	_, err := c.SearchUsers(context.Background(), "  	", 0)
	_ = readUsage(t, err)
	if n := len(srv.RequestsFor(http.MethodGet, "/users")); n != 0 {
		t.Errorf("requests = %d, want none before the round trip", n)
	}
}

// TestListPeopleQuotesSearchAndKeepsRelevanceOrder covers GET /me/people: the
// $search value is quoted, the response keeps the service's relevance order —
// "ordered by their relevance to the user, which is determined by the user's
// communication and collaboration patterns"
// (refs/graph/api-reference/v1.0/api/user-list-people.md:15,44) — and that order
// is why people beats a directory search when a name is ambiguous (PLAN.md:167).
func TestListPeopleQuotesSearchAndKeepsRelevanceOrder(t *testing.T) {
	srv, c := readSetup(t)
	ctx := context.Background()

	people, err := c.ListPeople(ctx, "", 0)
	if err != nil {
		t.Fatalf("ListPeople: %v", err)
	}
	wantIDs := []string{readSofia, readBob, readAlice, readCarol, readDev, readFrank}
	var ids []string
	for _, p := range people {
		ids = append(ids, p.ID)
	}
	if !readEqual(ids, wantIDs) {
		t.Fatalf("people = %v, want %v (relevance order, then the display name)", ids, wantIDs)
	}
	if people[0].DisplayName != "Sofia Marino" {
		t.Errorf("first person = %+v", people[0])
	}
	if got := people[0].Address(); got != "smarino@partner.example" {
		t.Errorf("address = %q, want the highest-scored address", got)
	}
	calls := readCalls(t, srv, http.MethodGet, "/me/people")
	if got := calls[0].Query.Get("$top"); got != "25" {
		t.Errorf("$top = %q, want the documented default of 25", got)
	}
	if got := calls[0].Query.Get("$search"); got != "" {
		t.Errorf("$search = %q, want none", got)
	}

	srv.ResetRequests()
	found, err := c.ListPeople(ctx, "ali", 0)
	if err != nil {
		t.Fatalf("ListPeople(search): %v", err)
	}
	if len(found) != 1 || found[0].ID != readAlice {
		t.Fatalf("people = %+v, want Alice", found)
	}
	calls = readCalls(t, srv, http.MethodGet, "/me/people")
	if got, want := calls[0].Query.Get("$search"), readQuote+"ali"+readQuote; got != want {
		t.Errorf("$search = %q, want %q (the documented quoted form)", got, want)
	}

	// A double quote in the term is dropped, so the value stays one phrase.
	srv.ResetRequests()
	found, err = c.ListPeople(ctx, "Ali"+readQuote+"ce", 0)
	if err != nil {
		t.Fatalf("ListPeople(quoted): %v", err)
	}
	if len(found) != 1 || found[0].ID != readAlice {
		t.Fatalf("people = %+v, want Alice", found)
	}
	calls = readCalls(t, srv, http.MethodGet, "/me/people")
	if got, want := calls[0].Query.Get("$search"), readQuote+"Alice"+readQuote; got != want {
		t.Errorf("$search = %q, want %q (the embedded quote removed)", got, want)
	}

	// The limit is honoured on the first page.
	limited, err := c.ListPeople(ctx, "", 2)
	if err != nil {
		t.Fatalf("ListPeople(limit): %v", err)
	}
	if len(limited) != 2 {
		t.Errorf("people = %d, want the limit of 2", len(limited))
	}
}

// ---------------------------------------------------------------------------
// files.go
// ---------------------------------------------------------------------------

// TestGetFilesFolder covers GET /teams/{team-id}/channels/{channel-id}/filesFolder
// (refs/graph/api-reference/v1.0/api/channel-get-filesfolder.md:34) and DriveID,
// which reads parentReference.driveId — the documented way to address the
// folder's drive afterwards
// (refs/graph/api-reference/v1.0/resources/driveitem.md:104).
func TestGetFilesFolder(t *testing.T) {
	srv, c := readSetup(t)

	folder, err := c.GetFilesFolder(context.Background(), readTeamID, readChannelID)
	if err != nil {
		t.Fatalf("GetFilesFolder: %v", err)
	}
	if folder.ID != readFilesFolder {
		t.Errorf("folder = %+v", folder)
	}
	if !folder.IsFolder() {
		t.Error("the filesFolder does not report the folder facet")
	}
	if got := folder.DriveID(); got != readDriveID {
		t.Errorf("DriveID = %q, want %q", got, readDriveID)
	}
	if empty := (DriveItem{}); empty.DriveID() != "" {
		t.Errorf("DriveID of an item without parentReference = %q, want empty", empty.DriveID())
	}
	readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/filesFolder")
}

// TestGetDriveItemSelectsWebDavURL covers GET /drives/{drive-id}/items/{item-id}
// (refs/graph/api-reference/v1.0/api/driveitem-get.md:48) and the $select the
// plan prescribes for channel file attachments: "Prefer the documented
// webDavUrl (with $select=webDavUrl)" (PLAN.md:246;
// refs/graph/api-reference/v1.0/resources/driveitem.md:116).
func TestGetDriveItemSelectsWebDavURL(t *testing.T) {
	srv, c := readSetup(t)
	ctx := context.Background()

	item, err := c.GetDriveItem(ctx, readDriveID, "file-1", true)
	if err != nil {
		t.Fatalf("GetDriveItem: %v", err)
	}
	if item.ID != "file-1" || item.Name != "spec.pdf" {
		t.Errorf("item = %+v", item)
	}
	if item.MimeType() != "application/pdf" {
		t.Errorf("mimeType = %q", item.MimeType())
	}
	if item.Size != int64(len("%PDF-1.7 fake")) {
		t.Errorf("size = %d", item.Size)
	}
	if item.IsFolder() {
		t.Error("a file reports the folder facet")
	}
	if item.ParentReference == nil || item.ParentReference.DriveID != readDriveID {
		t.Errorf("parentReference = %+v", item.ParentReference)
	}
	calls := readCalls(t, srv, http.MethodGet, "/drives/drive-t-eng/items/file-1")
	selectParam := calls[0].Query.Get("$select")
	if !strings.Contains(selectParam, "webDavUrl") {
		t.Errorf("$select = %q, want it to ask for webDavUrl", selectParam)
	}

	srv.ResetRequests()
	if _, err := c.GetDriveItem(ctx, readDriveID, "file-1", false); err != nil {
		t.Fatalf("GetDriveItem(plain): %v", err)
	}
	calls = readCalls(t, srv, http.MethodGet, "/drives/drive-t-eng/items/file-1")
	if len(calls[0].Query) != 0 {
		t.Errorf("query = %v, want none without the webDavUrl request", calls[0].Query)
	}
}

// TestListDriveItemChildren covers GET /drives/{drive-id}/items/{item-id}/children
// (refs/graph/api-reference/v1.0/api/driveitem-list-children.md:44). The
// wrapper sends no $top, so the documented default page size (200) applies and
// the service pages with @odata.nextLink
// (refs/graph/api-reference/v1.0/api/driveitem-list-children.md:172-174).
func TestListDriveItemChildren(t *testing.T) {
	srv, c := readSetup(t)
	ctx := context.Background()

	items, err := c.ListDriveItemChildren(ctx, readDriveID, readFilesFolder, 0)
	if err != nil {
		t.Fatalf("ListDriveItemChildren: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v, want 2", items)
	}
	if items[0].ID != "file-1" || items[1].ID != "file-2" {
		t.Errorf("items = %+v, want seed order", items)
	}
	if items[0].IsFolder() || items[1].IsFolder() {
		t.Error("a folder child reports the folder facet")
	}
	calls := readCalls(t, srv, http.MethodGet, "/drives/drive-t-eng/items/folder-c-general/children")
	if len(calls) != 1 || len(calls[0].Query) != 0 {
		t.Errorf("requests = %d with query %v, want one request and no query", len(calls), calls[0].Query)
	}

	limited, err := c.ListDriveItemChildren(ctx, readDriveID, readFilesFolder, 1)
	if err != nil {
		t.Fatalf("ListDriveItemChildren(limit): %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("items = %d, want the limit of 1", len(limited))
	}
}

// TestListChannelFiles walks the two documented round trips: the channel's
// filesFolder and then its children.
func TestListChannelFiles(t *testing.T) {
	srv, c := readSetup(t)

	folder, items, err := c.ListChannelFiles(context.Background(), readTeamID, readChannelID)
	if err != nil {
		t.Fatalf("ListChannelFiles: %v", err)
	}
	if folder.ID != readFilesFolder {
		t.Errorf("folder = %+v", folder)
	}
	if len(items) != 2 || items[0].Name != "spec.pdf" {
		t.Errorf("items = %+v", items)
	}
	readCalls(t, srv, http.MethodGet, "/teams/t-eng/channels/c-general/filesFolder")
	readCalls(t, srv, http.MethodGet, "/drives/drive-t-eng/items/folder-c-general/children")
	if n := len(srv.Requests()); n != 2 {
		t.Errorf("requests = %d, want the two round trips", n)
	}
}

// TestDownloadDriveItemContent checks the documented 302 to a pre-authenticated
// download URL, which needs no Authorization header and which an HTTP client
// follows automatically (refs/graph/api-reference/v1.0/api/driveitem-get-content.md:95-100).
// The call runs with the Layer 6 hook installed: fakegraph exempts the redirect
// it answers and the fake's own pre-authenticated URL, because the description
// models neither (internal/testing/fakegraph/contract.go).
func TestDownloadDriveItemContent(t *testing.T) {
	srv := readServer(t, readModel())
	c := readClient(t, srv)

	dl, err := c.DownloadDriveItemContent(context.Background(), readDriveID, "file-1")
	if err != nil {
		t.Fatalf("DownloadDriveItemContent: %v", err)
	}
	if string(dl.Bytes) != "%PDF-1.7 fake" {
		t.Errorf("bytes = %q", dl.Bytes)
	}
	if dl.ContentType != "application/pdf" {
		t.Errorf("contentType = %q", dl.ContentType)
	}
	// The fake's pre-authenticated URL serves the bytes without a
	// Content-Disposition header, so the original name is empty here; the item's
	// name is available from GetDriveItem when a caller needs it, and the parser
	// itself is table-tested below.
	if dl.Name != "" {
		t.Errorf("name = %q, want empty when the service sends no Content-Disposition", dl.Name)
	}
	readCalls(t, srv, http.MethodGet, "/drives/drive-t-eng/items/file-1/content")
	if n := len(srv.Requests()); n != 2 {
		t.Errorf("requests = %d, want the /content call plus the redirect it followed", n)
	}

	// A folder has no primary stream: "Only driveItem objects with the file
	// property can be downloaded" (driveitem-get-content.md:14).
	if _, err := c.DownloadDriveItemContent(context.Background(), readDriveID, readFilesFolder); !IsBadRequest(err) {
		t.Errorf("folder download = %v, want the documented 400", err)
	}
}

// TestFilenameFromDisposition covers the Content-Disposition parsing behind the
// download path. The refs do not document the header — the download page only
// promises a 302 to a pre-authenticated URL
// (refs/graph/api-reference/v1.0/api/driveitem-get-content.md:95-100) — so the
// forms asserted here are the two HTTP ones: filename="..." and the RFC 5987
// filename*=UTF-8”... form.
func TestFilenameFromDisposition(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   string
	}{
		{"no header", "", ""},
		{"a quoted filename", "attachment; filename=" + readQuote + "spec.pdf" + readQuote, "spec.pdf"},
		{"an unquoted filename", "attachment; filename=spec.pdf", "spec.pdf"},
		{"a name with a space", "attachment; filename=" + readQuote + "release notes.txt" + readQuote, "release notes.txt"},
		{"the RFC 5987 form", "attachment; filename*=UTF-8''r%C3%A9sum%C3%A9.pdf", "résumé.pdf"},
		{"the extended form wins when it comes first", "attachment; filename*=UTF-8''a.pdf; filename=" + readQuote + "b.pdf" + readQuote, "a.pdf"},
		{"the extended form without a charset", "attachment; filename*=report.pdf", "report.pdf"},
		{"a header without a filename", "attachment", ""},
		{"a malformed extended value", "attachment; filename*=UTF-8''%zz.pdf", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := filenameFromDisposition(tc.header); got != tc.want {
				t.Errorf("filenameFromDisposition(%q) = %q, want %q", tc.header, got, tc.want)
			}
		})
	}
}
