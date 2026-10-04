package fakegraph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers the directory endpoints: the user-list query surface
// ($filter, $search, $select, $orderby, paging), single-user lookup and
// /me/people.

func TestListUsersFilterMatrix(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	cases := []struct {
		name    string
		filter  string
		want    []string
		wantErr bool
	}{
		{name: "no filter", filter: "", want: []string{"u-me", "u-alice", "u-bob", "u-carol", "u-dave", "u-erin", "u-frank"}},
		{name: "eq display name", filter: "displayName eq 'Alice Example'", want: []string{"u-alice"}},
		{name: "eq is case-insensitive", filter: "displayName eq 'alice example'", want: []string{"u-alice"}},
		{name: "eq userPrincipalName", filter: "userPrincipalName eq 'bob@contoso.example'", want: []string{"u-bob"}},
		{name: "eq mail", filter: "mail eq 'carol@contoso.example'", want: []string{"u-carol"}},
		{name: "eq id", filter: "id eq 'u-dave'", want: []string{"u-dave"}},
		{name: "ne", filter: "displayName ne 'Alice Example'", want: []string{"u-me", "u-bob", "u-carol", "u-dave", "u-erin", "u-frank"}},
		{name: "startswith displayName", filter: "startswith(displayName,'A')", want: []string{"u-alice"}},
		{name: "startswith userPrincipalName", filter: "startswith(userPrincipalName,'erin')", want: []string{"u-erin"}},
		// The OData escape doubles the quote, which the CLI must do and
		// teams-mcp never did (PLAN.md, "Escape `'` in OData `$filter`").
		{name: "escaped quote", filter: "displayName eq 'Frank O''Neil'", want: []string{"u-frank"}},
		{name: "unknown property matches nothing", filter: "phone eq 'x'", want: nil},
		{name: "unsupported operator", filter: "displayName gt 'A'", wantErr: true},
		{name: "malformed", filter: "displayName eq", wantErr: true},
		{name: "malformed startswith", filter: "startswith(displayName)", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			query := url.Values{}
			if tc.filter != "" {
				query.Set("$filter", tc.filter)
			}
			if tc.wantErr {
				_, err := graph.GetPage[map[string]any](context.Background(), c, "/users", query)
				if errStatus(err) != 400 {
					t.Fatalf("err = %v, want 400", err)
				}
				return
			}
			page := mustPage[map[string]any](t, c, "/users", query)
			got := make([]string, 0, len(page.Value))
			for _, u := range page.Value {
				got = append(got, u["id"].(string))
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("ids = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestListUsersSearchSelectAndOrder(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	t.Run("search matches several fields", func(t *testing.T) {
		page := mustPage[map[string]any](t, c, "/users", url.Values{"$search": {`"Engineer"`}})
		if len(page.Value) != 1 || page.Value[0]["id"] != "u-alice" {
			t.Fatalf("search by jobTitle = %v", page.Value)
		}
		byName := mustPage[map[string]any](t, c, "/users", url.Values{"$search": {"carol"}})
		if len(byName.Value) != 1 || byName.Value[0]["id"] != "u-carol" {
			t.Fatalf("search by name = %v", byName.Value)
		}
	})

	t.Run("orderby displayName", func(t *testing.T) {
		page := mustPage[map[string]any](t, c, "/users", url.Values{"$orderby": {"displayName"}})
		names := make([]string, 0, len(page.Value))
		for _, u := range page.Value {
			names = append(names, u["displayName"].(string))
		}
		for i := 1; i < len(names); i++ {
			if names[i-1] > names[i] {
				t.Fatalf("names are not sorted: %v", names)
			}
		}
	})

	t.Run("orderby userPrincipalName", func(t *testing.T) {
		page := mustPage[map[string]any](t, c, "/users", url.Values{"$orderby": {"userPrincipalName"}})
		if len(page.Value) == 0 {
			t.Fatal("empty page")
		}
	})

	t.Run("unsupported orderby", func(t *testing.T) {
		if _, err := graph.GetPage[map[string]any](context.Background(), c, "/users", url.Values{"$orderby": {"mail"}}); errStatus(err) != 400 {
			t.Fatalf("err = %v, want 400", err)
		}
	})

	t.Run("select narrows the payload", func(t *testing.T) {
		page := mustPage[map[string]any](t, c, "/users", url.Values{"$select": {"id,displayName,jobTitle"}})
		alice := page.Value[1]
		if len(alice) != 3 {
			t.Fatalf("selected user = %v, want exactly three properties", alice)
		}
		if alice["displayName"] != "Alice Example" || alice["jobTitle"] != "Engineer" {
			t.Fatalf("selected user = %v", alice)
		}
		if _, ok := alice["mail"]; ok {
			t.Fatal("$select returned a property that was not asked for")
		}
	})

	t.Run("select every supported property", func(t *testing.T) {
		page := mustPage[map[string]any](t, c, "/users", url.Values{
			"$select": {"id,displayName,givenName,surname,userPrincipalName,mail,jobTitle,department,officeLocation,preferredLanguage,mobilePhone,businessPhones"},
		})
		if len(page.Value) == 0 {
			t.Fatal("empty page")
		}
	})

	t.Run("select an unknown property is dropped", func(t *testing.T) {
		page := mustPage[map[string]any](t, c, "/users", url.Values{"$select": {"id,nonsense"}})
		if len(page.Value[0]) != 1 {
			t.Fatalf("selected user = %v, want only id", page.Value[0])
		}
	})
}

func TestGetUser(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	t.Run("by id", func(t *testing.T) {
		resp, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/users/u-alice"})
		if err != nil {
			t.Fatal(err)
		}
		var user userWire
		if err := resp.Decode(&user); err != nil {
			t.Fatal(err)
		}
		if user.ID != "u-alice" || user.Mail != "alice@contoso.example" {
			t.Fatalf("user = %+v", user)
		}
	})

	t.Run("by user principal name", func(t *testing.T) {
		resp, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/users/bob@contoso.example"})
		if err != nil {
			t.Fatalf("lookup by UPN: %v", err)
		}
		var user userWire
		if err := resp.Decode(&user); err != nil {
			t.Fatal(err)
		}
		if user.ID != "u-bob" {
			t.Fatalf("user id = %q, want u-bob", user.ID)
		}
	})

	t.Run("me resolves to the signed-in user", func(t *testing.T) {
		resp, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/users/me"})
		if err != nil {
			t.Fatal(err)
		}
		var user userWire
		if err := resp.Decode(&user); err != nil {
			t.Fatal(err)
		}
		if user.ID != "u-me" {
			t.Fatalf("user id = %q", user.ID)
		}
	})

	t.Run("unknown user", func(t *testing.T) {
		if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/users/nobody"}); errStatus(err) != 404 {
			t.Fatalf("err = %v, want 404", err)
		}
	})

	t.Run("select on a single user", func(t *testing.T) {
		resp, err := c.Do(context.Background(), graph.Request{
			Method: http.MethodGet, Path: "/users/u-alice",
			Query: url.Values{"$select": {"id,department"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var user map[string]any
		if err := resp.Decode(&user); err != nil {
			t.Fatal(err)
		}
		if len(user) != 2 || user["id"] != "u-alice" {
			t.Fatalf("selected user = %v", user)
		}
	})
}

func TestPeople(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	t.Run("relevance order", func(t *testing.T) {
		page := mustPage[personWire](t, c, "/me/people", nil)
		if len(page.Value) != 6 {
			t.Fatalf("people = %d, want 6 (the signed-in user is not their own person)", len(page.Value))
		}
		if page.Value[0].ID != "u-alice" {
			t.Fatalf("first person = %q, want the highest-relevance user u-alice", page.Value[0].ID)
		}
		if page.Value[0].ScoredEmailAddresses[0].Address == "" {
			t.Fatal("person carries no scored email address")
		}
	})

	t.Run("search by name", func(t *testing.T) {
		page := mustPage[personWire](t, c, "/me/people", url.Values{"$search": {"carol"}})
		if len(page.Value) != 1 || page.Value[0].ID != "u-carol" {
			t.Fatalf("search = %+v", page.Value)
		}
	})

	t.Run("search with the displayName qualifier", func(t *testing.T) {
		page := mustPage[personWire](t, c, "/me/people", url.Values{"$search": {"displayName:Dave"}})
		if len(page.Value) != 1 || page.Value[0].ID != "u-dave" {
			t.Fatalf("search = %+v", page.Value)
		}
	})

	t.Run("search with the topic keyword", func(t *testing.T) {
		page := mustPage[personWire](t, c, "/me/people", url.Values{"$search": {"topic:erin"}})
		if len(page.Value) != 1 || page.Value[0].ID != "u-erin" {
			t.Fatalf("search = %+v", page.Value)
		}
	})

	t.Run("top and paging", func(t *testing.T) {
		page := mustPage[personWire](t, c, "/me/people", url.Values{"$top": {"2"}})
		if len(page.Value) != 2 || page.NextLink == "" {
			t.Fatalf("page = %d, next=%q", len(page.Value), page.NextLink)
		}
	})

	t.Run("unsupported query parameter", func(t *testing.T) {
		if _, err := graph.GetPage[personWire](context.Background(), c, "/me/people", url.Values{"$expand": {"manager"}}); errStatus(err) != 400 {
			t.Fatalf("err = %v, want 400", err)
		}
	})
}

func TestUserQueryHelpers(t *testing.T) {
	if got, ok := userProperty(&userRec{id: "x"}, "nope"); ok || got != "" {
		t.Fatalf("userProperty(unknown) = %q, %v", got, ok)
	}
	u := &userRec{id: "u", displayName: "Alice", upn: "alice@x", mail: "a@x", givenName: "Alice", surname: "X", jobTitle: "Engineer", department: "Eng"}
	for _, prop := range []string{"id", "displayName", "userPrincipalName", "mail", "givenName", "surname", "jobTitle", "department"} {
		if _, ok := userProperty(u, prop); !ok {
			t.Errorf("userProperty(%q) reported unknown", prop)
		}
	}
	if !userMatchesSearch(u, "engineer") || userMatchesSearch(u, "nomatch") {
		t.Error("userMatchesSearch behaved unexpectedly")
	}
	if !personMatchesSearch(u, "displayName:Ali") || personMatchesSearch(u, "displayName:Bob") {
		t.Error("personMatchesSearch behaved unexpectedly")
	}
	if !personMatchesSearch(u, "alice") {
		t.Error("personMatchesSearch without a qualifier must fall back to the fuzzy search")
	}
	if got := parseSelect(" id , displayName ,, "); strings.Join(got, ",") != "id,displayName" {
		t.Fatalf("parseSelect = %v", got)
	}
	if got := parseSelect(""); got != nil {
		t.Fatalf("parseSelect(\"\") = %v", got)
	}
	if prop, _, ok := splitTopLevelComma("a,'b,c'"); !ok || prop != "a" {
		t.Fatalf("splitTopLevelComma = %q, %v", prop, ok)
	}
	if _, _, ok := splitTopLevelComma("nocomma"); ok {
		t.Fatal("splitTopLevelComma accepted a string without a comma")
	}
	if got := unquoteOData("'Frank O''Neil'"); got != "Frank O'Neil" {
		t.Fatalf("unquoteOData = %q", got)
	}
	if _, err := parseUserFilter("displayName in ('a','b')"); err == nil {
		t.Fatal("parseUserFilter accepted an unsupported operator")
	}
	if _, err := parseUserFilter("nonsense"); err == nil {
		t.Fatal("parseUserFilter accepted a malformed filter")
	}
	if prop, err := parseUserOrderBy(""); err != nil || prop != "" {
		t.Fatalf("parseUserOrderBy(\"\") = %q, %v", prop, err)
	}
}

func TestChatListFilterOrderAndExpand(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	t.Run("filter by topic", func(t *testing.T) {
		page := mustPage[chatWire](t, c, "/me/chats", url.Values{"$filter": {"topic eq 'Release train'"}})
		if len(page.Value) != 1 || page.Value[0].ID != "chat-group" {
			t.Fatalf("chats = %+v", page.Value)
		}
	})

	t.Run("filter by chatType", func(t *testing.T) {
		page := mustPage[chatWire](t, c, "/me/chats", url.Values{"$filter": {"chatType eq 'oneOnOne'"}})
		if len(page.Value) != 2 {
			t.Fatalf("one-on-one chats = %d, want 2", len(page.Value))
		}
	})

	t.Run("unsupported filter", func(t *testing.T) {
		if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats", url.Values{"$filter": {"isHiddenForAllMembers eq true"}}); errStatus(err) != 400 {
			t.Fatalf("err = %v, want 400", err)
		}
		if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats", url.Values{"$filter": {"topic gt 'a'"}}); errStatus(err) != 400 {
			t.Fatalf("err = %v, want 400", err)
		}
	})

	t.Run("orderby lastMessagePreview/createdDateTime desc", func(t *testing.T) {
		page := mustPage[chatWire](t, c, "/me/chats",
			url.Values{"$orderby": {"lastMessagePreview/createdDateTime desc"}})
		if len(page.Value) == 0 {
			t.Fatal("empty page")
		}
		if page.Value[0].ID != "chat-1on1" {
			t.Fatalf("first chat = %q, want the one with the newest message", page.Value[0].ID)
		}
	})

	t.Run("orderby is descending only", func(t *testing.T) {
		if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats",
			url.Values{"$orderby": {"lastMessagePreview/createdDateTime asc"}}); errStatus(err) != 400 {
			t.Fatalf("err = %v, want 400", err)
		}
		if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats",
			url.Values{"$orderby": {"createdDateTime desc"}}); errStatus(err) != 400 {
			t.Fatalf("err = %v, want 400", err)
		}
	})

	t.Run("unsupported expand", func(t *testing.T) {
		if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats", url.Values{"$expand": {"tabs"}}); errStatus(err) != 400 {
			t.Fatalf("err = %v, want 400", err)
		}
	})

	t.Run("expand members on a single chat", func(t *testing.T) {
		chat := getChatByID(t, c, "chat-group", url.Values{"$expand": {"members"}})
		if len(chat.Members) != 3 {
			t.Fatalf("members = %d, want 3", len(chat.Members))
		}
		if chat.Topic == nil || *chat.Topic != "Release train" {
			t.Fatalf("topic = %v", chat.Topic)
		}
	})

	t.Run("unknown chat", func(t *testing.T) {
		if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me/chats/nope"}); errStatus(err) != 404 {
			t.Fatalf("err = %v, want 404", err)
		}
	})

	t.Run("unknown chat members", func(t *testing.T) {
		if _, err := graph.GetPage[conversationMemberWire](context.Background(), c, "/me/chats/nope/members", nil); errStatus(err) != 404 {
			t.Fatalf("err = %v, want 404", err)
		}
	})
}

