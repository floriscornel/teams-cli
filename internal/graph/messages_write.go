package graph

import (
	"context"
	"net/url"
	"strings"
)

// This file wraps the Phase 4 message writes: post, reply, edit, soft delete and
// the two reaction actions. Every path and body shape is the api-reference's,
// cited per function, and the fake asserts the same rules (fakegraph's
// handlers_messages.go).
//
// Two container quirks the docs force, both of which the CLI relies on:
//
//   - a chat has no threads, so a "reply" there is replyWithQuote: the body
//     carries the message being quoted in messageIds and the new message in
//     replyMessage (refs/graph/api-reference/v1.0/api/chatmessage-replywithquote.md:52-55),
//     with at most ten quoted messages (:18);
//   - a chat message's soft delete is documented only under the user-relative
//     path, /users/{user-id}/chats/{chat-id}/messages/{chat-message-id}/softDelete
//     (refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md:58), and the
//     spike got 405 from the /chats form (docs/spike/phase1.md:99). SoftDelete
//     therefore needs the signed-in user's id, which MessageTarget carries as
//     UserID for that one case.

// MessageTarget identifies the message a write acts on.
//
// A channel message needs its team and channel; a chat message needs the chat.
// A *reply* is addressed by its root: ReplyID is the reply itself and MessageID
// is the root message it hangs under, because every reply route takes the root
// in the path (refs/graph/api-reference/v1.0/api/chatmessage-update.md:33-34).
type MessageTarget struct {
	TeamID    string
	ChannelID string
	ChatID    string
	// UserID is the signed-in user's id, needed only by SoftDelete on a chat
	// message (see the file comment). It is never used for a channel message.
	UserID string
	// MessageID is the root message for a channel target and the message
	// itself for a chat target.
	MessageID string
	// ReplyID is set when the target is a reply inside a channel thread.
	ReplyID string
}

// IsChat reports whether the target is a chat message.
func (t MessageTarget) IsChat() bool { return t.ChatID != "" }

// path is the message's own route, for PATCH, softDelete and the reactions.
func (t MessageTarget) path() (string, error) {
	switch {
	case t.IsChat():
		if t.MessageID == "" {
			return "", usageError("graph: the chat message id is required")
		}
		return ChatMessagePath(t.ChatID, t.MessageID), nil
	case t.TeamID != "" && t.ChannelID != "" && t.MessageID != "":
		if t.ReplyID != "" {
			return ChannelMessagePath(t.TeamID, t.ChannelID, t.MessageID) + "/replies/" + segment(t.ReplyID), nil
		}
		return ChannelMessagePath(t.TeamID, t.ChannelID, t.MessageID), nil
	default:
		return "", usageError("graph: a message needs a chat, or a team, a channel and a message id")
	}
}

// collectionPath is the route a new message is POSTed to: the chat's message
// collection or the channel's.
func (t MessageTarget) collectionPath() (string, error) {
	switch {
	case t.IsChat():
		return "/chats/" + segment(t.ChatID) + "/messages", nil
	case t.TeamID != "" && t.ChannelID != "":
		return "/teams/" + segment(t.TeamID) + "/channels/" + segment(t.ChannelID) + "/messages", nil
	default:
		return "", usageError("graph: a message needs a chat, or a team and a channel")
	}
}

// MessagePost is the body of a message create. Only body is mandatory
// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:68); everything else is
// sent only when the command asked for it.
//
// Mentions, attachments and hostedContents are the three collections the same
// POST creates in one call, which is what makes an inline image possible at all:
// there is no v1.0 create endpoint for hosted content (refs/INDEX.md, "Inline
// images have no create endpoint in the v1.0 API reference").
type MessagePost struct {
	// Subject is stored by both containers, which the spike confirmed
	// (docs/spike/phase1.md:90,94; PLAN.md:241).
	Subject string `json:"subject,omitempty"`
	// Body is the message text; contentType is "html" or "text"
	// (refs/graph/api-reference/v1.0/resources/itembody.md).
	Body ItemBody `json:"body"`
	// Importance is "normal", "high" or "urgent"
	// (refs/graph/api-reference/v1.0/resources/chatmessage.md:75).
	Importance string `json:"importance,omitempty"`
	// Mentions carries one entry per mentioned person, and each entry's id is
	// the index of its <at id="N"> tag in Body.
	Mentions []Mention `json:"mentions,omitempty"`
	// Attachments are file references: {"contentType":"reference", ...}
	// (refs/graph/api-reference/v1.0/resources/chatmessageattachment.md).
	Attachments []Attachment `json:"attachments,omitempty"`
	// HostedContents are the inline images, whose temporaryId must equal the id
	// the body references as ../hostedContents/{temporaryId}/$value
	// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:731).
	HostedContents []HostedContentUpload `json:"hostedContents,omitempty"`
}

