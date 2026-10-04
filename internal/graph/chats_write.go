package graph

import (
	"context"
	"strings"
)

// This file wraps the Phase 4 chat writes: create a chat, add a member, delete a
// chat and the two per-user read-state actions. Every one of them is cited to
// its api-reference page.

// Chat member roles. The docs allow exactly two and require one on every member
// of a new chat: "Each member must be assigned a role of owner or guest"
// (refs/graph/api-reference/v1.0/api/chat-post.md:47). The CLI always sends
// owner: `member` is not an accepted value in the delegated flow, and a guest
// role is a tenant decision the CLI cannot make on the user's behalf.
const (
	RoleOwner = "owner"
	RoleGuest = "guest"
)

// ChatTypes the create endpoint accepts. The chat *resource* also knows
// meeting and unknownFutureValue, but a create only takes these two
// (refs/graph/api-reference/v1.0/api/chat-post.md:50 against
// refs/graph/api-reference/v1.0/resources/chat.md:67).
const (
	ChatTypeOneOnOne = "oneOnOne"
	ChatTypeGroup    = "group"
)

// aadUserConversationMember is the @odata.type every member of a chat carries
// (refs/graph/api-reference/v1.0/resources/aaduserconversationmember.md).
const aadUserConversationMember = "#microsoft.graph.aadUserConversationMember"

// ChatMemberSpec is one participant of a chat to create.
type ChatMemberSpec struct {
	// UserID is the participant's directory object id.
	UserID string
	// DisplayName is the participant's name, used by the CLI to report who was
	// added. Graph takes the name from the directory, so it is never sent.
	DisplayName string
	// Roles are the conversation roles; empty means owner.
	Roles []string
}

// ChatCreate is the body of a chat create.
type ChatCreate struct {
	// ChatType is "oneOnOne" or "group". One-on-one is unique: creating one for
	// a pair that already has one returns the existing chat
	// (refs/graph/api-reference/v1.0/api/chat-post.md:16).
	ChatType string
	// Topic is only valid on a group chat (:47).
	Topic string
	// Members must list everyone, including the caller (:47).
	Members []ChatMemberSpec
}

// chatMemberWire is aadUserConversationMember on the wire.
//
// The two address forms are both documented and are not interchangeable:
// creating a chat uses `user@odata.bind` with the key form
// (refs/graph/api-reference/v1.0/api/chat-post.md:615-620), while adding a member
// uses the plain object path
// (refs/graph/api-reference/v1.0/api/chat-post-members.md:68-71).
type chatMemberWire struct {
	ODataType string   `json:"@odata.type"`
	Roles     []string `json:"roles"`
	UserBind  string   `json:"user@odata.bind,omitempty"`
	UserID    string   `json:"userId,omitempty"`
}

// chatCreateWire is the POST /chats body.
type chatCreateWire struct {
	ChatType string           `json:"chatType"`
	Topic    string           `json:"topic,omitempty"`
	Members  []chatMemberWire `json:"members"`
}

// markChatWire is the markChatReadForUser / markChatUnreadForUser body: the user
// whose read state changes, and for the unread action an optional watermark
// (refs/graph/api-reference/v1.0/api/chat-markchatreadforuser.md:37,
// chat-markchatunreadforuser.md:37-44).
type markChatWire struct {
	User *markChatUser `json:"user,omitempty"`
}

type markChatUser struct {
	ID               string `json:"id,omitempty"`
	TenantID         string `json:"tenantId,omitempty"`
	UserIdentityType string `json:"userIdentityType,omitempty"`
}

