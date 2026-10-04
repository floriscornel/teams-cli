package graph

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// ChatQuery.Since is what keeps `teams unread` to one round trip: the chat listing
// is ordered by lastMessagePreview/createdDateTime desc
// (refs/graph/api-reference/v1.0/api/chat-list.md:63), so a client that walks past
// the window can stop instead of paging every chat in the tenant.

func TestListChatsStopsAtTheWindow(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	model := fakegraph.Model{
		Me:    "me",
		Users: []fakegraph.User{{ID: "me", DisplayName: "Me"}, {ID: "bob", DisplayName: "Bob"}},
	}
	// Newest first, exactly as the service orders them.
	for i := range 3 {
		model.Chats = append(model.Chats, fakegraph.Chat{
			ID:       "19:recent" + string(rune('a'+i)) + "@thread.v2",
			Members:  []fakegraph.Member{{UserID: "me"}, {UserID: "bob"}},
			Messages: []fakegraph.Message{{ID: "r" + string(rune('a'+i)), AuthorID: "bob", Created: now.Add(-time.Duration(i+1) * time.Hour), Body: "<p>x</p>"}},
		})
	}
	for i := range 300 {
		model.Chats = append(model.Chats, fakegraph.Chat{
			ID:       "19:stale" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + "@thread.v2",
			Members:  []fakegraph.Member{{UserID: "me"}, {UserID: "bob"}},
			Messages: []fakegraph.Message{{ID: "s" + string(rune('a'+i%26)) + string(rune('a'+i/26)), AuthorID: "bob", Created: now.AddDate(0, 0, -30-i), Body: "<p>y</p>"}},
		})
	}
	srv := readServer(t, model)
	c := readClient(t, srv)

	chats, err := c.ListChats(context.Background(), ChatQuery{
		Top:                MaxTopChats,
		LastMessagePreview: true,
		OrderByLastMessage: true,
		Since:              now.Add(-24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 3 {
		t.Fatalf("chats = %d, want the 3 inside the window", len(chats))
	}
	if calls := readCalls(t, srv, http.MethodGet, "/me/chats"); len(calls) != 1 {
		t.Errorf("requests = %d, want 1: the window ends the listing", len(calls))
	}
	for _, chat := range chats {
		if chat.LastActivity().Before(now.Add(-24 * time.Hour)) {
			t.Errorf("chat %s is outside the window", chat.ID)
		}
	}

	// Without a window it pages everything, which is what --all asks for.
	srv.ResetRequests()
	all, err := c.ListChats(context.Background(), ChatQuery{
		Top:                MaxTopChats,
		LastMessagePreview: true,
		OrderByLastMessage: true,
	})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(all) != 303 {
		t.Errorf("chats = %d, want every chat", len(all))
	}
	if calls := readCalls(t, srv, http.MethodGet, "/me/chats"); len(calls) < 2 {
		t.Errorf("requests = %d, want several without a window", len(calls))
	}
}

// TestChatLastActivity pins the timestamp the window compares against.
func TestChatLastActivity(t *testing.T) {
	preview := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 10, 4, 8, 0, 0, 0, time.UTC)
	chat := Chat{LastMessagePreview: &Message{CreatedDateTime: preview}, LastUpdatedDateTime: updated}
	if got := chat.LastActivity(); !got.Equal(preview) {
		t.Errorf("LastActivity = %s, want the preview time %s", got, preview)
	}
	chat = Chat{LastUpdatedDateTime: updated}
	if got := chat.LastActivity(); !got.Equal(updated) {
		t.Errorf("LastActivity = %s, want the chat's own timestamp %s", got, updated)
	}
	if got := (Chat{}).LastActivity(); !got.IsZero() {
		t.Errorf("LastActivity of an empty chat = %s, want the zero time", got)
	}
}
