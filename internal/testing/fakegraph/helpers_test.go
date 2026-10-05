package fakegraph

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file holds the shared fixtures. Every timestamp is a constant, so a
// failing assertion can be read straight from the source.

// testNow is the frozen clock the tests inject. It is deliberately later than
// every seeded timestamp, so a message created through the API is the newest
// one in its container.
var testNow = time.Date(2026, 1, 4, 15, 4, 5, 0, time.UTC)

// Base timestamps for the seeded messages.
var (
	tJan1 = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	tJan2 = time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	tJan3 = time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)
	tJan4 = time.Date(2026, 1, 4, 9, 0, 0, 0, time.UTC)
)

func members(ids ...string) []Member {
	out := make([]Member, 0, len(ids))
	for _, id := range ids {
		out = append(out, Member{UserID: id, Roles: []string{"owner"}})
	}
	return out
}

// testModel is the shared seed: two teams' worth of channels, a one-on-one and
// a group chat, a self chat, enough users to page and one drive with a file.
func testModel() Model {
	users := []User{
		{ID: "u-me", DisplayName: "Me Myself", UserPrincipalName: "me@contoso.example", Mail: "me@contoso.example", Relevance: 1},
		{ID: "u-alice", DisplayName: "Alice Example", UserPrincipalName: "alice@contoso.example", Mail: "alice@contoso.example", JobTitle: "Engineer", Relevance: 9},
		{ID: "u-bob", DisplayName: "Bob Builder", UserPrincipalName: "bob@contoso.example", Mail: "bob@contoso.example", JobTitle: "Designer", Relevance: 5},
		{ID: "u-carol", DisplayName: "Carol Jones", UserPrincipalName: "carol@contoso.example", Mail: "carol@contoso.example"},
		// u-dave exists but has no Exchange Online mailbox, so every calendar
		// route that needs one answers 404 MailboxNotEnabledForRESTAPI (on
		// /users/{id}/calendarView) or a 5016 schedule error (on getSchedule).
		{ID: "u-dave", DisplayName: "Dave Kim", UserPrincipalName: "dave@contoso.example", Mail: "dave@contoso.example", NoMailbox: true},
		{ID: "u-erin", DisplayName: "Erin Lopez", UserPrincipalName: "erin@contoso.example", Mail: "erin@contoso.example"},
		{ID: "u-frank", DisplayName: "Frank O'Neil", UserPrincipalName: "frank@contoso.example", Mail: "frank@contoso.example"},
	}
	teamMembers := members("u-me", "u-alice", "u-bob", "u-carol", "u-dave", "u-erin")
	return Model{
		Me:    "u-me",
		Users: users,
		Teams: []Team{{
			ID:          "t-eng",
			DisplayName: "Engineering",
			Created:     tJan1,
			Members:     teamMembers,
			Channels: []Channel{
				{
					ID:          "c-general",
					DisplayName: "General",
					Created:     tJan1,
					Messages: []Message{
						{
							ID: "cm-1", AuthorID: "u-alice", Body: "<p>hello world</p>", Created: tJan1,
							Mentions: []Mention{{ID: 0, Text: "Me Myself", UserID: "u-me", UserDisplayName: "Me Myself"}},
						},
						{
							ID: "cm-2", AuthorID: "u-bob", Body: "<p>deploy is done</p>", Created: tJan2, Subject: "Deploy",
							Replies: []Message{
								{ID: "cr-1", AuthorID: "u-me", Body: "<p>thanks!</p>", Created: tJan2},
								{ID: "cr-2", AuthorID: "u-carol", Body: "<p>nice</p>", Created: tJan3},
							},
						},
						{ID: "cm-3", AuthorID: "u-me", Body: "<p>morning</p>", Created: tJan3, MessageType: "systemEventMessage"},
					},
				},
				{ID: "c-private", DisplayName: "Private Ops", MembershipType: ChannelPrivate, Created: tJan2, Members: members("u-me", "u-alice")},
				{ID: "c-shared", DisplayName: "Shared With Partners", MembershipType: ChannelShared, Created: tJan3, Members: members("u-me", "u-alice", "u-bob")},
			},
		}},
		Chats: []Chat{
			{
				ID: "chat-1on1", ChatType: ChatTypeOneOnOne, Created: tJan1, LastRead: tJan1,
				Members: members("u-me", "u-alice"),
				// gm-hc carries an inline image, so the hostedContents routes
				// have something to read.
				Messages: append(chatMessages("1on1", 5), Message{
					ID: "gm-hc", AuthorID: "u-me", Created: tJan2.Add(20 * time.Hour),
					Body:           `<p>inline image</p><img src="../hostedContents/hc-1/$value">`,
					HostedContents: []HostedContent{{ID: "hc-1", ContentType: "image/png", Content: []byte("png-bytes"), Created: tJan2}},
				}),
			},
			{
				ID: "chat-group", ChatType: ChatTypeGroup, Topic: "Release train", Created: tJan2,
				Members:  members("u-me", "u-alice", "u-bob"),
				Messages: chatMessages("group", 3),
			},
			{
				ID: SelfChatID, ChatType: ChatTypeOneOnOne, Created: tJan3,
				Members:  members("u-me"),
				Messages: []Message{{ID: "note-1", AuthorID: "u-me", Body: "<p>note to self</p>", Created: tJan3}},
			},
		},
		Drives: []Drive{{
			ID: "drive-t-eng",
			Items: []DriveItem{
				{ID: "file-1", Name: "spec.pdf", ParentID: "folder-c-general", Content: []byte("%PDF-1.7 fake"), ContentType: "application/pdf", Created: tJan1, Modified: tJan2},
				{ID: "file-2", Name: "notes.txt", ParentID: "folder-c-general", Content: []byte("hello"), ContentType: "text/plain", Created: tJan2, Modified: tJan3},
			},
		}},
		// The calendar seed covers the shapes Phase 6 renders: a Teams meeting,
		// a plain timed event, an all-day event, a cancelled one, a recurring
		// occurrence, and one event in a calendar shared with the signed-in
		// user (plus one shared as free/busy only and one whose mailbox is
		// missing). The events sit in the tJan1..tJan5 window the frozen clock
		// and the other fixtures already use.
		CalendarEvents: []CalendarEvent{
			{
				ID: "ev-standup", Subject: "Daily standup", Start: tJan2, End: tJan2.Add(30 * time.Minute),
				ShowAs: "busy", Teams: true, IsOrganizer: true, Location: "Teams",
			},
			{
				ID: "ev-review", Subject: "Design review", Start: tJan3.Add(2 * time.Hour), End: tJan3.Add(3 * time.Hour),
				ShowAs: "tentative", OrganizerName: "Alice Example", IsOrganizer: false, Location: "Room 1",
			},
			{
				ID: "ev-holiday", Subject: "Company holiday", Kind: CalendarEventAllDay, Start: tJan2, Days: 1,
				ShowAs: "oof", IsOrganizer: true,
			},
			{
				ID: "ev-cancelled", Subject: "Cancelled sync", Start: tJan2.Add(5 * time.Hour), End: tJan2.Add(6 * time.Hour),
				IsCancelled: true, IsOrganizer: true,
			},
			{
				ID: "ev-occurrence", Subject: "Weekly sync", Start: tJan4, End: tJan4.Add(time.Hour),
				Type: "occurrence", SeriesMasterID: "ev-series", IsOrganizer: true,
			},
			{
				ID: "ev-alice-1", OwnerID: "u-alice", Subject: "Alice 1:1", Start: tJan2.Add(time.Hour), End: tJan2.Add(time.Hour + 30*time.Minute),
				OrganizerName: "Alice Example", IsOrganizer: true,
			},
			{
				ID: "ev-bob-busy", OwnerID: "u-bob", Subject: "Bob planning", Start: tJan2.Add(2 * time.Hour), End: tJan2.Add(3 * time.Hour),
				ShowAs: "busy", IsOrganizer: true,
			},
			{
				ID: "ev-erin-busy", OwnerID: "u-erin", Subject: "Erin interview", Start: tJan2.Add(4 * time.Hour), End: tJan2.Add(5 * time.Hour),
				ShowAs: "busy", IsOrganizer: true,
			},
		},
		CalendarAccess: map[string]string{
			"u-alice": CalendarAccessRead,
			"u-bob":   CalendarAccessFreeBusy,
			// u-erin has an event but no shared-calendar entry at all: the 403
			// ErrorAccessDenied fallback.
		},
	}
}

