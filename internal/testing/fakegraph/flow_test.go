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

// This file is the stateful end-to-end flow the brief asks for: post → read →
// react → edit → delete → search, plus the chat read state, a quote reply and
// the chat create/add-member/delete path. It runs against the real
// internal/graph client, the same way the testscript scripts will.

func TestWriteFlowPostReadReactEditDeleteSearch(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	ctx := context.Background()
	const channelMsgs = "/teams/t-eng/channels/c-general/messages"

	// --- post ---
	post := map[string]any{
		"subject": "Flow marker",
		"body": map[string]string{
			"contentType": "html",
			"content":     `<p>flowmarker <at id="0">Alice Example</at></p>`,
		},
		"mentions": []any{map[string]any{
			"id":          0,
			"mentionText": "Alice Example",
			"mentioned":   map[string]any{"user": map[string]any{"id": "u-alice", "displayName": "Alice Example", "userIdentityType": "aadUser"}},
		}},
	}
	resp, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: channelMsgs, Body: post})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("post status = %d, want 201", resp.StatusCode)
	}
	var created messageWire
	if err := resp.Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" {
		t.Fatal("post returned no message id")
	}
	if created.CreatedDateTime != graphTime(testNow) {
		t.Fatalf("createdDateTime = %q, want the frozen clock %q", created.CreatedDateTime, graphTime(testNow))
	}
	if len(created.Mentions) != 1 {
		t.Fatalf("mentions = %+v", created.Mentions)
	}

	// --- read ---
	list := mustPage[messageWire](t, c, channelMsgs, nil)
	if !containsID(idsOf(list.Value), created.ID) {
		t.Fatalf("the posted message is not in the channel list: %v", idsOf(list.Value))
	}
	fetched := getMessage(t, c, channelMsgs+"/"+created.ID)
	if !strings.Contains(fetched.Body.Content, "flowmarker") {
		t.Fatalf("fetched body = %q", fetched.Body.Content)
	}

	// --- react ---
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: channelMsgs + "/" + created.ID + "/setReaction",
		Body: map[string]string{"reactionType": "👍"}}); err != nil {
		t.Fatalf("setReaction: %v", err)
	}
	fetched = getMessage(t, c, channelMsgs+"/"+created.ID)
	if len(fetched.Reactions) != 1 || fetched.Reactions[0].ReactionType != "👍" {
		t.Fatalf("reactions = %+v", fetched.Reactions)
	}
	if fetched.Reactions[0].User == nil || fetched.Reactions[0].User.ID != "u-me" {
		t.Fatalf("reaction user = %+v", fetched.Reactions[0].User)
	}
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: channelMsgs + "/" + created.ID + "/unsetReaction",
		Body: map[string]string{"reactionType": "👍"}}); err != nil {
		t.Fatalf("unsetReaction: %v", err)
	}
	if got := getMessage(t, c, channelMsgs+"/"+created.ID); len(got.Reactions) != 0 {
		t.Fatalf("reactions after unset = %+v", got.Reactions)
	}

	// --- edit ---
	patch, err := c.Do(ctx, graph.Request{Method: http.MethodPatch, Path: channelMsgs + "/" + created.ID,
		Body: map[string]any{"body": map[string]string{"contentType": "html", "content": "<p>flowmarker edited</p>"}}})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if patch.StatusCode != http.StatusNoContent {
		t.Fatalf("patch status = %d, want 204", patch.StatusCode)
	}
	edited := getMessage(t, c, channelMsgs+"/"+created.ID)
	if !strings.Contains(edited.Body.Content, "edited") {
		t.Fatalf("edited body = %q", edited.Body.Content)
	}
	if edited.LastEditedDateTime == nil {
		t.Fatal("lastEditedDateTime is null after an edit")
	}
	if edited.Etag == created.Etag {
		t.Fatal("etag did not change after the edit")
	}

	// --- reply ---
	replyResp, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: channelMsgs + "/" + created.ID + "/replies",
		Body: map[string]any{"body": map[string]string{"contentType": "html", "content": "<p>a reply</p>"}}})
	if err != nil {
		t.Fatalf("reply: %v", err)
	}
	var reply messageWire
	if err := replyResp.Decode(&reply); err != nil {
		t.Fatal(err)
	}
	replies := mustPage[messageWire](t, c, channelMsgs+"/"+created.ID+"/replies", nil)
	if !containsID(idsOf(replies.Value), reply.ID) {
		t.Fatalf("replies = %v, want %s", idsOf(replies.Value), reply.ID)
	}
	// The whole thread is visible through $expand=replies.
	expanded := mustPage[messageWire](t, c, channelMsgs, url.Values{"$expand": {"replies"}})
	var withThread *messageWire
	for i := range expanded.Value {
		if expanded.Value[i].ID == created.ID {
			withThread = &expanded.Value[i]
		}
	}
	if withThread == nil || len(withThread.Replies) != 1 {
		t.Fatalf("expanded thread = %+v", withThread)
	}

	// --- search finds it before the delete ---
	if got := searchCount(t, c, "flowmarker"); got != 1 {
		t.Fatalf("search total = %d, want 1 before the delete", got)
	}

	// --- delete ---
	del, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: channelMsgs + "/" + created.ID + "/softDelete"})
	if err != nil {
		t.Fatalf("softDelete: %v", err)
	}
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("softDelete status = %d, want 204", del.StatusCode)
	}
	deleted := getMessage(t, c, channelMsgs+"/"+created.ID)
	if deleted.DeletedDateTime == nil {
		t.Fatal("deletedDateTime is null after softDelete")
	}
	after := mustPage[messageWire](t, c, channelMsgs, nil)
	if containsID(idsOf(after.Value), created.ID) {
		t.Fatal("a soft-deleted message is still listed")
	}
	if got := searchCount(t, c, "flowmarker"); got != 0 {
		t.Fatalf("search total = %d, want 0 after the delete", got)
	}
}

