package fakegraph

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers the documented page sizes, the caps that produce a 400, and
// the @odata.nextLink walk — including a walk driven by the real
// internal/graph paging iterator, so the fake is proven against the client the
// CLI actually uses.

// bigModel is a seed with enough objects to exercise paging: 12 channel roots,
// a root with 205 replies, 7 users and 8 team members.
func bigModel() Model {
	replies := make([]Message, 0, 205)
	for i := 0; i < 205; i++ {
		replies = append(replies, Message{
			ID:       fmt.Sprintf("big-r%03d", i),
			AuthorID: "u-0",
			Body:     fmt.Sprintf("<p>reply %d</p>", i),
			Created:  tJan1.Add(time.Duration(i) * time.Minute),
		})
	}
	roots := make([]Message, 0, 12)
	for i := 0; i < 11; i++ {
		roots = append(roots, Message{
			ID:       fmt.Sprintf("m-%02d", i),
			AuthorID: "u-me",
			Body:     fmt.Sprintf("<p>root %d</p>", i),
			Created:  tJan1.Add(time.Duration(i) * time.Hour),
		})
	}
	roots = append(roots, Message{ID: "m-big", AuthorID: "u-0", Body: "<p>busy thread</p>", Created: tJan2, Replies: replies})

	users := []User{{ID: "u-me", DisplayName: "Me", UserPrincipalName: "me@contoso.example"}}
	teamMembers := members("u-me")
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("u-%d", i)
		users = append(users, User{ID: id, DisplayName: fmt.Sprintf("User %d", i), UserPrincipalName: id + "@contoso.example"})
		teamMembers = append(teamMembers, Member{UserID: id, Roles: []string{"member"}})
	}
	chats := make([]Chat, 0, 4)
	for i := 0; i < 4; i++ {
		chats = append(chats, Chat{
			ID:       fmt.Sprintf("chat-%d", i),
			ChatType: ChatTypeGroup,
			Members:  members("u-me", "u-0"),
			Messages: []Message{{ID: fmt.Sprintf("gm-%d", i), AuthorID: "u-0", Body: "<p>hi</p>", Created: tJan1}},
		})
	}
	return Model{
		Me:    "u-me",
		Users: users,
		Teams: []Team{{
			ID: "t1", DisplayName: "Team One", Members: teamMembers,
			Channels: []Channel{{ID: "c1", DisplayName: "General", Messages: roots}},
		}},
		Chats: chats,
	}
}

func newBigServer(t *testing.T) *Server {
	t.Helper()
	return New(t, Options{Model: bigModel(), Clock: newFakeClock()})
}

