package graph

import (
	"context"
	"net/url"
	"sort"
	"time"
)

// This file wraps the message reads of PLAN.md Phase 3 ("channel read",
// "chat read", "thread read"). The query-parameter rules are the documented
// ones, quoted per wrapper, and are asserted by the fake and the contract
// layer rather than assumed.

// MessageQuery describes a message listing.
//
// The zero value lists the newest page of a container. Since and Until are
// inclusive windows; the client always re-checks timestamps and always sorts
// the result, because Graph's default order is neither created- nor
// modified-sorted (PLAN.md:185).
type MessageQuery struct {
	// Top is the requested $top per page; it is clamped to the documented 50.
	Top int
	// Limit caps the total number of messages returned (0 means every page).
	Limit int
	// Since drops messages created before it.
	Since time.Time
	// Until drops messages created after it.
	Until time.Time
	// Replies asks for $expand=replies on a channel listing, which carries each
	// root's replies so a thread view needs no second round trip.
	Replies bool
}

// windowed reports whether the query narrows the listing.
func (q MessageQuery) windowed() bool { return !q.Since.IsZero() || !q.Until.IsZero() }

// ListChannelMessages returns a channel's root messages
// (GET /teams/{team-id}/channels/{channel-id}/messages,
// refs/graph/api-reference/v1.0/api/channel-list-messages.md:65).
//
// The endpoint supports only $top (default 20, max 50) and $expand; $filter is
// a 400, not a silent ignore (docs/spike/phase1.md:53), so a --since window is
// applied here. Roots are ordered by "the last modified date of the entire
// reply chain", so the scan stops at the first chain older than --since and the
// result is sorted by the same key with the message id as a tie-break.
func (c *Client) ListChannelMessages(ctx context.Context, teamID, channelID string, q MessageQuery) ([]Message, error) {
	path := "/teams/" + segment(teamID) + "/channels/" + segment(channelID) + "/messages"
	query := url.Values{"$top": {intParam(topValue(q.Top, DefaultTopMessages, MaxTopMessages))}}
	if q.Replies || q.windowed() {
		query.Set("$expand", "replies")
	}
	var out []Message
	err := EachPage[Message](ctx, c, path, query, func(page Page[Message]) (bool, error) {
		for _, m := range page.Value {
			if !q.Until.IsZero() && !m.CreatedDateTime.IsZero() && m.CreatedDateTime.After(q.Until) {
				continue
			}
			if !q.Since.IsZero() && m.LastActivity().Before(q.Since) {
				return false, nil
			}
			out = append(out, m)
			if q.Limit > 0 && len(out) >= q.Limit {
				return false, nil
			}
		}
		return true, nil
	}, WithPreferUnknownEnumMembers())
	if err != nil {
		return nil, err
	}
	// The client can only re-derive the chain order when it asked for the replies:
	// without $expand=replies there are no reply timestamps to compute the chain's
	// last activity from, and the page is already ordered by that activity
	// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:65).
	if q.Replies || q.windowed() {
		sortByActivity(out)
	}
	return out, nil
}

// GetChannelMessage returns one root message
// (refs/graph/api-reference/v1.0/api/chatmessage-get.md).
func (c *Client) GetChannelMessage(ctx context.Context, teamID, channelID, messageID string) (Message, error) {
	var msg Message
	err := c.Get(ctx, ChannelMessagePath(teamID, channelID, messageID), &msg, WithPreferUnknownEnumMembers())
	return msg, err
}