// chatMessages builds n deterministic chat messages in a non-monotonic
// timestamp order, so the default (deliberately unsorted) listing and the
// client-side sort both have something to do.
func chatMessages(prefix string, n int) []Message {
	out := make([]Message, 0, n)
	for i := 0; i < n; i++ {
		created := tJan1.Add(time.Duration(i) * time.Hour)
		if i%2 == 0 {
			created = tJan3.Add(-time.Duration(i) * time.Hour)
		}
		body := fmt.Sprintf("<p>%s message %d</p>", prefix, i)
		if i == 0 {
			body = fmt.Sprintf("<p>%s message %d hello</p>", prefix, i)
		}
		out = append(out, Message{ID: fmt.Sprintf("%s-m%d", prefix, i), AuthorID: authorFor(i), Body: body, Created: created})
	}
	return out
}

func authorFor(i int) string {
	authors := []string{"u-alice", "u-me", "u-bob"}
	return authors[i%len(authors)]
}

// newTestServer starts a server seeded with testModel and a frozen clock.
func newTestServer(t *testing.T, mutate ...func(*Options)) *Server {
	t.Helper()
	opts := Options{
		Model: testModel(),
		Clock: clock.NewFake(testNow),
	}
	for _, m := range mutate {
		m(&opts)
	}
	return New(t, opts)
}

// newClient builds the real internal/graph client against the fake. Retries are
// off and the sleeper is a no-op, so a test never waits.
func newClient(t *testing.T, srv *Server, mutate ...func(*graph.Options)) *graph.Client {
	t.Helper()
	opts := graph.Options{
		BaseURL:    srv.URL(),
		HTTPClient: srv.Client(),
		Token:      graph.TokenSourceFunc(func(context.Context) (string, error) { return "fakegraph-token", nil }),
		Sleeper:    func(context.Context, time.Duration) error { return nil },
		MaxRetries: -1,
	}
	for _, m := range mutate {
		m(&opts)
	}
	client, err := graph.New(opts)
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}
	return client
}

// mustGet issues a GET through the real client and returns the raw response.
func mustGet(t *testing.T, c *graph.Client, path string, query url.Values) *graph.Response {
	t.Helper()
	resp, err := c.Do(context.Background(), graph.Request{Method: "GET", Path: path, Query: query})
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// mustPage decodes a GET into a page of one JSON shape.
func mustPage[T any](t *testing.T, c *graph.Client, path string, query url.Values, header ...graph.RequestOption) graph.Page[T] {
	t.Helper()
	page, err := graph.GetPage[T](context.Background(), c, path, query, header...)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return page
}

// newFakeClock returns the frozen clock the fixtures use.
func newFakeClock() clock.Clock { return clock.NewFake(testNow) }

// asError is errors.As for graph.APIError, kept short for the tests.
func asError(err error, target **graph.APIError) bool { return errors.As(err, target) }

// errStatus returns the HTTP status of a graph APIError, or -1.
func errStatus(err error) int {
	var apiErr *graph.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status
	}
	return -1
}