// HostedContentUpload is one hostedContents[] entry of a message POST. Graph
// takes the bytes inline, base64-encoded, and caps a hosted content at 4 MB
// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:733,851).
type HostedContentUpload struct {
	TemporaryID  string `json:"@microsoft.graph.temporaryId"`
	ContentBytes string `json:"contentBytes,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
}

// MessagePatch is the PATCH body of an edit: the caller supplies a chatMessage
// with the properties it wants changed (refs/graph/api-reference/v1.0/api/chatmessage-update.md:44-45).
// The CLI sends the body, the subject and the importance; the response is the
// documented 204.
type MessagePatch struct {
	Body       *ItemBody `json:"body,omitempty"`
	Subject    *string   `json:"subject,omitempty"`
	Importance *string   `json:"importance,omitempty"`
}

// replyWithQuoteBody is the replyWithQuote request: the message being quoted in
// messageIds (at most ten) and the new message in replyMessage
// (refs/graph/api-reference/v1.0/api/chatmessage-replywithquote.md:52-55).
type replyWithQuoteBody struct {
	ReplyMessage MessagePost `json:"replyMessage"`
	MessageIDs   []string    `json:"messageIds"`
}

// reactionBody is the setReaction/unsetReaction request: the reaction as unicode
// (refs/graph/api-reference/v1.0/api/chatmessage-setreaction.md:68).
type reactionBody struct {
	ReactionType string `json:"reactionType"`
}

// MaxQuotedMessages is the documented cap on replyWithQuote's messageIds
// (refs/graph/api-reference/v1.0/api/chatmessage-replywithquote.md:18).
const MaxQuotedMessages = 10

// PostMessage creates a message in a channel or a chat.
func (c *Client) PostMessage(ctx context.Context, target MessageTarget, post MessagePost) (Message, error) {
	path, err := target.collectionPath()
	if err != nil {
		return Message{}, err
	}
	var out Message
	if err := c.Post(ctx, path, post, &out); err != nil {
		return Message{}, err
	}
	return out, nil
}

// PostReply replies in a channel thread, or quote-replies in a chat (see the
// file comment). A chat reply quotes exactly the message being replied to.
func (c *Client) PostReply(ctx context.Context, target MessageTarget, post MessagePost) (Message, error) {
	if target.IsChat() {
		if target.MessageID == "" {
			return Message{}, usageError("graph: a chat reply needs the message it quotes")
		}
		return c.replyWithQuote(ctx, target.ChatID, []string{target.MessageID}, post)
	}
	if target.MessageID == "" {
		return Message{}, usageError("graph: a channel reply needs the message it replies to")
	}
	// The reply route is addressed by the root message; MessageTarget says so.
	path := ChannelMessagePath(target.TeamID, target.ChannelID, target.MessageID) + "/replies"
	var out Message
	if err := c.Post(ctx, path, post, &out); err != nil {
		return Message{}, err
	}
	return out, nil
}

// replyWithQuote posts a chat message that quotes earlier ones
// (POST /chats/{chat-id}/messages/replyWithQuote).
func (c *Client) replyWithQuote(ctx context.Context, chatID string, messageIDs []string, post MessagePost) (Message, error) {
	if len(messageIDs) == 0 {
		return Message{}, usageError("graph: replyWithQuote needs at least one quoted message")
	}
	if len(messageIDs) > MaxQuotedMessages {
		return Message{}, usageError("graph: replyWithQuote quotes at most 10 messages")
	}
	body := replyWithQuoteBody{ReplyMessage: post, MessageIDs: messageIDs}
	path := "/chats/" + segment(chatID) + "/messages/replyWithQuote"
	var out Message
	if err := c.Post(ctx, path, body, &out); err != nil {
		return Message{}, err
	}
	return out, nil
}

// UpdateMessage edits a message's body (and, when set, its subject).
// Delegated PATCH answers 204 No Content.
func (c *Client) UpdateMessage(ctx context.Context, target MessageTarget, patch MessagePatch) error {
	if patch.Body == nil && patch.Subject == nil && patch.Importance == nil {
		return usageError("graph: an edit must change the body, the subject or the importance")
	}
	path, err := target.path()
	if err != nil {
		return err
	}
	return c.Patch(ctx, path, patch, nil)
}

// SoftDeleteMessage removes a message the way Teams does: the message keeps its
// id and gains a deletedDateTime, and drops out of listings and search
// (refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md).
func (c *Client) SoftDeleteMessage(ctx context.Context, target MessageTarget) error {
	path, err := target.path()
	if err != nil {
		return err
	}
	if target.IsChat() {
		if target.UserID == "" {
			return usageError("graph: deleting a chat message needs the signed-in user's id")
		}
		path = "/users/" + segment(target.UserID) + "/chats/" + segment(target.ChatID) +
			"/messages/" + segment(target.MessageID) + "/softDelete"
	} else {
		path += "/softDelete"
	}
	return c.Post(ctx, path, nil, nil)
}

// React sets or removes one reaction on a message. remove selects unsetReaction.
//
// The reaction is a free-form string because the docs define reactionType as the
// reaction's unicode character, with "like" as the one named example
// (refs/graph/api-reference/v1.0/api/chatmessage-setreaction.md:68-71).
func (c *Client) React(ctx context.Context, target MessageTarget, reaction string, remove bool) error {
	if strings.TrimSpace(reaction) == "" {
		return usageError("graph: a reaction needs a reactionType")
	}
	path, err := target.path()
	if err != nil {
		return err
	}
	if remove {
		path += "/unsetReaction"
	} else {
		path += "/setReaction"
	}
	return c.Post(ctx, path, reactionBody{ReactionType: reaction}, nil)
}

// Me is the signed-in user, with the properties a write needs.
type Me struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName,omitempty"`
	UserPrincipalName string `json:"userPrincipalName,omitempty"`
	Mail              string `json:"mail,omitempty"`
}

// GetMe returns the signed-in user (GET /me, User.Read).
//
// A chat message's soft delete and chat creation both need the caller's own id:
// the documented delete route is user-relative, and a chat must list every
// participant including the caller
// (refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md:58,
// refs/graph/api-reference/v1.0/api/chat-post.md:47).
func (c *Client) GetMe(ctx context.Context) (Me, error) {
	var me Me
	err := c.Get(ctx, "/me", &me, WithQuery(url.Values{"$select": {"id,displayName,userPrincipalName,mail"}}))
	return me, err
}
