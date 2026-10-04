// Tests for the Phase 4 write wrappers: messages_write.go, chats_write.go and
// files_write.go.
//
// They go through the real client against the real fake Graph server, exactly
// like the read tests (read_test.go documents the setup), and the fake's
// contract hook validates every request and response against the vendored
// OpenAPI subset. The colon-addressed upload routes are the one exception: they
// are fake-only (Microsoft's description does not model colon addressing), so
// the fake exempts them and the assertions here check the bytes on the wire
// instead.
package graph

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// writeModel seeds one channel with a root message, one reply, a 1:1 chat and a
// group chat, plus the user's own drive.
func writeModel() fakegraph.Model {
	return fakegraph.Model{
		Me: writeMe,
		Users: []fakegraph.User{
			{ID: writeMe, DisplayName: "Me"},
			{ID: writeBob, DisplayName: "Bob Builder", Mail: "bob@example.com"},
			{ID: writeCarol, DisplayName: "Carol Chen", Mail: "carol@example.com"},
		},
		Teams: []fakegraph.Team{{
			ID: writeTeamID, DisplayName: "Engineering",
			Channels: []fakegraph.Channel{{
				ID: writeChannelID, DisplayName: "General",
				Messages: []fakegraph.Message{
					{ID: writeRoot, AuthorID: writeMe, Created: readT1, Body: "<p>root</p>"},
					{ID: writeOther, AuthorID: writeBob, Created: readT2, Body: "<p>other</p>", Replies: []fakegraph.Message{
						{ID: writeReply, AuthorID: writeMe, Created: readT3, Body: "<p>reply</p>"},
					}},
				},
			}},
		}},
		Chats: []fakegraph.Chat{
			{
				ID: writeChat, ChatType: fakegraph.ChatTypeOneOnOne, Members: []fakegraph.Member{{UserID: writeMe}, {UserID: writeBob}},
				Messages: []fakegraph.Message{{ID: writeChatMsg, AuthorID: writeBob, Created: readT1, Body: "<p>hi</p>"}},
			},
			{
				ID: writeGroup, ChatType: fakegraph.ChatTypeGroup, Topic: "Release train",
				Members: []fakegraph.Member{{UserID: writeMe}, {UserID: writeBob}},
			},
		},
		MyDriveID: writeMyDrive,
		Drives: []fakegraph.Drive{{ID: writeMyDrive, Items: []fakegraph.DriveItem{
			{ID: "root-file", Name: "existing.txt", ParentID: "root", Content: []byte("hello"), ContentType: "text/plain"},
		}}},
	}
}

const (
	writeMe        = "u-me"
	writeBob       = "u-bob"
	writeTeamID    = "t-eng"
	writeChannelID = "c-general"
	writeRoot      = "cm-root"
	writeOther     = "cm-other"
	writeReply     = "cr-1"
	writeChat      = "chat-1on1"
	writeChatMsg   = "gm-1"
	writeGroup     = "chat-group"
	writeMyDrive   = "drive-me"
	writeCarol     = "u-carol"
)

func writeSetup(t *testing.T, mutate ...func(*fakegraph.Options)) (*fakegraph.Server, *Client) {
	t.Helper()
	srv := readServer(t, writeModel(), mutate...)
	return srv, readClient(t, srv)
}