// getChatByID fetches one chat object, which a single-resource response is.
func getChatByID(t *testing.T, c *graph.Client, id string, query url.Values) chatWire {
	t.Helper()
	resp, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me/chats/" + id, Query: query})
	if err != nil {
		t.Fatalf("GET /me/chats/%s: %v", id, err)
	}
	var chat chatWire
	if err := resp.Decode(&chat); err != nil {
		t.Fatal(err)
	}
	return chat
}

func TestChatMembersExpandCap(t *testing.T) {
	// The docs cap an expanded chat at 25 members; the spike saw 116 with no
	// cap, so the fake defaults to no cap and the documented behaviour is an
	// option away (refs/graph/api-reference/v1.0/api/chat-list.md:31;
	// docs/spike/phase1.md:59).
	model := testModel()
	big := make([]Member, 0, 30)
	big = append(big, Member{UserID: "u-me", Roles: []string{"owner"}})
	users := make([]User, 0, 30)
	for i := 0; i < 29; i++ {
		id := "member-" + itoa(i)
		users = append(users, User{ID: id, DisplayName: "Member " + itoa(i)})
		big = append(big, Member{UserID: id, Roles: []string{"member"}})
	}
	model.Users = append(model.Users, users...)
	model.Chats = append(model.Chats, Chat{ID: "chat-big", ChatType: ChatTypeGroup, Members: big, Messages: []Message{
		{ID: "bigmsg", AuthorID: "u-me", Body: "<p>hi</p>", Created: tJan1},
	}})

	t.Run("no cap by default", func(t *testing.T) {
		srv := New(t, Options{Model: model, Clock: newFakeClock()})
		c := newClient(t, srv)
		chat := getChatByID(t, c, "chat-big", url.Values{"$expand": {"members"}})
		if len(chat.Members) != 30 {
			t.Fatalf("members = %d, want all 30 (the spike saw no 25 cap)", len(chat.Members))
		}
	})

	t.Run("documented cap when configured", func(t *testing.T) {
		srv := New(t, Options{Model: model, Clock: newFakeClock(), ChatMembersExpandCap: 25})
		c := newClient(t, srv)
		chat := getChatByID(t, c, "chat-big", url.Values{"$expand": {"members"}})
		if len(chat.Members) != 25 {
			t.Fatalf("members = %d, want the documented cap of 25", len(chat.Members))
		}
	})
}