func TestChannelMessagePaging(t *testing.T) {
	srv := newBigServer(t)
	c := newClient(t, srv)

	t.Run("top over the documented cap is a 400", func(t *testing.T) {
		_, err := graph.GetPage[map[string]any](context.Background(), c,
			"/teams/t1/channels/c1/messages", url.Values{"$top": {"51"}})
		if got := errStatus(err); got != 400 {
			t.Fatalf("status = %d (err %v), want 400", got, err)
		}
		var apiErr *graph.APIError
		if !asError(err, &apiErr) {
			t.Fatalf("error %v is not an APIError", err)
		}
		if !strings.Contains(apiErr.Message, "50") {
			t.Fatalf("message = %q, want it to name the limit", apiErr.Message)
		}
	})

	t.Run("pages with a next link", func(t *testing.T) {
		page := mustPage[map[string]any](t, c, "/teams/t1/channels/c1/messages", url.Values{"$top": {"5"}})
		if len(page.Value) != 5 {
			t.Fatalf("page size = %d, want 5", len(page.Value))
		}
		if page.NextLink == "" {
			t.Fatal("no @odata.nextLink on a collection with more items")
		}
		second := mustPage[map[string]any](t, c, page.NextLink, nil)
		if len(second.Value) != 5 {
			t.Fatalf("second page size = %d, want 5", len(second.Value))
		}
		if second.NextLink == "" {
			t.Fatal("no next link on the second page (12 roots, 5 per page)")
		}
		third := mustPage[map[string]any](t, c, second.NextLink, nil)
		if len(third.Value) != 2 {
			t.Fatalf("third page size = %d, want the remaining 2", len(third.Value))
		}
		if third.NextLink != "" {
			t.Fatalf("unexpected next link on the last page: %q", third.NextLink)
		}
	})

	t.Run("filter is rejected, not ignored", func(t *testing.T) {
		_, err := graph.GetPage[map[string]any](context.Background(), c,
			"/teams/t1/channels/c1/messages", url.Values{"$filter": {"lastModifiedDateTime gt 2026-01-01T00:00:00Z"}})
		var apiErr *graph.APIError
		if !asError(err, &apiErr) || apiErr.Status != 400 {
			t.Fatalf("err = %v, want a 400 APIError", err)
		}
		if apiErr.Message != "Parameter 'Filter' not supported" {
			t.Fatalf("message = %q, want the spike-observed wording", apiErr.Message)
		}
	})

	t.Run("expand replies inlines 200 and links the rest", func(t *testing.T) {
		page := mustPage[messageWire](t, c, "/teams/t1/channels/c1/messages",
			url.Values{"$top": {"12"}, "$expand": {"replies"}})
		var big *messageWire
		for i := range page.Value {
			if page.Value[i].ID == "m-big" {
				big = &page.Value[i]
			}
		}
		if big == nil {
			t.Fatal("m-big not in the page")
		}
		if len(big.Replies) != RepliesExpandedMax {
			t.Fatalf("inlined replies = %d, want %d", len(big.Replies), RepliesExpandedMax)
		}
		if big.RepliesNextLink == "" {
			t.Fatal("replies@odata.nextLink missing for a thread with more than 200 replies")
		}
		rest := mustPage[messageWire](t, c, big.RepliesNextLink, nil)
		if len(rest.Value) != 5 {
			t.Fatalf("rest of the thread = %d replies, want 5", len(rest.Value))
		}
		if rest.NextLink != "" {
			t.Fatalf("unexpected further replies link: %q", rest.NextLink)
		}
	})

	t.Run("replies list caps at 50", func(t *testing.T) {
		if _, err := graph.GetPage[map[string]any](context.Background(), c,
			"/teams/t1/channels/c1/messages/m-big/replies", url.Values{"$top": {"51"}}); errStatus(err) != 400 {
			t.Fatalf("err = %v, want 400", err)
		}
		page := mustPage[map[string]any](t, c, "/teams/t1/channels/c1/messages/m-big/replies", url.Values{"$top": {"50"}})
		if len(page.Value) != 50 || page.NextLink == "" {
			t.Fatalf("replies page = %d items, next=%q", len(page.Value), page.NextLink)
		}
	})
}

func TestMemberPaging(t *testing.T) {
	srv := newBigServer(t)
	c := newClient(t, srv)

	// The spike saw team members page with @odata.nextLink at $top=5, where the
	// docs imply no paging (docs/spike/phase1.md:50).
	page := mustPage[conversationMemberWire](t, c, "/teams/t1/members", url.Values{"$top": {"5"}})
	if len(page.Value) != 5 {
		t.Fatalf("page size = %d, want 5", len(page.Value))
	}
	if page.NextLink == "" {
		t.Fatal("member list did not page; the spike saw a next link at $top=5")
	}
	rest := mustPage[conversationMemberWire](t, c, page.NextLink, nil)
	if len(rest.Value) != 3 {
		t.Fatalf("second member page = %d, want 3", len(rest.Value))
	}
	if rest.NextLink != "" {
		t.Fatalf("unexpected next link: %q", rest.NextLink)
	}
	if rest.Value[0].UserID == "" || rest.Value[0].ODataType != "#microsoft.graph.aadUserConversationMember" {
		t.Fatalf("member shape = %+v", rest.Value[0])
	}

	// Default 100, max 999, so 8 members fit in one page with no link.
	all := mustPage[conversationMemberWire](t, c, "/teams/t1/members", nil)
	if len(all.Value) != 8 || all.NextLink != "" {
		t.Fatalf("default page = %d members, next=%q", len(all.Value), all.NextLink)
	}
	if _, err := graph.GetPage[map[string]any](context.Background(), c, "/teams/t1/members", url.Values{"$top": {"1000"}}); errStatus(err) != 400 {
		t.Fatalf("$top=1000 err = %v, want 400", err)
	}

	// Channel members use the same window.
	chPage := mustPage[conversationMemberWire](t, c, "/teams/t1/channels/c1/members", url.Values{"$top": {"3"}})
	if len(chPage.Value) != 3 || chPage.NextLink == "" {
		t.Fatalf("channel member page = %d, next=%q", len(chPage.Value), chPage.NextLink)
	}
}