func TestMessageTargetPath(t *testing.T) {
	tests := []struct {
		name   string
		target MessageTarget
		want   string
	}{
		{
			name:   "channel root",
			target: MessageTarget{TeamID: "t", ChannelID: "c", MessageID: "m"},
			want:   "/teams/t/channels/c/messages/m",
		},
		{
			name:   "channel reply is addressed by its root",
			target: MessageTarget{TeamID: "t", ChannelID: "c", MessageID: "root", ReplyID: "reply"},
			want:   "/teams/t/channels/c/messages/root/replies/reply",
		},
		{
			name:   "chat message",
			target: MessageTarget{ChatID: "19:c@thread.v2", MessageID: "m"},
			want:   "/chats/19:c@thread.v2/messages/m",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.target.path()
			if err != nil {
				t.Fatalf("path: %v", err)
			}
			if got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMessageTargetPathRejectsIncompleteTargets(t *testing.T) {
	tests := map[string]MessageTarget{
		"nothing":     {},
		"no message":  {TeamID: "t", ChannelID: "c"},
		"no chat id":  {MessageID: "m"},
		"chat no msg": {ChatID: "19:c@thread.v2"},
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := target.path()
			// readUsage fails the test when err is not the CLI's usage error;
			// its value is checked so the call is not a bare statement.
			if readUsage(t, err) == nil {
				t.Fatal("want a usage error")
			}
		})
	}
}

func TestPostMessageCarriesTheWholePayload(t *testing.T) {
	srv, c := writeSetup(t)
	ctx := context.Background()

	post := MessagePost{
		Subject:    "Deploy",
		Body:       ItemBody{Content: `<p>hello <at id="0">Bob</at></p>`, ContentType: "html"},
		Importance: "high",
		Mentions: []Mention{{
			ID: 0, MentionText: "Bob Builder",
			Mentioned: &MentionedIdentitySet{User: &TeamworkUserIdentity{
				ID: writeBob, DisplayName: "Bob Builder", UserIdentityType: "aadUser",
			}},
		}},
		Attachments:    []Attachment{{ID: "att-1", ContentType: "reference", ContentURL: "https://x/y", Name: "y.txt"}},
		HostedContents: []HostedContentUpload{HostedContentUploadFor("1", "image/png", []byte("png"))},
	}
	// The fake refuses a hostedContents[] entry whose temporaryId the body does
	// not reference, so the body names it.
	post.Body.Content = `<p>hello <at id="0">Bob</at></p><p><img src="../hostedContents/1/$value"></p>`

	msg, err := c.PostMessage(ctx, MessageTarget{TeamID: writeTeamID, ChannelID: writeChannelID}, post)
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if msg.ID == "" {
		t.Fatal("PostMessage returned no id")
	}
	call := readCalls(t, srv, http.MethodPost, "/teams/"+writeTeamID+"/channels/"+writeChannelID+"/messages")[0]
	var sent MessagePost
	if err := call.DecodeBody(&sent); err != nil {
		t.Fatalf("decode the recorded body: %v", err)
	}
	if sent.Subject != "Deploy" || sent.Importance != "high" {
		t.Errorf("body = %+v, want the subject and importance", sent)
	}
	if len(sent.Mentions) != 1 || sent.Mentions[0].Mentioned.User.DisplayName != "Bob Builder" ||
		sent.Mentions[0].Mentioned.User.UserIdentityType != "aadUser" {
		t.Errorf("mentions = %+v, want the documented user shape", sent.Mentions)
	}
	if len(sent.HostedContents) != 1 || sent.HostedContents[0].TemporaryID != "1" {
		t.Errorf("hostedContents = %+v", sent.HostedContents)
	}
	if len(sent.Attachments) != 1 || sent.Attachments[0].ContentType != "reference" {
		t.Errorf("attachments = %+v", sent.Attachments)
	}
}

func TestPostMessageToChat(t *testing.T) {
	srv, c := writeSetup(t)
	msg, err := c.PostMessage(context.Background(), MessageTarget{ChatID: writeChat},
		MessagePost{Body: ItemBody{Content: "<p>hi</p>", ContentType: "html"}})
	if err != nil {
		t.Fatalf("PostMessage: %v", err)
	}
	if msg.ID == "" {
		t.Fatal("no id returned")
	}
	readCalls(t, srv, http.MethodPost, "/chats/"+writeChat+"/messages")
}

func TestPostReplyChannelAndChat(t *testing.T) {
	srv, c := writeSetup(t)
	ctx := context.Background()

	// A channel reply goes to the root's replies collection.
	if _, err := c.PostReply(ctx, MessageTarget{TeamID: writeTeamID, ChannelID: writeChannelID, MessageID: writeRoot},
		MessagePost{Body: ItemBody{Content: "<p>answer</p>", ContentType: "html"}}); err != nil {
		t.Fatalf("PostReply (channel): %v", err)
	}
	readCalls(t, srv, http.MethodPost, "/teams/"+writeTeamID+"/channels/"+writeChannelID+"/messages/"+writeRoot+"/replies")

	// Replying to a reply stays in the root's thread: the route is the root's.
	if _, err := c.PostReply(ctx, MessageTarget{TeamID: writeTeamID, ChannelID: writeChannelID, MessageID: writeRoot, ReplyID: writeReply},
		MessagePost{Body: ItemBody{Content: "<p>again</p>", ContentType: "html"}}); err != nil {
		t.Fatalf("PostReply (reply): %v", err)
	}

	// A chat reply is a quote reply.
	msg, err := c.PostReply(ctx, MessageTarget{ChatID: writeChat, MessageID: writeChatMsg},
		MessagePost{Body: ItemBody{Content: "<p>quoted answer</p>", ContentType: "html"}})
	if err != nil {
		t.Fatalf("PostReply (chat): %v", err)
	}
	if msg.ID == "" {
		t.Fatal("the quote reply returned no id")
	}
	call := readCalls(t, srv, http.MethodPost, "/chats/"+writeChat+"/messages/replyWithQuote")[0]
	var body struct {
		MessageIDs   []string    `json:"messageIds"`
		ReplyMessage MessagePost `json:"replyMessage"`
	}
	if err := call.DecodeBody(&body); err != nil {
		t.Fatalf("decode the recorded body: %v", err)
	}
	if len(body.MessageIDs) != 1 || body.MessageIDs[0] != writeChatMsg {
		t.Errorf("messageIds = %v, want the quoted message", body.MessageIDs)
	}
	if body.ReplyMessage.Body.Content != "<p>quoted answer</p>" {
		t.Errorf("replyMessage = %+v", body.ReplyMessage)
	}
}

func TestPostReplyRejectsAnIncompleteTarget(t *testing.T) {
	_, c := writeSetup(t)
	if _, err := c.PostReply(context.Background(), MessageTarget{TeamID: writeTeamID, ChannelID: writeChannelID},
		MessagePost{Body: ItemBody{Content: "x", ContentType: "text"}}); err == nil {
		t.Fatal("want a usage error for a channel reply with no message")
	}
	if _, err := c.PostReply(context.Background(), MessageTarget{ChatID: writeChat},
		MessagePost{Body: ItemBody{Content: "x", ContentType: "text"}}); err == nil {
		t.Fatal("want a usage error for a chat reply with no quoted message")
	}
}

func TestUpdateMessage(t *testing.T) {
	srv, c := writeSetup(t)
	ctx := context.Background()
	body := ItemBody{Content: "<p>edited</p>", ContentType: "html"}
	subject := "new subject"
	if err := c.UpdateMessage(ctx, MessageTarget{TeamID: writeTeamID, ChannelID: writeChannelID, MessageID: writeRoot},
		MessagePatch{Body: &body, Subject: &subject}); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}
	call := readCalls(t, srv, http.MethodPatch, "/teams/"+writeTeamID+"/channels/"+writeChannelID+"/messages/"+writeRoot)[0]
	var sent MessagePatch
	if err := call.DecodeBody(&sent); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sent.Body == nil || sent.Body.Content != "<p>edited</p>" || sent.Subject == nil || *sent.Subject != "new subject" {
		t.Errorf("patch = %+v, want the body and subject", sent)
	}

	// An empty patch is a usage error rather than a request Graph would reject.
	if err := c.UpdateMessage(ctx, MessageTarget{ChatID: writeChat, MessageID: writeChatMsg}, MessagePatch{}); err == nil {
		t.Fatal("want a usage error for an empty patch")
	}
}

