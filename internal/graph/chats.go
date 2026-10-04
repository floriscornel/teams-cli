package graph

import (
	"context"
	"net/url"
)

// This file wraps the chat reads of PLAN.md Phase 3 ("chat list/show/read",
// "chat list --unread", "teams unread").

// ChatQuery describes a chat listing.
type ChatQuery struct {
	// Top is the requested $top per page; it is clamped to the documented 50.
	Top int
	// Limit caps the total number of chats (0 means every page).
	Limit int
	// Members asks for $expand=members, which the docs cap at 25 member items
	// per chat regardless of $top (refs/graph/api-reference/v1.0/api/chat-list.md:20).
	Members bool
	// LastMessagePreview asks for $expand=lastMessagePreview, which carries the
	// newest message and is what makes the unread rule computable.
	LastMessagePreview bool
	// OrderByLastMessage asks for $orderby=lastMessagePreview/createdDateTime
	// desc, the only ordering the API supports (refs/graph/api-reference/v1.0/api/chat-list.md:63).
	OrderByLastMessage bool
	// Filter is a raw OData $filter (topic eq '...' / chatType eq '...'), which
	// the API documents but does not enumerate.
	Filter string
}

// ListChats returns the signed-in user's chats (GET /me/chats,
// refs/graph/api-reference/v1.0/api/chat-list.md).
func (c *Client) ListChats(ctx context.Context, q ChatQuery) ([]Chat, error) {
	query := url.Values{"$top": {intParam(topValue(q.Top, 50, MaxTopChats))}}
	var expand []string
	if q.Members {
		expand = append(expand, "members")
	}
	if q.LastMessagePreview {
		expand = append(expand, "lastMessagePreview")
	}
	if len(expand) > 0 {
		query.Set("$expand", joinComma(expand))
	}
	if q.OrderByLastMessage {
		query.Set("$orderby", "lastMessagePreview/createdDateTime desc")
	}
	if q.Filter != "" {
		query.Set("$filter", q.Filter)
	}
	return ListAll[Chat](ctx, c, "/me/chats", query, q.Limit)
}

// GetChat returns one chat (GET /me/chats/{chat-id},
// refs/graph/api-reference/v1.0/api/chat-get.md). Members asks for
// $expand=members so the caller can name the other participants.
func (c *Client) GetChat(ctx context.Context, chatID string, members bool) (Chat, error) {
	var chat Chat
	opts := []RequestOption{}
	if members {
		opts = append(opts, WithQuery(url.Values{"$expand": {"members"}}))
	}
	err := c.Get(ctx, "/me/chats/"+segment(chatID), &chat, opts...)
	return chat, err
}

// ListChatMembers returns a chat's members (GET /chats/{chat-id}/members,
// refs/graph/api-reference/v1.0/api/chat-list-members.md).
func (c *Client) ListChatMembers(ctx context.Context, chatID string) ([]ConversationMember, error) {
	return ListAll[ConversationMember](ctx, c, "/chats/"+segment(chatID)+"/members", nil, 0)
}

// ChatFilterTopic builds the documented $filter for a chat topic.
func ChatFilterTopic(topic string) string { return "topic eq " + ODataQuote(topic) }

// ChatFilterType builds the documented $filter for a chat type.
func ChatFilterType(chatType string) string { return "chatType eq " + ODataQuote(chatType) }

// joinComma renders a $expand list.
func joinComma(values []string) string {
	out := ""
	for i, v := range values {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