func TestChatPaging(t *testing.T) {
	srv := newBigServer(t)
	c := newClient(t, srv)

	if _, err := graph.GetPage[map[string]any](context.Background(), c, "/me/chats", url.Values{"$top": {"51"}}); errStatus(err) != 400 {
		t.Fatalf("$top=51 err = %v, want 400", err)
	}
	page := mustPage[chatWire](t, c, "/me/chats", url.Values{"$top": {"2"}})
	if len(page.Value) != 2 || page.NextLink == "" {
		t.Fatalf("chat page = %d, next=%q", len(page.Value), page.NextLink)
	}
}

func TestUserAndJoinedTeamPaging(t *testing.T) {
	srv := newBigServer(t)
	c := newClient(t, srv)

	page := mustPage[userWire](t, c, "/users", url.Values{"$top": {"3"}})
	if len(page.Value) != 3 || page.NextLink == "" {
		t.Fatalf("user page = %d, next=%q", len(page.Value), page.NextLink)
	}

	// joinedTeams is one team, so no link.
	teams := mustPage[teamWire](t, c, "/me/joinedTeams", nil)
	if len(teams.Value) != 1 {
		t.Fatalf("joinedTeams = %d, want 1", len(teams.Value))
	}
	if teams.Value[0].ID != "t1" {
		t.Fatalf("team id = %q", teams.Value[0].ID)
	}
}

func TestPagingThroughRealGraphClientIterator(t *testing.T) {
	srv := newBigServer(t)
	c := newClient(t, srv)

	// Walk every root message with the real iterator, which follows
	// @odata.nextLink verbatim.
	var ids []string
	for msg, err := range graph.Items[messageWire](context.Background(), c,
		"/teams/t1/channels/c1/messages", url.Values{"$top": {"5"}}) {
		if err != nil {
			t.Fatalf("iterate: %v", err)
		}
		ids = append(ids, msg.ID)
	}
	if len(ids) != 12 {
		t.Fatalf("iterated %d messages, want 12: %v", len(ids), ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("message %q appeared twice", id)
		}
		seen[id] = true
	}

	// A short --limit walk stops early without fetching the rest.
	count := 0
	for _, err := range graph.Items[messageWire](context.Background(), c,
		"/teams/t1/channels/c1/messages", url.Values{"$top": {"5"}}) {
		if err != nil {
			t.Fatal(err)
		}
		count++
		if count == 6 {
			break
		}
	}
	if count != 6 {
		t.Fatalf("early stop iterated %d, want 6", count)
	}

	// EachPage sees the page boundaries the fake produced.
	pages := 0
	sizes := []int{}
	err := graph.EachPage[messageWire](context.Background(), c, "/teams/t1/channels/c1/messages",
		url.Values{"$top": {"5"}}, func(page graph.Page[messageWire]) (bool, error) {
			pages++
			sizes = append(sizes, len(page.Value))
			return true, nil
		})
	if err != nil {
		t.Fatalf("EachPage: %v", err)
	}
	if pages != 3 || fmt.Sprint(sizes) != "[5 5 2]" {
		t.Fatalf("pages = %d, sizes = %v, want 3 pages of [5 5 2]", pages, sizes)
	}
}