func TestSoftDeleteMessageUsesTheDocumentedChatRoute(t *testing.T) {
	srv, c := writeSetup(t)
	ctx := context.Background()

	if err := c.SoftDeleteMessage(ctx, MessageTarget{TeamID: writeTeamID, ChannelID: writeChannelID, MessageID: writeOther}); err != nil {
		t.Fatalf("SoftDeleteMessage (channel): %v", err)
	}
	readCalls(t, srv, http.MethodPost, "/teams/"+writeTeamID+"/channels/"+writeChannelID+"/messages/"+writeOther+"/softDelete")

	// A chat message needs the signed-in user's id: the documented route is
	// user-relative and the /chats form answers 405 live (docs/spike/phase1.md:99).
	if err := c.SoftDeleteMessage(ctx, MessageTarget{ChatID: writeChat, MessageID: writeChatMsg}); err == nil {
		t.Fatal("want a usage error without the user id")
	}
	if err := c.SoftDeleteMessage(ctx, MessageTarget{ChatID: writeChat, MessageID: writeChatMsg, UserID: writeMe}); err != nil {
		t.Fatalf("SoftDeleteMessage (chat): %v", err)
	}
	readCalls(t, srv, http.MethodPost, "/users/"+writeMe+"/chats/"+writeChat+"/messages/"+writeChatMsg+"/softDelete")
}

