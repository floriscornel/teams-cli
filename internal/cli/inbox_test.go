package cli

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// `teams unread` must be an inbox, not an archaeology dig: a chat whose read
// state Teams never set is "unread" forever, and the listing behind it pages the
// whole tenant (with $expand=members on every page) if nothing bounds it. The
// window bounds it, and because the listing is ordered by last activity the client
// can stop paging as soon as it walks past the window.

// TestUnreadStopsAtTheWindow seeds recent chats after a page of stale ones and
// asserts both halves of that: one request, and only the recent chats.
func TestUnreadStopsAtTheWindow(t *testing.T) {
	h, srv := newInboxHarness(t, 5, 200)

	out := h.mustRun("unread", "--chats")
	if got := len(srv.RequestsFor(http.MethodGet, "/me/chats")); got != 1 {
		t.Errorf("requests = %d, want 1: a 24h window ends the listing", got)
	}
	if !strings.Contains(out, "recent 0") {
		t.Errorf("output does not list the recent chats: %q", out)
	}
	if strings.Contains(out, "stale 0") {
		t.Errorf("a chat outside the window was listed: %q", out)
	}

	srv.ResetRequests()
	all := h.mustRun("unread", "--chats", "--all")
	if got := len(srv.RequestsFor(http.MethodGet, "/me/chats")); got < 2 {
		t.Errorf("requests = %d, want --all to page the whole listing", got)
	}
	if !strings.Contains(all, "stale 0") {
		t.Errorf("--all does not list the stale chats: %q", all)
	}
}

// TestUnreadJSONCarriesTheWindow documents the window in --json, so a script can
// tell what "unread" meant for the run.
func TestUnreadJSONCarriesTheWindow(t *testing.T) {
	h, _ := newInboxHarness(t, 2, 3)

	out := h.mustRun("unread", "--chats", "--json")
	if !strings.Contains(out, "\"since\"") {
		t.Errorf("--json has no window: %q", out)
	}
	out = h.mustRun("unread", "--chats", "--all", "--json")
	if strings.Contains(out, "\"since\"") {
		t.Errorf("--all still reports a window: %q", out)
	}
}

// newInboxHarness seeds never-read chats: recent ones first, then stale ones, which
// is the order the API returns them in (newest activity first).
func newInboxHarness(t *testing.T, recent, stale int) (*harness, *fakegraph.Server) {
	t.Helper()
	now := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
	model := fakegraph.Model{Me: "u-me", Users: []fakegraph.User{
		{ID: "u-me", DisplayName: "Alice Example", UserPrincipalName: "alice@example.com"},
		{ID: "u-bob", DisplayName: "Bob Builder", UserPrincipalName: "bob@example.com"},
	}}
	add := func(index int, topic string, at time.Time) {
		model.Chats = append(model.Chats, fakegraph.Chat{
			ID:      "19:" + strings.ReplaceAll(topic, " ", "-") + "@thread.v2",
			Topic:   topic,
			Members: []fakegraph.Member{{UserID: "u-me"}, {UserID: "u-bob"}},
			// No LastRead: the read watermark is never set, so every one of these
			// chats counts as unread.
			Messages: []fakegraph.Message{{
				ID: "m" + strconv.Itoa(index), AuthorID: "u-bob", Created: at, Body: "<p>hello</p>",
			}},
		})
	}
	for i := range recent {
		add(i, "recent "+strconv.Itoa(i), now.Add(-time.Duration(i+1)*time.Minute))
	}
	for i := range stale {
		add(1000+i, "stale "+strconv.Itoa(i), now.AddDate(0, 0, -40-i))
	}
	srv := fakegraph.New(t, fakegraph.Options{Model: model})

	h := newHarness(t)
	h.graphURL = srv.URL()
	h.applyHooks()
	h.useAccessToken(t, "Chat.Read Chat.ReadBasic Chat.ReadWrite")
	return h, srv
}