// ListChannelReplies returns the replies of a channel message. $top caps at 50
// here (refs/graph/api-reference/v1.0/api/chatmessage-list-replies.md:41).
func (c *Client) ListChannelReplies(ctx context.Context, teamID, channelID, messageID string, q MessageQuery) ([]Message, error) {
	query := url.Values{"$top": {intParam(topValue(q.Top, DefaultTopMessages, MaxTopMessages))}}
	// The window is applied before the limit, exactly as in the channel and chat
	// listings: the server sends replies oldest-first, so taking --limit first
	// would drop the newest ones the window was asked for.
	out, err := ListAll[Message](ctx, c, ChannelMessagePath(teamID, channelID, messageID)+"/replies", query, 0, WithPreferUnknownEnumMembers())
	if err != nil {
		return nil, err
	}
	out = withinWindow(out, q)
	sortByCreated(out)
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// GetChannelReply returns one reply of a channel message
// (refs/graph/api-reference/v1.0/api/chatmessage-get.md).
func (c *Client) GetChannelReply(ctx context.Context, teamID, channelID, messageID, replyID string) (Message, error) {
	var msg Message
	path := ChannelMessagePath(teamID, channelID, messageID) + "/replies/" + segment(replyID)
	err := c.Get(ctx, path, &msg, WithPreferUnknownEnumMembers())
	return msg, err
}

// ListChatMessages returns a chat's messages
// (GET /chats/{chat-id}/messages,
// refs/graph/api-reference/v1.0/api/chat-list-messages.md:47).
//
// This endpoint, unlike the channel one, filters on the server: $orderby on
// lastModifiedDateTime (the default) or createdDateTime descending, and a
// $filter that is silently ignored unless $orderby names the same property. A
// --since window therefore uses lastModifiedDateTime gt, which returns a
// superset when a message was edited, and the result is re-checked on
// createdDateTime (PLAN.md:183).
func (c *Client) ListChatMessages(ctx context.Context, chatID string, q MessageQuery) ([]Message, error) {
	query := url.Values{"$top": {intParam(topValue(q.Top, DefaultTopMessages, MaxTopMessages))}}
	switch {
	case !q.Since.IsZero():
		query.Set("$orderby", "lastModifiedDateTime desc")
		query.Set("$filter", "lastModifiedDateTime gt "+odataTime(q.Since))
	case !q.Until.IsZero():
		query.Set("$orderby", "createdDateTime desc")
		query.Set("$filter", "createdDateTime lt "+odataTime(q.Until))
	}
	var out []Message
	err := EachPage[Message](ctx, c, "/chats/"+segment(chatID)+"/messages", query, func(page Page[Message]) (bool, error) {
		for _, m := range page.Value {
			if !q.Since.IsZero() && m.CreatedDateTime.Before(q.Since) {
				continue
			}
			if !q.Until.IsZero() && m.CreatedDateTime.After(q.Until) {
				continue
			}
			out = append(out, m)
			if q.Limit > 0 && len(out) >= q.Limit {
				return false, nil
			}
		}
		return true, nil
	}, WithPreferUnknownEnumMembers())
	if err != nil {
		return nil, err
	}
	sortByCreated(out)
	return out, nil
}

// GetChatMessage returns one chat message
// (refs/graph/api-reference/v1.0/api/chatmessage-get.md).
func (c *Client) GetChatMessage(ctx context.Context, chatID, messageID string) (Message, error) {
	var msg Message
	err := c.Get(ctx, ChatMessagePath(chatID, messageID), &msg, WithPreferUnknownEnumMembers())
	return msg, err
}

// ListMessageHostedContents returns the inline images of a message, addressed by
// the message path built with ChannelMessagePath or ChatMessagePath
// (refs/graph/api-reference/v1.0/api/chatmessage-list-hostedcontents.md).
func (c *Client) ListMessageHostedContents(ctx context.Context, messagePath string) ([]HostedContent, error) {
	return ListAll[HostedContent](ctx, c, messagePath+"/hostedContents", nil, 0)
}

// GetHostedContentValue returns one inline image's bytes and content type
// (refs/graph/api-reference/v1.0/api/chatmessagehostedcontent-get.md).
func (c *Client) GetHostedContentValue(ctx context.Context, messagePath, contentID string) (Download, error) {
	path := messagePath + "/hostedContents/" + segment(contentID) + "/$value"
	resp, err := c.Do(ctx, Request{Method: "GET", Path: path})
	if err != nil {
		return Download{}, err
	}
	return Download{ContentType: resp.Header.Get("Content-Type"), Bytes: resp.Body}, nil
}

// ChannelMessagePath is the Graph path of one channel message. It is exported
// because the file-download and inline-image reads address a message by path.
func ChannelMessagePath(teamID, channelID, messageID string) string {
	return "/teams/" + segment(teamID) + "/channels/" + segment(channelID) + "/messages/" + segment(messageID)
}

// ChatMessagePath is the Graph path of one chat message. The /chats form is the
// one the api-reference documents for every chat message operation, and it is
// equivalent to /me/chats for a delegated caller.
func ChatMessagePath(chatID, messageID string) string {
	return "/chats/" + segment(chatID) + "/messages/" + segment(messageID)
}

// withinWindow applies the client-side timestamps check to messages that were
// fetched without a server-side filter.
func withinWindow(msgs []Message, q MessageQuery) []Message {
	if !q.windowed() {
		return msgs
	}
	out := msgs[:0]
	for _, m := range msgs {
		if !q.Since.IsZero() && m.CreatedDateTime.Before(q.Since) {
			continue
		}
		if !q.Until.IsZero() && m.CreatedDateTime.After(q.Until) {
			continue
		}
		out = append(out, m)
	}
	return out
}

// sortByActivity orders messages newest-chain-first, which is the documented
// channel order, with the id as a deterministic tie-break.
func sortByActivity(msgs []Message) {
	sort.SliceStable(msgs, func(i, j int) bool {
		a, b := msgs[i].LastActivity(), msgs[j].LastActivity()
		if !a.Equal(b) {
			return a.After(b)
		}
		return msgs[i].ID > msgs[j].ID
	})
}

// sortByCreated orders messages newest-first by creation time.
func sortByCreated(msgs []Message) {
	sort.SliceStable(msgs, func(i, j int) bool {
		a, b := msgs[i].CreatedDateTime, msgs[j].CreatedDateTime
		if !a.Equal(b) {
			return a.After(b)
		}
		return msgs[i].ID > msgs[j].ID
	})
}