func TestReactSetsAndRemoves(t *testing.T) {
	srv, c := writeSetup(t)
	ctx := context.Background()
	target := MessageTarget{ChatID: writeChat, MessageID: writeChatMsg}

	if err := c.React(ctx, target, "👍", false); err != nil {
		t.Fatalf("React: %v", err)
	}
	call := readCalls(t, srv, http.MethodPost, "/chats/"+writeChat+"/messages/"+writeChatMsg+"/setReaction")[0]
	var sent reactionBody
	if err := call.DecodeBody(&sent); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sent.ReactionType != "👍" {
		t.Errorf("reactionType = %q", sent.ReactionType)
	}
	if err := c.React(ctx, target, "👍", true); err != nil {
		t.Fatalf("React (remove): %v", err)
	}
	readCalls(t, srv, http.MethodPost, "/chats/"+writeChat+"/messages/"+writeChatMsg+"/unsetReaction")

	if err := c.React(ctx, target, "  ", false); err == nil {
		t.Fatal("want a usage error for an empty reaction")
	}
}

func TestCreateChatAddMemberDeleteAndMark(t *testing.T) {
	srv, c := writeSetup(t)
	ctx := context.Background()

	chat, err := c.CreateChat(ctx, ChatCreate{
		ChatType: ChatTypeGroup,
		Topic:    "Launch",
		Members: []ChatMemberSpec{
			{UserID: writeMe, Roles: []string{RoleOwner}},
			{UserID: writeBob},
		},
	})
	if err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	if chat.ID == "" || chat.Topic == nil || *chat.Topic != "Launch" {
		t.Fatalf("chat = %+v, want the created group chat", chat)
	}
	call := readCalls(t, srv, http.MethodPost, "/chats")[0]
	var sent struct {
		ChatType string `json:"chatType"`
		Topic    string `json:"topic"`
		Members  []struct {
			ODataType string   `json:"@odata.type"`
			Roles     []string `json:"roles"`
			UserBind  string   `json:"user@odata.bind"`
		} `json:"members"`
	}
	if err := call.DecodeBody(&sent); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if sent.ChatType != "group" || sent.Topic != "Launch" || len(sent.Members) != 2 {
		t.Errorf("create body = %+v", sent)
	}
	// The documented address form is the keyed one, against our own service
	// root, and every member carries a role.
	for _, member := range sent.Members {
		if !strings.Contains(member.UserBind, "/users('") || member.ODataType == "" || len(member.Roles) == 0 {
			t.Errorf("member = %+v, want the documented bind, type and role", member)
		}
	}

	// A one-on-one chat needs exactly two members.
	if _, err := c.CreateChat(ctx, ChatCreate{ChatType: ChatTypeOneOnOne, Members: []ChatMemberSpec{{UserID: writeMe}}}); err == nil {
		t.Fatal("want a usage error for a one-on-one chat with one member")
	}
	if _, err := c.CreateChat(ctx, ChatCreate{ChatType: "meeting", Members: []ChatMemberSpec{{UserID: writeMe}}}); err == nil {
		t.Fatal("want a usage error for an unsupported chat type")
	}

	if _, err := c.AddChatMember(ctx, writeGroup, writeCarol, nil); err != nil {
		t.Fatalf("AddChatMember: %v", err)
	}
	memberCall := readCalls(t, srv, http.MethodPost, "/chats/"+writeGroup+"/members")[0]
	var member struct {
		Roles    []string `json:"roles"`
		UserBind string   `json:"user@odata.bind"`
	}
	if err := memberCall.DecodeBody(&member); err != nil {
		t.Fatalf("decode member: %v", err)
	}
	// The add-member example uses the plain object path, not the keyed form.
	if !strings.HasSuffix(member.UserBind, "/users/"+writeCarol) || len(member.Roles) != 1 || member.Roles[0] != RoleOwner {
		t.Errorf("member body = %+v", member)
	}

	if err := c.MarkChatUnread(ctx, writeChat, writeMe); err != nil {
		t.Fatalf("MarkChatUnread: %v", err)
	}
	unreadCall := readCalls(t, srv, http.MethodPost, "/chats/"+writeChat+"/markChatUnreadForUser")[0]
	var unread markChatWire
	if err := unreadCall.DecodeBody(&unread); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if unread.User == nil || unread.User.ID != writeMe {
		t.Errorf("markChatUnreadForUser body = %+v, want the caller", unread)
	}
	if err := c.MarkChatRead(ctx, writeChat, ""); err != nil {
		t.Fatalf("MarkChatRead: %v", err)
	}

	if err := c.DeleteChat(ctx, writeGroup); err != nil {
		t.Fatalf("DeleteChat: %v", err)
	}
	readCalls(t, srv, http.MethodDelete, "/chats/"+writeGroup)
}