// CreateChat creates a chat and returns it. The response is the documented 201
// with the chat resource, including its members.
func (c *Client) CreateChat(ctx context.Context, in ChatCreate) (Chat, error) {
	if in.ChatType != ChatTypeOneOnOne && in.ChatType != ChatTypeGroup {
		return Chat{}, usageError("graph: a chat's type must be oneOnOne or group")
	}
	if in.ChatType == ChatTypeOneOnOne && len(in.Members) != 2 {
		return Chat{}, usageError("graph: a one-on-one chat has exactly two members")
	}
	if len(in.Members) == 0 {
		return Chat{}, usageError("graph: a chat needs its members, including the caller")
	}
	body := chatCreateWire{
		ChatType: in.ChatType,
		Members:  make([]chatMemberWire, 0, len(in.Members)),
	}
	// topic is documented as group-only, so an accidental one is dropped rather
	// than sent and rejected.
	if in.ChatType == ChatTypeGroup {
		body.Topic = strings.TrimSpace(in.Topic)
	}
	for _, member := range in.Members {
		if member.UserID == "" {
			return Chat{}, usageError("graph: every chat member needs a user id")
		}
		roles := member.Roles
		if len(roles) == 0 {
			roles = []string{RoleOwner}
		}
		body.Members = append(body.Members, chatMemberWire{
			ODataType: aadUserConversationMember,
			Roles:     roles,
			UserBind:  c.userBind(member.UserID, true),
		})
	}
	var out Chat
	if err := c.Post(ctx, "/chats", body, &out); err != nil {
		return Chat{}, err
	}
	return out, nil
}

// AddChatMember adds one member to an existing chat and returns the created
// member (the documented 201). The role defaults to owner for the same reason
// CreateChat's does.
func (c *Client) AddChatMember(ctx context.Context, chatID, userID string, roles []string) (ConversationMember, error) {
	if strings.TrimSpace(userID) == "" {
		return ConversationMember{}, usageError("graph: a chat member needs a user id")
	}
	if len(roles) == 0 {
		roles = []string{RoleOwner}
	}
	body := chatMemberWire{
		ODataType: aadUserConversationMember,
		Roles:     roles,
		UserBind:  c.userBind(userID, false),
	}
	var out ConversationMember
	if err := c.Post(ctx, "/chats/"+segment(chatID)+"/members", body, &out); err != nil {
		return ConversationMember{}, err
	}
	return out, nil
}

// DeleteChat deletes a chat (DELETE /chats/{chat-id}, 204 No Content).
//
// The scope it needs, Chat.ManageDeletion.All, is admin-consented and is not in
// any preset: the CLI asks for it incrementally when this command runs
// (internal/config/scopes.go, IncrementalScopes).
func (c *Client) DeleteChat(ctx context.Context, chatID string) error {
	return c.Delete(ctx, "/chats/"+segment(chatID))
}

// MarkChatRead marks a chat read for one user
// (POST /chats/{chat-id}/markChatReadForUser, 204).
func (c *Client) MarkChatRead(ctx context.Context, chatID, userID string) error {
	return c.markChat(ctx, chatID, "markChatReadForUser", userID)
}

// MarkChatUnread marks a chat unread for one user
// (POST /chats/{chat-id}/markChatUnreadForUser, 204). Leaving
// lastMessageReadDateTime out is documented as "the last message would be marked
// as unread" (refs/graph/api-reference/v1.0/api/chat-markchatunreadforuser.md:47).
func (c *Client) MarkChatUnread(ctx context.Context, chatID, userID string) error {
	return c.markChat(ctx, chatID, "markChatUnreadForUser", userID)
}

func (c *Client) markChat(ctx context.Context, chatID, action, userID string) error {
	body := markChatWire{}
	if strings.TrimSpace(userID) != "" {
		// The documented example carries id and tenantId; the id is what
		// identifies whose read state changes, and a delegated call is always
		// the caller's own tenant, so tenantId adds nothing here
		// (refs/graph/api-reference/v1.0/api/chat-markchatreadforuser.md:71-76).
		body.User = &markChatUser{ID: userID, UserIdentityType: "aadUser"}
	}
	return c.Post(ctx, "/chats/"+segment(chatID)+"/"+action, body, nil)
}

// userBind builds the `user@odata.bind` value a chat member carries, against
// this client's own service root so a national cloud gets its own URL.
//
// keyed selects the `users('{id}')` form, which is what the create-chat example
// sends (refs/graph/api-reference/v1.0/api/chat-post.md:615-620); the plain
// `/users/{id}` form is what the add-member example sends
// (refs/graph/api-reference/v1.0/api/chat-post-members.md:68-71).
func (c *Client) userBind(userID string, keyed bool) string {
	id := segment(userID)
	if keyed {
		return c.baseURL + "/users('" + id + "')"
	}
	return c.baseURL + "/users/" + id
}