func TestWriteFlowChatReadStateAndQuoteReply(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	ctx := context.Background()
	const chatMsgs = "/chats/chat-1on1/messages"

	// --- post ---
	resp, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: chatMsgs,
		Body: map[string]any{"body": map[string]string{"contentType": "html", "content": "<p>chat flowmarker</p>"}}})
	if err != nil {
		t.Fatalf("post chat message: %v", err)
	}
	var created messageWire
	if err := resp.Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ChatID != "chat-1on1" {
		t.Fatalf("chatId = %q, want chat-1on1", created.ChatID)
	}

	// --- read ---
	page := mustPage[messageWire](t, c, chatMsgs, nil)
	if !containsID(idsOf(page.Value), created.ID) {
		t.Fatalf("posted chat message missing: %v", idsOf(page.Value))
	}

	// --- mark unread then read, checking viewpoint each time ---
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats/chat-1on1/markChatUnreadForUser",
		Body: map[string]any{"user": map[string]string{"id": "u-me"}}}); err != nil {
		t.Fatalf("markChatUnreadForUser: %v", err)
	}
	if vp := getChat(t, c).Viewpoint; vp == nil || vp.LastMessageReadDateTime != nil {
		t.Fatalf("viewpoint after mark-unread = %+v, want a null lastMessageReadDateTime", vp)
	}
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats/chat-1on1/markChatReadForUser",
		Body: map[string]any{"user": map[string]string{"id": "u-me"}}}); err != nil {
		t.Fatalf("markChatReadForUser: %v", err)
	}
	vp := getChat(t, c).Viewpoint
	if vp == nil || vp.LastMessageReadDateTime == nil {
		t.Fatalf("viewpoint after mark-read = %+v, want a timestamp", vp)
	}
	if *vp.LastMessageReadDateTime != graphTime(testNow) {
		t.Fatalf("lastMessageReadDateTime = %q, want the frozen clock %q", *vp.LastMessageReadDateTime, graphTime(testNow))
	}

	// The unread view is /me/chats?$expand=lastMessagePreview.
	chats := mustPage[chatWire](t, c, "/me/chats", url.Values{"$expand": {"lastMessagePreview"}})
	var withPreview *chatWire
	for i := range chats.Value {
		if chats.Value[i].ID == "chat-1on1" {
			withPreview = &chats.Value[i]
		}
	}
	if withPreview == nil || withPreview.LastMessagePreview == nil {
		t.Fatalf("lastMessagePreview missing: %+v", withPreview)
	}
	if withPreview.LastMessagePreview.ID != created.ID {
		t.Fatalf("lastMessagePreview = %q, want the newest message %q", withPreview.LastMessagePreview.ID, created.ID)
	}

	// --- quote reply ---
	quote, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats/chat-1on1/messages/replyWithQuote",
		Body: map[string]any{
			"messageIds": []string{created.ID},
			"replyMessage": map[string]any{
				"body": map[string]string{"contentType": "html", "content": "<p>quoting you</p>"},
			},
		}})
	if err != nil {
		t.Fatalf("replyWithQuote: %v", err)
	}
	if quote.StatusCode != http.StatusCreated {
		t.Fatalf("replyWithQuote status = %d, want 201", quote.StatusCode)
	}
	var quoted messageWire
	if err := quote.Decode(&quoted); err != nil {
		t.Fatal(err)
	}
	if quoted.ReplyToID != created.ID {
		t.Fatalf("replyToId = %q, want the quoted message %q", quoted.ReplyToID, created.ID)
	}

	// More than ten quoted messages is refused.
	tooMany := make([]string, 11)
	for i := range tooMany {
		tooMany[i] = created.ID
	}
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats/chat-1on1/messages/replyWithQuote",
		Body: map[string]any{"messageIds": tooMany, "replyMessage": map[string]any{"body": map[string]string{"content": "x"}}}}); errStatus(err) != 400 {
		t.Fatalf("11 quoted messages: err = %v, want 400", err)
	}

	// --- edit and delete the chat message through the chat path ---
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPatch, Path: chatMsgs + "/" + created.ID,
		Body: map[string]any{"body": map[string]string{"content": "<p>chat flowmarker edited</p>"}}}); err != nil {
		t.Fatalf("patch chat message: %v", err)
	}
	if got := getMessage(t, c, chatMsgs+"/"+created.ID); !strings.Contains(got.Body.Content, "edited") {
		t.Fatalf("chat message body = %q", got.Body.Content)
	}
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: chatMsgs + "/" + created.ID + "/softDelete"}); err != nil {
		t.Fatalf("softDelete chat message: %v", err)
	}
	if got := getMessage(t, c, chatMsgs+"/"+created.ID); got.DeletedDateTime == nil {
		t.Fatal("deletedDateTime is null after the chat soft delete")
	}
}