func TestGetMe(t *testing.T) {
	_, c := writeSetup(t)
	me, err := c.GetMe(context.Background())
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if me.ID != writeMe {
		t.Errorf("me = %+v, want %s", me, writeMe)
	}
}

func TestUploadFileToChannelInOnePut(t *testing.T) {
	srv, c := writeSetup(t)
	ctx := context.Background()

	data := []byte("# notes\n")
	result, err := c.UploadFile(ctx, UploadTarget{TeamID: writeTeamID, ChannelID: writeChannelID}, "notes.txt", "text/plain", data)
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if result.AttachmentID == "" || result.WebURL == "" {
		t.Fatalf("result = %+v, want an attachment id and a URL", result)
	}
	if result.Size != int64(len(data)) {
		t.Errorf("size = %d, want %d", result.Size, len(data))
	}
	// The documented path creates a file that does not exist yet.
	call := readCalls(t, srv, http.MethodPut, "/drives/"+readDriveID+"/items/"+readFilesFolder+":/notes.txt:/content")[0]
	if !bytes.Equal(call.Body, data) {
		t.Errorf("uploaded body = %q, want the file's bytes", call.Body)
	}
	if got := call.Header.Get("Content-Type"); got != "text/plain" {
		t.Errorf("Content-Type = %q, want the file's own type", got)
	}

	// The file is really in the channel's folder afterwards.
	folder, items, err := c.ListChannelFiles(ctx, writeTeamID, writeChannelID)
	if err != nil {
		t.Fatalf("ListChannelFiles: %v", err)
	}
	if folder.ID == "" {
		t.Fatal("no files folder")
	}
	found := false
	for _, item := range items {
		if item.Name == "notes.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("items = %+v, want the uploaded file", items)
	}
}

