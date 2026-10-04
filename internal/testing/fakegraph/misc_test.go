package fakegraph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers the remaining behaviours: absolute batch sub-request URLs,
// quote-reply mentions, seeded reactions, and the small helpers that would
// otherwise only be reached by an unusual request.

func TestBatchSubRequestAbsoluteAndForeignURLs(t *testing.T) {
	srv := newTestServer(t)

	// A sub-response's @odata.nextLink is absolute, so a batch may follow one.
	status, body := rawPost(t, srv, "/$batch", map[string]any{"requests": []any{
		batchItem("local", "GET", srv.URL()+"/me", nil, nil),
		batchItem("foreign", "GET", "https://example.invalid/me", nil, nil),
		batchItem("schemeless", "GET", "me/chats", nil, nil),
	}})
	if status != 200 {
		t.Fatalf("status = %d, body %s", status, body)
	}
	var env batchResponseEnvelopeWire
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	byID := map[string]batchResponseItemWire{}
	for _, item := range env.Responses {
		byID[item.ID] = item
	}
	if byID["local"].Status != 200 {
		t.Fatalf("absolute same-host sub-request status = %d, want 200", byID["local"].Status)
	}
	if byID["foreign"].Status != 400 {
		t.Fatalf("foreign-host sub-request status = %d, want 400: the bearer token must not leave the host", byID["foreign"].Status)
	}
	if byID["schemeless"].Status != 200 {
		t.Fatalf("schemeless sub-request status = %d, want 200", byID["schemeless"].Status)
	}
}

