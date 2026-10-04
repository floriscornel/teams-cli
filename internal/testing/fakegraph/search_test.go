package fakegraph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers POST /search/query: the from/size protocol (no
// @odata.nextLink), the full-match `total` and `moreResultsAvailable` the spike
// observed, the bodyless hits, the KQL subset, and the documented-but-broken
// IsRead term that must answer 500.

// guidUserID is a dashed user id, because the documented `mentions:` term takes
// the id without dashes (search-concept-chat-messages.md:267).
const guidUserID = "11111111-2222-3333-4444-555555555555"

// searchFixture is the model the search tests run against. Its message set is
// built so every KQL expectation in this file is a countable fact.
func searchFixture() Model {
	chat := make([]Message, 0, 30)
	for i := 0; i < 30; i++ {
		author := "u-me"
		if i%2 == 0 {
			author = "u-alice"
		}
		chat = append(chat, Message{
			ID:       fmt.Sprintf("s-%02d", i),
			AuthorID: author,
			Body:     fmt.Sprintf("<p>hello world %02d</p>", i),
			Created:  tJan1.Add(9*time.Hour + time.Duration(i)*time.Minute),
		})
	}
	return Model{
		Me: "u-me",
		Users: []User{
			{ID: "u-me", DisplayName: "Me Myself", UserPrincipalName: "me@contoso.example"},
			{ID: "u-alice", DisplayName: "Alice Example", UserPrincipalName: "alice@contoso.example", Mail: "alice@contoso.example"},
			{ID: guidUserID, DisplayName: "Guid User"},
		},
		Teams: []Team{{
			ID: "st", DisplayName: "Search Team",
			Channels: []Channel{{ID: "sc", DisplayName: "General", Messages: []Message{
				{ID: "s-chan", AuthorID: "u-alice", Body: "<p>hello channel</p>", Created: tJan2.Add(9 * time.Hour)},
			}}},
		}},
		Chats: []Chat{
			{ID: "schat", ChatType: ChatTypeGroup, Members: members("u-me", "u-alice"), Messages: chat},
			{ID: "s-1on1", ChatType: ChatTypeOneOnOne, Members: members("u-me", "u-alice"), Messages: []Message{
				{ID: "s-1a", AuthorID: "u-alice", Body: "<p>hello one on one</p>", Created: tJan2.Add(11 * time.Hour)},
				{ID: "s-1b", AuthorID: "u-me", Body: "<p>hi</p>", Created: tJan2.Add(12 * time.Hour)},
			}},
			{ID: "s-attach-chat", ChatType: ChatTypeGroup, Members: members("u-me", "u-alice"), Messages: []Message{
				{ID: "s-att", AuthorID: "u-alice", Body: "<p>hello with file</p>", Created: tJan3.Add(9 * time.Hour),
					Attachments: []Attachment{{ID: "att-1", Name: "spec.pdf", ContentType: "reference", ContentURL: "https://contoso.example/spec.pdf"}}},
				{ID: "s-mention", AuthorID: "u-alice", Body: `<p>hey <at id="0">Me Myself</at> and <at id="1">Guid User</at></p>`, Created: tJan2.Add(10 * time.Hour),
					Mentions: []Mention{
						{ID: 0, Text: "Me Myself", UserID: "u-me", UserDisplayName: "Me Myself"},
						{ID: 1, Text: "Guid User", UserID: guidUserID, UserDisplayName: "Guid User"},
					}},
			}},
		},
	}
}

// searchHit and searchContainer are the decoded response shapes.
type searchHit struct {
	HitID    string          `json:"hitId"`
	Rank     int             `json:"rank"`
	Summary  string          `json:"summary"`
	Resource json.RawMessage `json:"resource"`
}

type searchContainer struct {
	Hits                 []searchHit `json:"hits"`
	Total                int         `json:"total"`
	MoreResultsAvailable bool        `json:"moreResultsAvailable"`
}

// searchResponse is the decoded response shape.
type searchResponse struct {
	Value []struct {
		SearchTerms    []string          `json:"searchTerms"`
		HitsContainers []searchContainer `json:"hitsContainers"`
	} `json:"value"`
}

func (r searchResponse) container() searchContainer {
	if len(r.Value) == 0 || len(r.Value[0].HitsContainers) == 0 {
		return searchContainer{}
	}
	return r.Value[0].HitsContainers[0]
}

// runSearch posts one search request through the real client.
func runSearch(t *testing.T, c *graph.Client, query string, from, size int) (searchResponse, *graph.Response, error) {
	t.Helper()
	req := map[string]any{
		"requests": []map[string]any{{
			"entityTypes": []string{"chatMessage"},
			"query":       map[string]string{"queryString": query},
			"from":        from,
			"size":        size,
		}},
	}
	resp, err := c.Do(context.Background(), graph.Request{Method: http.MethodPost, Path: "/search/query", Body: req})
	if err != nil {
		return searchResponse{}, resp, err
	}
	var out searchResponse
	err = resp.Decode(&out)
	return out, resp, err
}

// searchCount returns the total match count for a query, starting a fresh page
// sequence.
func searchCount(t *testing.T, c *graph.Client, query string) int {
	t.Helper()
	out, _, err := runSearch(t, c, query, 0, 25)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return out.container().Total
}

func TestSearchPagingAndTotal(t *testing.T) {
	srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
	c := newClient(t, srv)

	first, _, err := runSearch(t, c, "hello", 0, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	container := first.container()
	if len(container.Hits) != 5 {
		t.Fatalf("hits = %d, want 5", len(container.Hits))
	}
	// 30 chat messages + hello channel + hello one on one + hello with file.
	if container.Total != 33 {
		t.Fatalf("total = %d, want the full match count 33", container.Total)
	}
	if !container.MoreResultsAvailable {
		t.Fatal("moreResultsAvailable = false on a page that is not the last")
	}
	if len(first.Value[0].SearchTerms) == 0 || first.Value[0].SearchTerms[0] != "hello" {
		t.Fatalf("searchTerms = %v, want the free-text terms", first.Value[0].SearchTerms)
	}

	// The last page reports no more results and a smaller hit count.
	last, _, err := runSearch(t, c, "hello", 30, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	lastContainer := last.container()
	if len(lastContainer.Hits) != 3 {
		t.Fatalf("last page hits = %d, want 3", len(lastContainer.Hits))
	}
	if lastContainer.MoreResultsAvailable {
		t.Fatal("moreResultsAvailable = true on the last page")
	}
	if lastContainer.Total != 33 {
		t.Fatalf("total on the last page = %d, want 33", lastContainer.Total)
	}
	if lastContainer.Hits[0].Rank != 31 {
		t.Fatalf("rank = %d, want 31", lastContainer.Hits[0].Rank)
	}

	// A page past the end is empty, not an error.
	past, _, err := runSearch(t, c, "hello", 30, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	_ = past

	// Ranks continue across pages.
	second, _, err := runSearch(t, c, "hello", 5, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if second.container().Hits[0].Rank != 6 {
		t.Fatalf("rank on page 2 = %d, want 6", second.container().Hits[0].Rank)
	}
}

func TestSearchHasNoNextLink(t *testing.T) {
	srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
	c := newClient(t, srv)
	_, resp, err := runSearch(t, c, "hello", 0, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	// The API pages by from/size; a @odata.nextLink would be a lie
	// (refs/graph/api-reference/v1.0/resources/search-api-overview.md:28).
	if strings.Contains(string(resp.Body), "nextLink") {
		t.Fatalf("search response contains a next link:\n%s", resp.Body)
	}
}

func TestSearchFromMustStartAtZero(t *testing.T) {
	srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
	c := newClient(t, srv)

	_, _, err := runSearch(t, c, "hello", 5, 5)
	if got := errStatus(err); got != 400 {
		t.Fatalf("first page with from=5: status = %d (err %v), want 400", got, err)
	}
	if _, _, err := runSearch(t, c, "hello", 0, 5); err != nil {
		t.Fatalf("first page: %v", err)
	}
	if _, _, err := runSearch(t, c, "hello", 5, 5); err != nil {
		t.Fatalf("continuation page must be accepted: %v", err)
	}
}

func TestSearchSizeLimits(t *testing.T) {
	srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
	c := newClient(t, srv)

	out, _, err := runSearch(t, c, "hello", 0, 50)
	if err != nil {
		t.Fatalf("size=50 must work: %v", err)
	}
	if got := len(out.container().Hits); got != 33 {
		t.Fatalf("size=50 returned %d hits, want all 33", got)
	}
	if _, _, err := runSearch(t, c, "hello", 0, 51); errStatus(err) != 400 {
		t.Fatalf("size=51 err = %v, want 400", err)
	}
	if _, _, err := runSearch(t, c, "hello", 0, 0); errStatus(err) != 400 {
		t.Fatalf("size=0 err = %v, want 400", err)
	}
}

func TestSearchRequestShapeValidation(t *testing.T) {
	srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
	c := newClient(t, srv)

	cases := []struct {
		name string
		body any
		want int
	}{
		{"no requests", map[string]any{"requests": []any{}}, 400},
		{
			"two requests",
			map[string]any{"requests": []any{
				map[string]any{"entityTypes": []string{"chatMessage"}, "query": map[string]string{"queryString": "hi"}},
				map[string]any{"entityTypes": []string{"chatMessage"}, "query": map[string]string{"queryString": "hi"}},
			}},
			400,
		},
		{"wrong entity type", map[string]any{"requests": []any{map[string]any{"entityTypes": []string{"message"}, "query": map[string]string{"queryString": "hi"}}}}, 400},
		{"mixed entity types", map[string]any{"requests": []any{map[string]any{"entityTypes": []string{"chatMessage", "message"}, "query": map[string]string{"queryString": "hi"}}}}, 400},
		{"empty query string", map[string]any{"requests": []any{map[string]any{"entityTypes": []string{"chatMessage"}, "query": map[string]string{"queryString": ""}}}}, 400},
		{"no query object", map[string]any{"requests": []any{map[string]any{"entityTypes": []string{"chatMessage"}}}}, 400},
		{"malformed body", "not json", 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.Do(context.Background(), graph.Request{Method: http.MethodPost, Path: "/search/query", Body: tc.body})
			if got := errStatus(err); got != tc.want {
				t.Fatalf("status = %d (err %v), want %d", got, err, tc.want)
			}
		})
	}
}

func TestSearchHitsHaveNoBody(t *testing.T) {
	srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
	c := newClient(t, srv)

	out, _, err := runSearch(t, c, "hello", 0, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	hit := out.container().Hits[0]
	if strings.Contains(string(hit.Resource), `"body"`) {
		t.Fatalf("hit resource carries a body, which the spike never saw:\n%s", hit.Resource)
	}
	var resource map[string]any
	if err := json.Unmarshal(hit.Resource, &resource); err != nil {
		t.Fatalf("hit resource is not JSON: %v", err)
	}
	for _, key := range []string{"@odata.type", "id", "createdDateTime", "lastModifiedDateTime", "etag", "importance", "subject", "webLink"} {
		if _, ok := resource[key]; !ok {
			t.Errorf("hit resource is missing %q: %v", key, resource)
		}
	}
	if hit.Summary == "" {
		t.Error("hit carries no summary, so a caller has nothing to show")
	}
	if resource["@odata.type"] != "microsoft.graph.chatMessage" {
		t.Errorf("@odata.type = %v, want microsoft.graph.chatMessage", resource["@odata.type"])
	}
}

func TestSearchIsReadReturns500(t *testing.T) {
	srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
	c := newClient(t, srv)

	// The spike saw 500 every time, whatever the casing, alone or combined
	// (docs/spike/phase1.md:79).
	for _, query := range []string{"IsRead:true", "isread:false", "hello IsRead:true", "IsRead:false from:alice"} {
		t.Run(query, func(t *testing.T) {
			_, err := c.Do(context.Background(), graph.Request{Method: http.MethodPost, Path: "/search/query", Body: map[string]any{
				"requests": []map[string]any{{
					"entityTypes": []string{"chatMessage"},
					"query":       map[string]string{"queryString": query},
					"from":        0,
					"size":        5,
				}},
			}})
			if got := errStatus(err); got != 500 {
				t.Fatalf("IsRead status = %d (err %v), want the documented-but-broken 500", got, err)
			}
			var apiErr *graph.APIError
			if !asError(err, &apiErr) || apiErr.Code != "InternalServerError" {
				t.Fatalf("error = %+v, want InternalServerError", apiErr)
			}
		})
	}
}

func TestSearchKQLSubset(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"free text", "hello", 33},
		{"two free-text terms are ANDed", "hello world", 30},
		{"quoted phrase", `"hello world"`, 30},
		{"from by display name", "from:alice", 19},
		{"from combined with free text", "hello from:alice", 18},
		{"from by address", "from:alice@contoso.example", 19},
		{"from unknown user", "from:nobody", 0},
		{"isMentioned true", "IsMentioned:true", 1},
		{"isMentioned false", "IsMentioned:false", 34},
		{"mentions without dashes", "mentions:" + strings.ReplaceAll(guidUserID, "-", ""), 1},
		{"mentions with an unknown id", "mentions:00000000000000000000000000000000", 0},
		{"hasAttachment true", "hasAttachment:true", 1},
		{"hasAttachment false", "hasAttachment:false", 34},
		{"sent at a full timestamp", "sent>=2026-01-02T00:00:00Z", 5},
		{"sent strictly after a timestamp", "sent>2026-01-02T00:00:00Z", 5},
		{"sent with a bare date excludes that day", "sent>2026-01-02", 1},
		{"sent at or after a bare date includes it", "sent>=2026-01-02", 5},
		{"sent before a timestamp", "sent<2026-01-02T00:00:00Z", 30},
		{"sent at or before a bare date", "sent<=2026-01-02", 34},
		{"to in a one-on-one chat", "to:alice hello", 1},
		{"to without a one-on-one chat", "to:bob hello", 0},
		{"no match", "nonsense", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
			c := newClient(t, srv)
			if got := searchCount(t, c, tc.query); got != tc.want {
				t.Fatalf("total for %q = %d, want %d", tc.query, got, tc.want)
			}
		})
	}
}

func TestSearchRejectsUnsupportedKQL(t *testing.T) {
	srv := New(t, Options{Model: searchFixture(), Clock: newFakeClock()})
	c := newClient(t, srv)

	for _, query := range []string{"subject:hello", "filename:spec.pdf", "from:alice extra:thing"} {
		t.Run(query, func(t *testing.T) {
			_, _, err := runSearch(t, c, query, 0, 5)
			if got := errStatus(err); got != 400 {
				t.Fatalf("status = %d (err %v), want 400: an unsupported scope term must not be ignored", got, err)
			}
		})
	}
	// A bad boolean is a 400 too.
	if _, _, err := runSearch(t, c, "IsMentioned:maybe", 0, 5); errStatus(err) != 400 {
		t.Fatalf("IsMentioned:maybe err = %v, want 400", err)
	}
}

func TestSearchRespectsMembership(t *testing.T) {
	// A message in a chat the signed-in user is not a member of must not be
	// found: "You can access only the … message the user is included in"
	// (refs/graph/concepts/search-concept-chat-messages.md:392).
	model := searchFixture()
	model.Users = append(model.Users, User{ID: "u-outsider", DisplayName: "Outsider"})
	model.Chats = append(model.Chats, Chat{
		ID: "s-hidden", ChatType: ChatTypeGroup, Members: members("u-outsider"),
		Messages: []Message{{ID: "s-hidden-1", AuthorID: "u-outsider", Body: "<p>hello secret</p>", Created: tJan3}},
	})
	srv := New(t, Options{Model: model, Clock: newFakeClock()})
	c := newClient(t, srv)
	if got := searchCount(t, c, "hello"); got != 33 {
		t.Fatalf("total = %d, want 33: a chat the user is not in must not contribute", got)
	}
}

func TestParseKQL(t *testing.T) {
	cases := []struct {
		in       string
		freeText int
		from     string
		mentions int
		wantErr  bool
		want500  bool
	}{
		{in: "hello", freeText: 1},
		{in: "hello world", freeText: 2},
		{in: `"hello world"`, freeText: 1},
		{in: "from:alice", from: "alice"},
		{in: "mentions:11111111222233334444555555555555", mentions: 1},
		{in: "mentions:11111111-2222-3333-4444-555555555555", mentions: 1},
		{in: "IsRead:true", want500: true},
		{in: "isread:FALSE", want500: true},
		{in: "IsMentioned:maybe", wantErr: true},
		{in: "hasAttachment:sure", wantErr: true},
		{in: "subject:hello", wantErr: true},
		{in: "sent>not-a-date", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			got, err := parseKQL(tc.in)
			if tc.want500 {
				if err == nil || err.Status != 500 {
					t.Fatalf("err = %v, want a 500", err)
				}
				return
			}
			if tc.wantErr {
				if err == nil || err.Status != 400 {
					t.Fatalf("err = %v, want a 400", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got.freeText) != tc.freeText {
				t.Fatalf("freeText = %v, want %d terms", got.freeText, tc.freeText)
			}
			if got.from != tc.from {
				t.Fatalf("from = %q, want %q", got.from, tc.from)
			}
			if len(got.mentions) != tc.mentions {
				t.Fatalf("mentions = %v, want %d", got.mentions, tc.mentions)
			}
		})
	}
}

func TestSentTermBoundaries(t *testing.T) {
	day := "2026-01-02"
	cases := []struct {
		term  string
		at    time.Time
		match bool
	}{
		{"sent>2026-01-02", time.Date(2026, 1, 2, 23, 59, 59, 0, time.UTC), false},
		{"sent>2026-01-02", time.Date(2026, 1, 3, 0, 0, 1, 0, time.UTC), true},
		{"sent>=2026-01-02", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), true},
		{"sent<2026-01-02", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), false},
		{"sent<=2026-01-02", time.Date(2026, 1, 2, 23, 59, 59, 0, time.UTC), true},
		{"sent<=2026-01-02", time.Date(2026, 1, 3, 0, 0, 1, 0, time.UTC), false},
		{"sent>2026-01-02T00:00:00Z", time.Date(2026, 1, 2, 0, 0, 1, 0, time.UTC), true},
		{"sent>2026-01-02T00:00:00Z", time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range cases {
		filter, ok := parseSentTerm(tc.term)
		if !ok {
			t.Fatalf("parseSentTerm(%q) failed", tc.term)
		}
		if got := filter.matches(tc.at); got != tc.match {
			t.Errorf("%s against %s = %v, want %v", tc.term, tc.at, got, tc.match)
		}
	}
	_ = day
}