func TestUploadFileToChatUsesASharingLink(t *testing.T) {
	srv, c := writeSetup(t)
	ctx := context.Background()

	result, err := c.UploadFile(ctx, UploadTarget{Chat: true}, "notes.txt", "text/plain", []byte("hi"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if !strings.Contains(result.WebURL, "_download") {
		t.Errorf("WebURL = %q, want the sharing link createLink returned", result.WebURL)
	}
	// The drive is resolved first, then the file goes into the Teams chat folder.
	readCalls(t, srv, http.MethodGet, "/me/drive")
	readCalls(t, srv, http.MethodPut, "/drives/"+writeMyDrive+"/items/root:/Microsoft Teams Chat Files/notes.txt:/content")
	readCalls(t, srv, http.MethodPost, "/drives/"+writeMyDrive+"/items/"+result.ItemID+"/createLink")
}

func TestUploadFileFallsBackToTheUploadURLWhenCreateLinkFails(t *testing.T) {
	// A tenant that refuses both org-wide and user links: the upload still
	// succeeds and the attachment keeps the file's own URL.
	srv, c := writeSetup(t, func(o *fakegraph.Options) {
		o.Faults = []fakegraph.Fault{
			{Method: http.MethodPost, Path: "/drives/" + writeMyDrive + "/items/*", Status: http.StatusForbidden, Call: 0},
		}
	})
	result, err := c.UploadFile(context.Background(), UploadTarget{Chat: true}, "notes.txt", "text/plain", []byte("hi"))
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if result.WebURL == "" || strings.Contains(result.WebURL, "createLink") {
		t.Errorf("WebURL = %q, want the upload's own URL as the fallback", result.WebURL)
	}
	if calls := srv.RequestsFor(http.MethodPost, "/drives/"+writeMyDrive+"/items/"+result.ItemID+"/createLink"); len(calls) != 2 {
		t.Errorf("createLink calls = %d, want the organization and users attempts", len(calls))
	}
}

func TestUploadLargeFileUsesASession(t *testing.T) {
	srv, c := writeSetup(t)
	// Just over the simple-upload ceiling, so the file spans two chunks and the
	// Content-Range arithmetic is visible.
	data := bytes.Repeat([]byte("x"), SimpleUploadMaxSize+10)

	result, err := c.UploadFile(context.Background(), UploadTarget{TeamID: writeTeamID, ChannelID: writeChannelID}, "big.bin", "application/octet-stream", data)
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if result.AttachmentID == "" {
		t.Error("no attachment id from the final response")
	}
	readCalls(t, srv, http.MethodPost, "/drives/"+readDriveID+"/items/"+readFilesFolder+":/big.bin:/createUploadSession")

	chunks := srv.RequestsFor(http.MethodPut, "")
	if len(chunks) == 0 {
		// The upload URL is /_upload/{id}, which is not a Graph path: find the
		// recorded PUTs by their path prefix instead.
		returned := readChunkCalls(t, srv)
		if len(returned) != 2 {
			t.Fatalf("chunk count = %d, want 2", len(returned))
		}
		wantFirst := fmt.Sprintf("bytes 0-%d/%d", UploadChunkSize-1, len(data))
		if got := returned[0].Header.Get("Content-Range"); got != wantFirst {
			t.Errorf("first Content-Range = %q, want %q", got, wantFirst)
		}
		wantSecond := fmt.Sprintf("bytes %d-%d/%d", UploadChunkSize, len(data)-1, len(data))
		if got := returned[1].Header.Get("Content-Range"); got != wantSecond {
			t.Errorf("second Content-Range = %q, want %q", got, wantSecond)
		}
		if got := returned[0].Header.Get("Authorization"); got != "" {
			t.Errorf("the chunk carried Authorization = %q, which the docs warn against", got)
		}
		if len(returned[0].Body) != UploadChunkSize || len(returned[1].Body) != len(data)-UploadChunkSize {
			t.Errorf("chunk sizes = %d, %d, want %d and %d", len(returned[0].Body), len(returned[1].Body),
				UploadChunkSize, len(data)-UploadChunkSize)
		}
	}
}

// readChunkCalls returns the recorded upload-session chunks, which live under
// the fake's own /_upload path rather than a Graph one.
func readChunkCalls(t *testing.T, srv *fakegraph.Server) []*fakegraph.RecordedRequest {
	t.Helper()
	var out []*fakegraph.RecordedRequest
	for _, rec := range srv.Requests() {
		if rec.Method == http.MethodPut && strings.HasPrefix(rec.Path, "/_upload/") {
			out = append(out, rec)
		}
	}
	return out
}

func TestAttachmentIDFromETag(t *testing.T) {
	tests := map[string]string{
		`"{B5765B1F-4D42-4C53-91A2-D49A14B0C8C3},2"`: "B5765B1F-4D42-4C53-91A2-D49A14B0C8C3",
		`"{AAAA-BBBB-CCCC},1"`:                       "AAAA-BBBB-CCCC",
		`"some-guid,3"`:                              "some-guid",
		`"plain"`:                                    "plain",
		`{}`:                                         "{}",
		"":                                           "",
	}
	for etag, want := range tests {
		if got := attachmentIDFromETag(etag); got != want {
			t.Errorf("attachmentIDFromETag(%q) = %q, want %q", etag, got, want)
		}
	}
}

func TestUploadFileRejectsBadTargets(t *testing.T) {
	_, c := writeSetup(t)
	ctx := context.Background()
	if _, err := c.UploadFile(ctx, UploadTarget{}, "x.txt", "text/plain", []byte("x")); err == nil {
		t.Error("want a usage error for a target that is neither a channel nor a chat")
	}
	if _, err := c.UploadFile(ctx, UploadTarget{Chat: true}, "  ", "text/plain", []byte("x")); err == nil {
		t.Error("want a usage error for an empty name")
	}
}
