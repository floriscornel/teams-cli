package fakegraph

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers the chat message query matrix: the documented
// $orderby/$filter rules, the spike's corrections to them, and the deliberately
// unsorted default. The expected results come from
// refs/graph/api-reference/v1.0/api/chat-list-messages.md:32-34 and
// docs/spike/phase1.md:60-65.

// chatQueryModel seeds ten messages whose created and lastModified times differ
// deliberately, so an ordering test can tell the two apart.
func chatQueryModel() Model {
	msgs := make([]Message, 0, 10)
	for i := 0; i < 10; i++ {
		created := tJan1.Add(time.Duration(i) * time.Hour)
		modified := created.Add(48 * time.Hour)
		if i%3 != 0 {
			modified = created
		}
		msgs = append(msgs, Message{
			ID:       fmt.Sprintf("q-%d", i),
			AuthorID: "u-me",
			Body:     fmt.Sprintf("<p>message %d</p>", i),
			Created:  created,
			Modified: modified,
		})
	}
	return Model{
		Me:    "u-me",
		Users: []User{{ID: "u-me", DisplayName: "Me"}},
		Chats: []Chat{{ID: "cq", ChatType: ChatTypeGroup, Members: members("u-me"), Messages: msgs}},
	}
}

func newChatQueryServer(t *testing.T) (*Server, *graph.Client) {
	t.Helper()
	srv := New(t, Options{Model: chatQueryModel(), Clock: newFakeClock()})
	return srv, newClient(t, srv)
}

func chatMessageIDs(t *testing.T, c *graph.Client, query url.Values) []string {
	t.Helper()
	page := mustPage[messageWire](t, c, "/chats/cq/messages", query)
	ids := make([]string, 0, len(page.Value))
	for _, m := range page.Value {
		ids = append(ids, m.ID)
	}
	return ids
}