func TestWriteFlowChatCreateMembersAndDelete(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	ctx := context.Background()

	// A one-on-one chat needs every member to carry an owner or guest role
	// (refs/graph/api-reference/v1.0/api/chat-post.md:47; PLAN.md "Chat quirks").
	oneOnOne := map[string]any{
		"chatType": "oneOnOne",
		"members": []any{
			map[string]any{"@odata.type": "#microsoft.graph.aadUserConversationMember", "roles": []string{"owner"}, "userId": "u-me"},
			map[string]any{"@odata.type": "#microsoft.graph.aadUserConversationMember", "roles": []string{"owner"}, "userId": "u-bob"},
		},
	}
	resp, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats", Body: oneOnOne})
	if err != nil {
		t.Fatalf("create chat: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create chat status = %d, want 201", resp.StatusCode)
	}
	var created chatWire
	if err := resp.Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || len(created.Members) != 2 {
		t.Fatalf("created chat = %+v", created)
	}

	// Creating the same pair again returns the existing chat: "Only one
	// one-on-one chat can exist between two members".
	again, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats", Body: oneOnOne})
	if err != nil {
		t.Fatalf("create chat again: %v", err)
	}
	var duplicate chatWire
	if err := again.Decode(&duplicate); err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != created.ID {
		t.Fatalf("second create returned %q, want the existing %q", duplicate.ID, created.ID)
	}

	// A member without a role is refused.
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats", Body: map[string]any{
		"chatType": "group",
		"members":  []any{map[string]any{"userId": "u-me"}},
	}}); errStatus(err) != 400 {
		t.Fatalf("role-less member: err = %v, want 400", err)
	}

	// A one-on-one chat with three members is refused.
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats", Body: map[string]any{
		"chatType": "oneOnOne",
		"members": []any{
			map[string]any{"roles": []string{"owner"}, "userId": "u-me"},
			map[string]any{"roles": []string{"owner"}, "userId": "u-bob"},
			map[string]any{"roles": []string{"owner"}, "userId": "u-alice"},
		},
	}}); errStatus(err) != 400 {
		t.Fatalf("3-member one-on-one: err = %v, want 400", err)
	}

	// Add a member to a group chat.
	group, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats", Body: map[string]any{
		"chatType": "group",
		"topic":    "Flow group",
		"members": []any{
			map[string]any{"roles": []string{"owner"}, "userId": "u-me"},
			map[string]any{"roles": []string{"owner"}, "userId": "u-alice"},
		},
	}})
	if err != nil {
		t.Fatalf("create group chat: %v", err)
	}
	var groupChat chatWire
	if err := group.Decode(&groupChat); err != nil {
		t.Fatal(err)
	}
	add, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats/" + groupChat.ID + "/members", Body: map[string]any{
		"@odata.type":     "#microsoft.graph.aadUserConversationMember",
		"roles":           []string{"owner"},
		"user@odata.bind": "https://graph.microsoft.com/v1.0/users('u-bob')",
	}})
	if err != nil {
		t.Fatalf("add member: %v", err)
	}
	if add.StatusCode != http.StatusCreated {
		t.Fatalf("add member status = %d, want 201", add.StatusCode)
	}
	members := mustPage[conversationMemberWire](t, c, "/chats/"+groupChat.ID+"/members", nil)
	if len(members.Value) != 3 {
		t.Fatalf("members = %d, want 3", len(members.Value))
	}
	// Adding the same member twice is a conflict.
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats/" + groupChat.ID + "/members", Body: map[string]any{"userId": "u-bob"}}); errStatus(err) != 409 {
		t.Fatalf("duplicate member: err = %v, want 409", err)
	}

	// Delete the chat.
	del, err := c.Do(ctx, graph.Request{Method: http.MethodDelete, Path: "/chats/" + groupChat.ID})
	if err != nil {
		t.Fatalf("delete chat: %v", err)
	}
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", del.StatusCode)
	}
	after := mustPage[chatWire](t, c, "/me/chats", nil)
	if containsID(chatIDs(after.Value), groupChat.ID) {
		t.Fatal("a deleted chat is still listed")
	}
	// The documented soft-delete form works too, and the /chats form answers
	// 405 (docs/spike/phase1.md:98-99).
	soft, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/users/u-me/chats/" + created.ID + "/softDelete"})
	if err != nil {
		t.Fatalf("soft delete chat: %v", err)
	}
	if soft.StatusCode != http.StatusNoContent {
		t.Fatalf("soft delete status = %d, want 204", soft.StatusCode)
	}
	if _, err := c.Do(ctx, graph.Request{Method: http.MethodPost, Path: "/chats/" + created.ID + "/softDelete"}); errStatus(err) != http.StatusMethodNotAllowed {
		t.Fatalf("POST /chats/{id}/softDelete: err = %v, want 405", err)
	}
}