func TestHiddenChatIsExcludedFromTheList(t *testing.T) {
	model := testModel()
	model.Chats = append(model.Chats, Chat{
		ID: "chat-hidden", ChatType: ChatTypeGroup, Hidden: true, Members: members("u-me", "u-alice"),
		Messages: []Message{{ID: "hidden-m", AuthorID: "u-alice", Body: "<p>shh</p>", Created: tJan1}},
	})
	srv := New(t, Options{Model: model, Clock: newFakeClock()})
	c := newClient(t, srv)

	page := mustPage[chatWire](t, c, "/me/chats", nil)
	if containsID(chatIDs(page.Value), "chat-hidden") {
		t.Fatal("a hidden-for-all chat is still listed")
	}
	// It is still readable directly.
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me/chats/chat-hidden"}); err != nil {
		t.Fatalf("hidden chat: %v", err)
	}
}

func TestUserIDFromOdataBind(t *testing.T) {
	cases := map[string]string{
		"https://graph.microsoft.com/v1.0/users('abc')": "abc",
		"https://graph.microsoft.com/v1.0/users/abc":    "abc",
		"":            "",
		"/users/xyz/": "xyz",
		"not a bind":  "",
	}
	for in, want := range cases {
		if got := userIDFromOdataBind(in); got != want {
			t.Errorf("userIDFromOdataBind(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUserChatsRoute(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	page := mustPage[chatWire](t, c, "/users/u-alice/chats", nil)
	if len(page.Value) == 0 {
		t.Fatal("no chats for a member")
	}
	if _, err := graph.GetPage[chatWire](context.Background(), c, "/users/nobody/chats", nil); errStatus(err) != 404 {
		t.Fatalf("err = %v, want 404", err)
	}
}

func TestUnsupportedExpandOnUserList(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	if _, err := graph.GetPage[map[string]any](context.Background(), c, "/users", url.Values{"$expand": {"manager"}}); errStatus(err) != 400 {
		t.Fatalf("err = %v, want 400", err)
	}
}

func TestRenderUserSelectedDirect(t *testing.T) {
	st := newStore(testModel(), DefaultTenantID, 0)
	st.mu.RLock()
	defer st.mu.RUnlock()
	u := st.users["u-alice"]
	full, ok := st.renderUserSelected(u, nil).(userWire)
	if !ok || full.ID != "u-alice" {
		t.Fatalf("full user = %#v", full)
	}
	selected := st.renderUserSelected(u, []string{"businessPhones", "preferredLanguage", "mobilePhone", "givenName", "surname", "officeLocation", "id"})
	m, ok := selected.(map[string]any)
	if !ok {
		t.Fatalf("selected = %#v", selected)
	}
	for _, key := range []string{"businessPhones", "preferredLanguage", "mobilePhone", "givenName", "surname", "officeLocation", "id"} {
		if _, ok := m[key]; !ok {
			t.Errorf("selected user is missing %q", key)
		}
	}
}

func TestUploadSessionFlow(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	ctx := context.Background()

	resp, err := c.Do(ctx, graph.Request{
		Method: http.MethodPost,
		Path:   "/drives/drive-t-eng/items/folder-c-general/createUploadSession",
		Body:   map[string]any{"item": map[string]any{"name": "chunked.bin", "@microsoft.graph.conflictBehavior": "replace"}},
	})
	if err != nil {
		t.Fatalf("createUploadSession: %v", err)
	}
	var session struct {
		UploadURL          string   `json:"uploadUrl"`
		ExpirationDateTime string   `json:"expirationDateTime"`
		NextExpectedRanges []string `json:"nextExpectedRanges"`
	}
	if err := resp.Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.UploadURL == "" || !strings.Contains(session.UploadURL, "/_upload/") {
		t.Fatalf("uploadUrl = %q", session.UploadURL)
	}
	if len(session.NextExpectedRanges) != 1 || session.NextExpectedRanges[0] != "0-" {
		t.Fatalf("nextExpectedRanges = %v", session.NextExpectedRanges)
	}

	// A chunk without Content-Range is a 400.
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPut, Path: session.UploadURL, Body: json.RawMessage("hello")}); errStatus(err) != 400 {
		t.Fatalf("chunk without Content-Range: err = %v, want 400", err)
	}
	// A chunk that skips ahead is a 400.
	if _, err := c.Do(ctx, graph.Request{
		Method: http.MethodPut, Path: session.UploadURL, Body: json.RawMessage("hello"),
		Header: http.Header{"Content-Range": []string{"bytes 5-9/10"}},
	}); errStatus(err) != 400 {
		t.Fatalf("out-of-order chunk: err = %v, want 400", err)
	}
	// The first chunk is accepted and reports the next range.
	first, err := c.Do(ctx, graph.Request{
		Method: http.MethodPut, Path: session.UploadURL, Body: json.RawMessage("hello"),
		Header: http.Header{"Content-Range": []string{"bytes 0-4/10"}},
	})
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	var progress struct {
		NextExpectedRanges []string `json:"nextExpectedRanges"`
	}
	if err := first.Decode(&progress); err != nil {
		t.Fatal(err)
	}
	if len(progress.NextExpectedRanges) != 1 || progress.NextExpectedRanges[0] != "5-" {
		t.Fatalf("nextExpectedRanges = %v, want [5-]", progress.NextExpectedRanges)
	}
	// The second chunk completes the upload.
	if _, err := c.Do(ctx, graph.Request{
		Method: http.MethodPut, Path: session.UploadURL, Body: json.RawMessage("world"),
		Header: http.Header{"Content-Range": []string{"bytes 5-9/10"}},
	}); err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	// The uploaded file is now a child of the folder.
	children := mustPage[driveItemWire](t, c, "/drives/drive-t-eng/items/folder-c-general/children", nil)
	var found bool
	for _, item := range children.Value {
		if item.Name == "chunked.bin" && item.Size == 10 {
			found = true
		}
	}
	if !found {
		t.Fatalf("uploaded file missing from the folder: %+v", children.Value)
	}
	// An unknown session is a 404.
	if _, err := c.Do(ctx, graph.Request{
		Method: http.MethodPut, Path: "/_upload/nope", Body: json.RawMessage("x"),
		Header: http.Header{"Content-Range": []string{"bytes 0-0/1"}},
	}); errStatus(err) != 404 {
		t.Fatalf("unknown session: err = %v, want 404", err)
	}
}

func TestUploadSessionNeedsAFolderParent(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/drives/drive-t-eng/items/file-1/createUploadSession",
		Body:   map[string]any{"item": map[string]any{"name": "x.bin"}},
	}); errStatus(err) != 400 {
		t.Fatalf("err = %v, want 400", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/drives/drive-t-eng/items/nope/createUploadSession",
		Body:   map[string]any{"item": map[string]any{"name": "x.bin"}},
	}); errStatus(err) != 404 {
		t.Fatalf("err = %v, want 404", err)
	}
}

func TestFileDownloadAndChildrenErrors(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/drives/drive-t-eng/items/nope"}); errStatus(err) != 404 {
		t.Fatalf("unknown item: err = %v, want 404", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/drives/drive-t-eng/items/nope/children"}); errStatus(err) != 404 {
		t.Fatalf("children of unknown item: err = %v, want 404", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/drives/drive-t-eng/items/folder-c-general/content"}); errStatus(err) != 400 {
		t.Fatalf("content of a folder: err = %v, want 400", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/teams/t-eng/channels/nope/filesFolder"}); errStatus(err) != 404 {
		t.Fatalf("filesFolder of unknown channel: err = %v, want 404", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodPut, Path: "/drives/drive-t-eng/items/folder-c-general/content", Body: json.RawMessage("x")}); errStatus(err) != 400 {
		t.Fatalf("upload into a folder: err = %v, want 400", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodPut, Path: "/drives/drive-t-eng/items/nope/content", Body: json.RawMessage("x")}); errStatus(err) != 404 {
		t.Fatalf("upload to an unknown item: err = %v, want 404", err)
	}
}