func TestChannelRootOrderingFollowsChainActivity(t *testing.T) {
	// A root whose own lastModifiedDateTime is old but whose newest reply is
	// recent must sort above a root with a newer lastModifiedDateTime: the
	// spike confirmed the chain order (docs/spike/phase1.md:55).
	model := Model{
		Me:    "u-me",
		Users: []User{{ID: "u-me", DisplayName: "Me"}, {ID: "u-alice", DisplayName: "Alice"}},
		Teams: []Team{{ID: "t", DisplayName: "T", Channels: []Channel{{ID: "c", DisplayName: "C", Messages: []Message{
			{ID: "old-root-new-reply", AuthorID: "u-me", Body: "a", Created: tJan1, Modified: tJan1,
				Replies: []Message{{ID: "r1", AuthorID: "u-alice", Body: "b", Created: tJan3}}},
			{ID: "newer-root", AuthorID: "u-me", Body: "c", Created: tJan2, Modified: tJan2},
		}}}}},
	}
	srv := New(t, Options{Model: model, Clock: newFakeClock()})
	c := newClient(t, srv)

	page := mustPage[messageWire](t, c, "/teams/t/channels/c/messages", nil)
	if len(page.Value) != 2 {
		t.Fatalf("got %d messages", len(page.Value))
	}
	if page.Value[0].ID != "old-root-new-reply" {
		t.Fatalf("first root = %q; the thread with the newest reply must come first", page.Value[0].ID)
	}
}

func TestSystemEventMessageNeedsPreferHeader(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	without := mustPage[messageWire](t, c, "/teams/t-eng/channels/c-general/messages", nil)
	for _, m := range without.Value {
		if m.ID == "cm-3" && m.MessageType != "unknownFutureValue" {
			t.Fatalf("messageType = %q without the Prefer header, want unknownFutureValue", m.MessageType)
		}
	}
	with := mustPage[messageWire](t, c, "/teams/t-eng/channels/c-general/messages", nil, graph.WithPreferUnknownEnumMembers())
	for _, m := range with.Value {
		if m.ID == "cm-3" && m.MessageType != "systemEventMessage" {
			t.Fatalf("messageType = %q with the Prefer header, want systemEventMessage", m.MessageType)
		}
	}
}

func TestDriveItemPaging(t *testing.T) {
	model := Model{
		Me:    "u-me",
		Users: []User{{ID: "u-me", DisplayName: "Me"}},
		Drives: []Drive{{ID: "d1", Items: func() []DriveItem {
			var items []DriveItem
			for i := 0; i < 5; i++ {
				items = append(items, DriveItem{ID: fmt.Sprintf("f%d", i), Name: fmt.Sprintf("f%d.txt", i), ParentID: "root", Content: []byte("x")})
			}
			return items
		}()}},
	}
	srv := New(t, Options{Model: model, Clock: newFakeClock()})
	c := newClient(t, srv)

	page := mustPage[driveItemWire](t, c, "/drives/d1/items/root/children", url.Values{"$top": {"2"}})
	if len(page.Value) != 2 || page.NextLink == "" {
		t.Fatalf("children page = %d, next=%q", len(page.Value), page.NextLink)
	}
}

func TestParsePageErrors(t *testing.T) {
	cases := []struct {
		query url.Values
		want  string
	}{
		{url.Values{"$top": {"abc"}}, "not an integer"},
		{url.Values{"$top": {"0"}}, "at least 1"},
		{url.Values{"$top": {"51"}}, "maximum allowed"},
		{url.Values{"$skiptoken": {"-1"}}, "non-negative"},
		{url.Values{"$skip": {"x"}}, "non-negative"},
	}
	for _, tc := range cases {
		t.Run(tc.query.Encode(), func(t *testing.T) {
			_, err := parsePage(tc.query, defaultTopMessages, maxTopMessages)
			if err == nil {
				t.Fatal("parsePage accepted an invalid window")
			}
			if !strings.Contains(err.Message, tc.want) {
				t.Fatalf("message = %q, want it to mention %q", err.Message, tc.want)
			}
		})
	}
	if _, err := parsePage(url.Values{}, defaultTopMessages, maxTopMessages); err != nil {
		t.Fatalf("empty query: %v", err)
	}
	if _, err := parsePage(url.Values{"$skiptoken": {"3"}}, defaultTopMessages, maxTopMessages); err != nil {
		t.Fatalf("skiptoken: %v", err)
	}
}