func TestWriteFlowFileDownloadAndUpload(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	ctx := context.Background()

	// The channel's filesFolder is derived from the seed.
	folderResp, err := c.Do(ctx, graph.Request{Method: http.MethodGet, Path: "/teams/t-eng/channels/c-general/filesFolder"})
	if err != nil {
		t.Fatalf("filesFolder: %v", err)
	}
	var folder driveItemWire
	if err := folderResp.Decode(&folder); err != nil {
		t.Fatal(err)
	}
	if folder.ID != "folder-c-general" || folder.Folder == nil {
		t.Fatalf("filesFolder = %+v", folder)
	}

	children := mustPage[driveItemWire](t, c, "/drives/drive-t-eng/items/"+folder.ID+"/children", nil)
	if len(children.Value) != 2 {
		t.Fatalf("children = %d, want 2", len(children.Value))
	}
	file := children.Value[0]
	if file.DownloadURL == "" {
		t.Fatal("child driveItem carries no @microsoft.graph.downloadUrl")
	}

	// Downloading follows the documented 302 to a pre-authenticated URL.
	download, err := c.Do(ctx, graph.Request{Method: http.MethodGet, Path: "/drives/drive-t-eng/items/" + file.ID + "/content"})
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(download.Body) != "%PDF-1.7 fake" {
		t.Fatalf("downloaded bytes = %q", download.Body)
	}

	// Simple upload replaces the content.
	put, err := c.Do(ctx, graph.Request{Method: http.MethodPut, Path: "/drives/drive-t-eng/items/" + file.ID + "/content", Body: json.RawMessage("new bytes")})
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if put.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", put.StatusCode)
	}
	again, err := c.Do(ctx, graph.Request{Method: http.MethodGet, Path: "/drives/drive-t-eng/items/" + file.ID + "/content"})
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Body) != "new bytes" {
		t.Fatalf("content after upload = %q", again.Body)
	}
}

// getMessage fetches one message through the real client.
func getMessage(t *testing.T, c *graph.Client, path string) messageWire {
	t.Helper()
	resp, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: path})
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	var msg messageWire
	if err := resp.Decode(&msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

// getChat fetches one chat.
func getChat(t *testing.T, c *graph.Client) chatWire {
	t.Helper()
	resp, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/chats/chat-1on1"})
	if err != nil {
		t.Fatalf("GET chat: %v", err)
	}
	var chat chatWire
	if err := resp.Decode(&chat); err != nil {
		t.Fatal(err)
	}
	return chat
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func chatIDs(chats []chatWire) []string {
	out := make([]string, 0, len(chats))
	for _, c := range chats {
		out = append(out, c.ID)
	}
	return out
}