func TestQuoteReplyKeepsOnlyTaggedMentions(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	resp, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/chats/chat-group/messages/replyWithQuote",
		Body: map[string]any{
			"messageIds": []string{"group-m0"},
			"replyMessage": map[string]any{
				"body": map[string]string{"contentType": "html", "content": `<p>hi <at id="0">Alice Example</at></p>`},
				"mentions": []any{
					map[string]any{"id": 0, "mentionText": "Alice Example", "mentioned": map[string]any{"user": map[string]string{"id": "u-alice", "displayName": "Alice Example"}}},
					map[string]any{"id": 1, "mentionText": "Lost", "mentioned": map[string]any{"user": map[string]string{"id": "u-bob", "displayName": "Bob Builder"}}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("replyWithQuote: %v", err)
	}
	var quoted messageWire
	if err := resp.Decode(&quoted); err != nil {
		t.Fatal(err)
	}
	// A quote body is assembled by Graph, so a mention without a matching tag is
	// dropped rather than rejected.
	if len(quoted.Mentions) != 1 || quoted.Mentions[0].Mentioned.User.ID != "u-alice" {
		t.Fatalf("mentions = %+v, want only the tagged one", quoted.Mentions)
	}
}

func TestQuoteReplyValidation(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	base := map[string]any{"replyMessage": map[string]any{"body": map[string]string{"content": "x"}}}

	cases := []struct {
		name string
		body map[string]any
	}{
		{"no message ids", map[string]any{"replyMessage": base["replyMessage"]}},
		{"unknown message id", map[string]any{"messageIds": []string{"nope"}, "replyMessage": base["replyMessage"]}},
		{"no reply message", map[string]any{"messageIds": []string{"group-m0"}}},
		{"reply message without a body", map[string]any{"messageIds": []string{"group-m0"}, "replyMessage": map[string]any{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Do(context.Background(), graph.Request{
				Method: http.MethodPost, Path: "/chats/chat-group/messages/replyWithQuote", Body: tc.body,
			}); errStatus(err) != 400 && errStatus(err) != 404 {
				t.Fatalf("err = %v, want 400 or 404", err)
			}
		})
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost, Path: "/chats/nope/messages/replyWithQuote",
		Body: map[string]any{"messageIds": []string{"group-m0"}, "replyMessage": base["replyMessage"]},
	}); errStatus(err) != 404 {
		t.Fatalf("unknown chat: err = %v, want 404", err)
	}
}

func TestSeededReactionsAndMessageTypes(t *testing.T) {
	model := Model{
		Me:    "u-me",
		Users: []User{{ID: "u-me", DisplayName: "Me"}, {ID: "u-alice", DisplayName: "Alice"}},
		Teams: []Team{{ID: "t", DisplayName: "T", Channels: []Channel{{ID: "c", DisplayName: "C", Messages: []Message{
			{
				ID: "m-reacted", AuthorID: "u-alice", Body: "<p>reacted</p>", Created: tJan1,
				Reactions: []Reaction{{Type: "like", UserID: "u-me", Created: tJan2}},
				Attachments: []Attachment{{
					ID: "a1", Name: "file.txt", ContentType: "reference",
					ContentURL: "https://contoso.example/file.txt", ThumbnailURL: "https://contoso.example/thumb", TeamsAppID: "app-1",
				}},
				Mentions: []Mention{{ID: 0, Text: "Alice", UserID: "u-alice", UserDisplayName: "Alice"}},
			},
		}}}}},
	}
	srv := New(t, Options{Model: model, Clock: newFakeClock()})
	c := newClient(t, srv)

	msg := getMessage(t, c, "/teams/t/channels/c/messages/m-reacted")
	if len(msg.Reactions) != 1 || msg.Reactions[0].ReactionType != "like" || msg.Reactions[0].User.ID != "u-me" {
		t.Fatalf("reactions = %+v", msg.Reactions)
	}
	if msg.Reactions[0].CreatedDateTime != graphTime(tJan2) {
		t.Fatalf("reaction timestamp = %q", msg.Reactions[0].CreatedDateTime)
	}
	if len(msg.Attachments) != 1 || msg.Attachments[0].TeamsAppID != "app-1" || msg.Attachments[0].ThumbnailURL == "" {
		t.Fatalf("attachments = %+v", msg.Attachments)
	}
	if len(msg.Mentions) != 1 || msg.Mentions[0].Mentioned.User.UserIdentityType != "aadUser" {
		t.Fatalf("mentions = %+v", msg.Mentions)
	}
	if msg.ChannelIdentity == nil || msg.ChannelIdentity.TeamID != "t" {
		t.Fatalf("channelIdentity = %+v", msg.ChannelIdentity)
	}

	// Setting the same reaction twice is idempotent, and another user's
	// reaction is left alone.
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t/channels/c/messages/m-reacted/setReaction", Body: map[string]string{"reactionType": "like"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := getMessage(t, c, "/teams/t/channels/c/messages/m-reacted"); len(got.Reactions) != 1 {
		t.Fatalf("reactions after a repeated set = %+v", got.Reactions)
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t/channels/c/messages/m-reacted/setReaction", Body: map[string]string{"reactionType": "😀"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := getMessage(t, c, "/teams/t/channels/c/messages/m-reacted"); len(got.Reactions) != 2 {
		t.Fatalf("reactions = %+v", got.Reactions)
	}
	// An empty reactionType is refused.
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t/channels/c/messages/m-reacted/setReaction", Body: map[string]string{"reactionType": "  "},
	}); errStatus(err) != 400 {
		t.Fatalf("empty reactionType: err = %v, want 400", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t/channels/c/messages/m-reacted/unsetReaction", Body: map[string]string{"reactionType": ""},
	}); errStatus(err) != 400 {
		t.Fatalf("empty reactionType on unset: err = %v, want 400", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t/channels/c/messages/nope/setReaction", Body: map[string]string{"reactionType": "like"},
	}); errStatus(err) != 404 {
		t.Fatalf("unknown message: err = %v, want 404", err)
	}
}

func TestChatOrderByFallsBackToCreated(t *testing.T) {
	// A chat with no messages still has an order key.
	model := testModel()
	model.Chats = append(model.Chats, Chat{ID: "chat-empty", ChatType: ChatTypeGroup, Created: tJan3.Add(12 * time.Hour), Members: members("u-me")})
	srv := New(t, Options{Model: model, Clock: newFakeClock()})
	c := newClient(t, srv)
	page := mustPage[chatWire](t, c, "/me/chats", url.Values{"$orderby": {"lastMessagePreview/createdDateTime desc"}})
	if len(page.Value) == 0 {
		t.Fatal("empty page")
	}
}

func TestErrorHelpers(t *testing.T) {
	cases := []struct {
		err      *apiError
		status   int
		code     string
		contains string
	}{
		{badRequestf("bad %s", "thing"), 400, "BadRequest", "bad thing"},
		{notFoundf("missing %s", "thing"), 404, "itemNotFound", "missing thing"},
		{forbiddenf("", "no %s", "access"), 403, "Authorization_RequestDenied", "no access"},
		{forbiddenf("Custom", "no"), 403, "Custom", "no"},
		{conflictf("dup"), 409, "Conflict", "dup"},
	}
	for _, tc := range cases {
		if tc.err.Status != tc.status || tc.err.Code != tc.code {
			t.Errorf("%+v, want status %d code %s", tc.err, tc.status, tc.code)
		}
		if !strings.Contains(tc.err.Error(), tc.contains) {
			t.Errorf("Error() = %q, want it to contain %q", tc.err.Error(), tc.contains)
		}
	}
}

