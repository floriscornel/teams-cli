package cli

import (
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// `chat list` must cost one bounded request by default. It used to page the
// whole tenant with $expand=members on every page, which on a busy tenant takes
// long enough that a user cannot tell a slow listing from a hung one.

func TestChatListFetchesOnePageByDefault(t *testing.T) {
	h, srv := newChatHarness(t, 60)

	h.mustRun("chat", "list")
	if got := len(srv.RequestsFor(http.MethodGet, "/me/chats")); got != 1 {
		t.Errorf("requests = %d, want 1: the default is one page", got)
	}

	srv.ResetRequests()
	h.mustRun("chat", "list", "--all")
	if got := len(srv.RequestsFor(http.MethodGet, "/me/chats")); got < 2 {
		t.Errorf("requests = %d, want several with --all", got)
	}

	srv.ResetRequests()
	h.mustRun("chat", "list", "--limit", "1")
	if got := len(srv.RequestsFor(http.MethodGet, "/me/chats")); got != 1 {
		t.Errorf("requests = %d, want 1 with --limit 1", got)
	}
}

// A listing that has to look at every chat still does, because an unread chat
// older than the newest page is exactly what `unread` is for.
func TestUnreadPagesEveryChat(t *testing.T) {
	h, srv := newChatHarness(t, 60)

	h.mustRun("unread", "--chats")
	if got := len(srv.RequestsFor(http.MethodGet, "/me/chats")); got < 2 {
		t.Errorf("requests = %d, want the whole listing", got)
	}
}

// newChatHarness seeds n chats and points the harness at the fake Graph.
func newChatHarness(t *testing.T, n int) (*harness, *fakegraph.Server) {
	t.Helper()
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	model := fakegraph.Model{Me: "u-me", Users: []fakegraph.User{
		{ID: "u-me", DisplayName: "Alice Example", UserPrincipalName: "alice@example.com"},
		{ID: "u-bob", DisplayName: "Bob Builder", UserPrincipalName: "bob@example.com"},
	}}
	for i := range n {
		topic := "Chat " + strconv.Itoa(i)
		model.Chats = append(model.Chats, fakegraph.Chat{
			ID:      "19:chat" + strconv.Itoa(i) + "@thread.v2",
			Topic:   topic,
			Members: []fakegraph.Member{{UserID: "u-me"}, {UserID: "u-bob"}},
			Messages: []fakegraph.Message{{
				ID: "m" + strconv.Itoa(i), AuthorID: "u-bob", Created: now.Add(-time.Duration(i) * time.Minute),
				Body: "<p>hello</p>",
			}},
		})
	}
	srv := fakegraph.New(t, fakegraph.Options{Model: model})

	h := newHarness(t)
	h.graphURL = srv.URL()
	h.applyHooks()
	h.useAccessToken(t, "Chat.Read Chat.ReadBasic Chat.ReadWrite")
	return h, srv
}