func TestChatMessageQueryMatrix(t *testing.T) {
	_, c := newChatQueryServer(t)
	// The fixtures are anchored at tJan1, so the cut-offs are derived from it
	// rather than written as literals.
	cut5 := tJan1.Add(5 * time.Hour).Format(time.RFC3339)
	cut24 := tJan1.Add(24 * time.Hour).Format(time.RFC3339)

	cases := []struct {
		name       string
		query      url.Values
		wantStatus int
		want       []string
		check      func(t *testing.T, ids []string)
	}{
		{
			name:  "orderby createdDateTime desc",
			query: url.Values{"$orderby": {"createdDateTime desc"}},
			want:  []string{"q-9", "q-8", "q-7", "q-6", "q-5", "q-4", "q-3", "q-2", "q-1", "q-0"},
		},
		{
			// lastModified hours are 48,1,2,51,4,5,54,7,8,57, so this order is
			// neither the created order nor its reverse.
			name:  "orderby lastModifiedDateTime desc",
			query: url.Values{"$orderby": {"lastModifiedDateTime desc"}},
			want:  []string{"q-9", "q-6", "q-3", "q-0", "q-8", "q-7", "q-5", "q-4", "q-2", "q-1"},
		},
		{
			name:  "orderby defaults to descending when no direction is given",
			query: url.Values{"$orderby": {"createdDateTime"}},
			want:  []string{"q-9", "q-8", "q-7", "q-6", "q-5", "q-4", "q-3", "q-2", "q-1", "q-0"},
		},
		{
			name:       "ascending order is not supported",
			query:      url.Values{"$orderby": {"createdDateTime asc"}},
			wantStatus: 400,
		},
		{
			name:       "an unknown orderby property is a 400",
			query:      url.Values{"$orderby": {"importance desc"}},
			wantStatus: 400,
		},
		{
			name:       "createdDateTime gt is a 400",
			query:      url.Values{"$filter": {"createdDateTime gt 2026-01-01T00:00:00Z"}},
			wantStatus: 400,
		},
		{
			name:  "createdDateTime lt without orderby is ignored",
			query: url.Values{"$filter": {"createdDateTime lt " + cut5}},
			// The filter is dropped, so the page is the full (unsorted)
			// collection.
			check: func(t *testing.T, ids []string) {
				t.Helper()
				if len(ids) != 10 {
					t.Fatalf("filter was applied without a matching $orderby: got %d messages", len(ids))
				}
			},
		},
		{
			name:  "createdDateTime lt with a matching orderby is applied",
			query: url.Values{"$orderby": {"createdDateTime desc"}, "$filter": {"createdDateTime lt " + cut5}},
			want:  []string{"q-4", "q-3", "q-2", "q-1", "q-0"},
		},
		{
			name:  "lastModifiedDateTime gt works without an orderby",
			query: url.Values{"$filter": {"lastModifiedDateTime gt " + cut24}},
			check: func(t *testing.T, ids []string) {
				t.Helper()
				// The four messages whose lastModified is more than 24h after
				// creation are q-0, q-3, q-6 and q-9. The spike saw this filter
				// applied even without $orderby (docs/spike/phase1.md:62); the
				// default order is unsorted, so the check is on the set.
				if got := sortStringsCopy(ids); strings.Join(got, ",") != "q-0,q-3,q-6,q-9" {
					t.Fatalf("filtered set = %v, want q-0,q-3,q-6,q-9", got)
				}
			},
		},
		{
			name:  "lastModifiedDateTime gt is ignored when orderby names another property",
			query: url.Values{"$orderby": {"createdDateTime desc"}, "$filter": {"lastModifiedDateTime gt " + cut24}},
			want:  []string{"q-9", "q-8", "q-7", "q-6", "q-5", "q-4", "q-3", "q-2", "q-1", "q-0"},
		},
		{
			name:       "an unsupported filter operator is a 400",
			query:      url.Values{"$filter": {"createdDateTime eq " + cut5}},
			wantStatus: 400,
		},
		{
			name:       "an unparseable filter date is a 400",
			query:      url.Values{"$filter": {"lastModifiedDateTime gt yesterday"}},
			wantStatus: 400,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantStatus != 0 {
				_, err := graph.GetPage[messageWire](context.Background(), c, "/chats/cq/messages", tc.query)
				if got := errStatus(err); got != tc.wantStatus {
					t.Fatalf("status = %d (err %v), want %d", got, err, tc.wantStatus)
				}
				return
			}
			ids := chatMessageIDs(t, c, tc.query)
			if tc.want != nil && strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("order = %v, want %v", ids, tc.want)
			}
			if tc.check != nil {
				tc.check(t, ids)
			}
		})
	}
}

func TestChatMessageDefaultOrderIsNotSorted(t *testing.T) {
	_, c := newChatQueryServer(t)

	ids := chatMessageIDs(t, c, nil)
	if len(ids) != 10 {
		t.Fatalf("got %d messages, want 10", len(ids))
	}
	descending := chatMessageIDs(t, c, url.Values{"$orderby": {"createdDateTime desc"}})
	ascending := reverse(descending)
	if strings.Join(ids, ",") == strings.Join(descending, ",") || strings.Join(ids, ",") == strings.Join(ascending, ",") {
		t.Fatalf("default order %v is date-sorted; the spike saw unsorted messages", ids)
	}
	// Deterministic: the same request gives the same order.
	again := chatMessageIDs(t, c, nil)
	if strings.Join(ids, ",") != strings.Join(again, ",") {
		t.Fatalf("default order is not deterministic: %v then %v", ids, again)
	}
}