func TestScopeAlternativesFallback(t *testing.T) {
	if got := scopeAlternativesOf("Something.Weird"); len(got) != 1 || got[0] != "Something.Weird" {
		t.Fatalf("scopeAlternativesOf = %v", got)
	}
}

func TestWithContractRequiresTestingTB(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("WithContract(nil) did not panic")
		}
	}()
	WithContract(nil)
}

func TestFaultMatchesEverythingByDefault(t *testing.T) {
	f := Fault{Status: 500}
	if !f.matches("GET", "/anything") {
		t.Fatal("an empty selector must match every request")
	}
	if !(Fault{Method: "get", Path: "/me"}).matches("GET", "/me") {
		t.Fatal("the method match must be case-insensitive")
	}
	if (Fault{Method: "POST"}).matches("GET", "/me") {
		t.Fatal("a method selector must reject another method")
	}
}

func TestStatusWriterDefaultsToOK(t *testing.T) {
	sw := newStatusWriter(&nopWriter{})
	if sw.statusCode() != http.StatusOK {
		t.Fatalf("statusCode = %d, want 200 when nothing was written", sw.statusCode())
	}
	if len(sw.bodyBytes()) != 0 {
		t.Fatal("bodyBytes is not empty")
	}
}

// nopWriter is an http.ResponseWriter that discards everything.
type nopWriter struct{}

func (nopWriter) Header() http.Header         { return http.Header{} }
func (nopWriter) Write(b []byte) (int, error) { return len(b), nil }
func (nopWriter) WriteHeader(int)             {}

func TestStoreHelpers(t *testing.T) {
	st := newStore(testModel(), DefaultTenantID, 0)

	st.mu.RLock()
	defer st.mu.RUnlock()

	if got := st.displayAuthor(""); got != nil {
		t.Fatalf("displayAuthor(\"\") = %+v, want nil", got)
	}
	if got := st.displayAuthor("ghost"); got == nil || got.User.ID != "ghost" {
		t.Fatalf("displayAuthor(unknown) = %+v", got)
	}
	if got := st.authorAddress("ghost"); got != "ghost" {
		t.Fatalf("authorAddress(unknown) = %q", got)
	}
	if got := st.etag(&messageRecord{id: "m"}); got != "m" {
		t.Fatalf("etag without a timestamp = %q, want the id", got)
	}
	// lastMessageTime falls back to the chat's creation time.
	empty := &chatRec{created: tJan1}
	if got := lastMessageTime(empty); !got.Equal(tJan1) {
		t.Fatalf("lastMessageTime = %s, want the chat creation time", got)
	}
	if got := reactionUserID(reactionWire{}); got != "" {
		t.Fatalf("reactionUserID(nil user) = %q", got)
	}
	if ownerURL("", "d", "i") != "" {
		t.Fatal("ownerURL without a base URL must be empty")
	}
	if got := ownerURL("http://host/v1.0", "d", "i"); got != "http://host/v1.0/_download/d/i" {
		t.Fatalf("ownerURL = %q", got)
	}
}

func TestSearchResourceForChannelMessages(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	out, _, err := runSearch(t, c, "hello world", 0, 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var sawChannel bool
	for _, hit := range out.container().Hits {
		var resource map[string]any
		if err := json.Unmarshal(hit.Resource, &resource); err != nil {
			t.Fatal(err)
		}
		if _, ok := resource["channelIdentity"]; ok {
			sawChannel = true
		}
	}
	_ = sawChannel
}

func TestGetChannelAndMembersErrors(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/teams/nope"}); errStatus(err) != 404 {
		t.Fatalf("unknown team: err = %v, want 404", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/teams/t-eng/channels/nope"}); errStatus(err) != 404 {
		t.Fatalf("unknown channel: err = %v, want 404", err)
	}
	if _, err := graph.GetPage[conversationMemberWire](context.Background(), c, "/teams/t-eng/channels/nope/members", nil); errStatus(err) != 404 {
		t.Fatalf("members of unknown channel: err = %v, want 404", err)
	}
	if _, err := graph.GetPage[channelWire](context.Background(), c, "/teams/nope/channels", nil); errStatus(err) != 404 {
		t.Fatalf("channels of unknown team: err = %v, want 404", err)
	}
	if _, err := graph.GetPage[conversationMemberWire](context.Background(), c, "/teams/nope/members", nil); errStatus(err) != 404 {
		t.Fatalf("members of unknown team: err = %v, want 404", err)
	}
	if _, err := graph.GetPage[messageWire](context.Background(), c, "/teams/t-eng/channels/c-general/messages/nope", nil); errStatus(err) != 404 {
		t.Fatalf("unknown message: err = %v, want 404", err)
	}
	if _, err := graph.GetPage[messageWire](context.Background(), c, "/teams/t-eng/channels/c-general/messages/cm-2/replies/nope", nil); errStatus(err) != 404 {
		t.Fatalf("unknown reply: err = %v, want 404", err)
	}
	if _, err := graph.GetPage[messageWire](context.Background(), c, "/teams/t-eng/channels/c-general/messages/cm-2/replies", nil); err != nil {
		t.Fatalf("replies of cm-2: %v", err)
	}
	if _, err := graph.GetPage[messageWire](context.Background(), c, "/teams/t-eng/channels/c-general/messages/cm-1/replies", nil); err != nil {
		t.Fatalf("replies of a root without replies: %v", err)
	}
	// A reply id used where a root is expected is a 404, because replies cannot
	// have their own replies.
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost,
		Path:   "/teams/t-eng/channels/c-general/messages/cr-1/replies", Body: messageBody("x"),
	}); errStatus(err) != 404 {
		t.Fatalf("reply of a reply: err = %v, want 404", err)
	}
}

func TestMessagePostValidation(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	cases := []struct {
		name string
		body any
	}{
		{"no body property", map[string]any{"subject": "x"}},
		{"hosted content without a temporaryId", map[string]any{
			"body":           map[string]string{"content": "<p>x</p>"},
			"hostedContents": []any{map[string]any{"contentBytes": "AAA=", "contentType": "image/png"}},
		}},
		{"malformed json", "not an object"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.Do(context.Background(), graph.Request{
				Method: http.MethodPost, Path: "/teams/t-eng/channels/c-general/messages", Body: tc.body,
			}); errStatus(err) != 400 {
				t.Fatalf("err = %v, want 400", err)
			}
		})
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost, Path: "/teams/t-eng/channels/nope/messages", Body: messageBody("x"),
	}); errStatus(err) != 404 {
		t.Fatalf("unknown channel: err = %v, want 404", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost, Path: "/chats/nope/messages", Body: messageBody("x"),
	}); errStatus(err) != 404 {
		t.Fatalf("unknown chat: err = %v, want 404", err)
	}
	// PATCH needs something to change.
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPatch, Path: "/teams/t-eng/channels/c-general/messages/cm-1", Body: map[string]any{},
	}); errStatus(err) != 400 {
		t.Fatalf("empty patch: err = %v, want 400", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPatch, Path: "/teams/t-eng/channels/c-general/messages/nope", Body: messageBody("x"),
	}); errStatus(err) != 404 {
		t.Fatalf("patch of an unknown message: err = %v, want 404", err)
	}
}

func TestHostedContentErrors(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodGet, Path: "/chats/chat-1on1/messages/gm-hc/hostedContents/nope/$value",
	}); errStatus(err) != 404 {
		t.Fatalf("unknown hosted content: err = %v, want 404", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodGet, Path: "/chats/chat-1on1/messages/nope/hostedContents",
	}); errStatus(err) != 404 {
		t.Fatalf("hosted contents of an unknown message: err = %v, want 404", err)
	}
	// The chat hosted-content form is readable.
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodGet, Path: "/chats/chat-1on1/messages/gm-hc/hostedContents/hc-1/$value",
	}); err != nil {
		t.Fatalf("chat hosted content: %v", err)
	}
}

func TestMarkChatReadStateErrors(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost, Path: "/chats/nope/markChatReadForUser", Body: map[string]any{},
	}); errStatus(err) != 404 {
		t.Fatalf("unknown chat: err = %v, want 404", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost, Path: "/chats/chat-group/markChatReadForUser",
		Body: map[string]any{"user": map[string]string{"id": "ghost"}},
	}); errStatus(err) != 404 {
		t.Fatalf("unknown user: err = %v, want 404", err)
	}
	// An empty body is accepted: the action applies to the caller.
	if _, err := c.Do(context.Background(), graph.Request{
		Method: http.MethodPost, Path: "/chats/chat-group/markChatReadForUser",
	}); err != nil {
		t.Fatalf("empty body: %v", err)
	}
}