func TestChatMessagePagingKeepsOrderAndFilter(t *testing.T) {
	_, c := newChatQueryServer(t)
	query := url.Values{
		"$orderby": {"createdDateTime desc"},
		"$filter":  {"createdDateTime lt " + tJan1.Add(7*time.Hour).Format(time.RFC3339)},
		"$top":     {"2"},
	}
	page := mustPage[messageWire](t, c, "/chats/cq/messages", query)
	if len(page.Value) != 2 || page.Value[0].ID != "q-6" || page.Value[1].ID != "q-5" {
		t.Fatalf("first page = %v", idsOf(page.Value))
	}
	if page.NextLink == "" {
		t.Fatal("no next link")
	}
	second := mustPage[messageWire](t, c, page.NextLink, nil)
	if idsOf(second.Value)[0] != "q-4" {
		t.Fatalf("second page starts at %v, want q-4", idsOf(second.Value))
	}
}

func TestChatMessagePagingCapsAt50(t *testing.T) {
	_, c := newChatQueryServer(t)
	if _, err := graph.GetPage[messageWire](context.Background(), c, "/chats/cq/messages", url.Values{"$top": {"51"}}); errStatus(err) != 400 {
		t.Fatalf("err = %v, want 400", err)
	}
}

func TestParseChatMessageQuery(t *testing.T) {
	cases := []struct {
		name        string
		query       url.Values
		wantOrder   string
		wantFilter  string
		wantIgnored bool
		wantErr     bool
	}{
		{name: "empty", query: url.Values{}},
		{name: "order desc", query: url.Values{"$orderby": {"createdDateTime desc"}}, wantOrder: propCreated},
		{name: "order implicit desc", query: url.Values{"$orderby": {"lastModifiedDateTime"}}, wantOrder: propLastModified},
		{name: "order asc", query: url.Values{"$orderby": {"createdDateTime asc"}}, wantErr: true},
		{name: "order unknown", query: url.Values{"$orderby": {"subject desc"}}, wantErr: true},
		{name: "order malformed", query: url.Values{"$orderby": {"createdDateTime desc extra"}}, wantErr: true},
		{name: "filter applied", query: url.Values{"$orderby": {"createdDateTime desc"}, "$filter": {"createdDateTime lt 2026-01-01T00:00:00Z"}}, wantOrder: propCreated, wantFilter: propCreated},
		{name: "filter ignored", query: url.Values{"$filter": {"createdDateTime lt 2026-01-01T00:00:00Z"}}, wantIgnored: true},
		{name: "lastModified filter always on", query: url.Values{"$filter": {"lastModifiedDateTime gt 2026-01-01T00:00:00Z"}}, wantFilter: propLastModified},
		{name: "createdTime gt rejected", query: url.Values{"$filter": {"createdDateTime gt 2026-01-01T00:00:00Z"}}, wantErr: true},
		{name: "bad operator", query: url.Values{"$filter": {"lastModifiedDateTime eq 2026-01-01T00:00:00Z"}}, wantErr: true},
		{name: "bad shape", query: url.Values{"$filter": {"lastModifiedDateTime gt"}}, wantErr: true},
		{name: "unknown property", query: url.Values{"$filter": {"subject gt 2026-01-01T00:00:00Z"}}, wantErr: true},
		{name: "quoted timestamp", query: url.Values{"$filter": {"lastModifiedDateTime gt '2026-01-01T00:00:00Z'"}}, wantFilter: propLastModified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseChatMessageQuery(tc.query)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				if err.Status != 400 {
					t.Fatalf("status = %d, want 400", err.Status)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.OrderBy != tc.wantOrder {
				t.Fatalf("OrderBy = %q, want %q", got.OrderBy, tc.wantOrder)
			}
			if got.FilterProp != tc.wantFilter {
				t.Fatalf("FilterProp = %q, want %q", got.FilterProp, tc.wantFilter)
			}
			if got.IgnoredFilter != tc.wantIgnored {
				t.Fatalf("IgnoredFilter = %v, want %v", got.IgnoredFilter, tc.wantIgnored)
			}
		})
	}
}

// sortStringsCopy returns a sorted copy, so a set assertion does not depend on
// the default (deliberately unsorted) order.
func sortStringsCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func reverse(in []string) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = in[len(in)-1-i]
	}
	return out
}

func idsOf(msgs []messageWire) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.ID)
	}
	return out
}
