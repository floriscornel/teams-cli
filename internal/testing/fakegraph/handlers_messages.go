package fakegraph

import (
	"encoding/json"
	"hash/fnv"
	"net/http"
	"sort"
	"strings"
	"time"
)

// This file serves channel messages, replies and the message actions that both
// containers share (PATCH, softDelete, the reactions and hostedContents).
// Shapes come from refs/graph/api-reference/v1.0/resources/chatmessage.md; the
// endpoint rules from the api-reference pages cited on each handler and, where
// the live service disagrees with them, from docs/spike/phase1.md.

// messageCreateWire is the request body of a message POST
// (refs/graph/api-reference/v1.0/api/channel-post-messages.md:35 and
// chatmessage-post.md, which documents body as the only required property and
// hostedContents[] as the way inline images are created).
type messageCreateWire struct {
	Subject        string                    `json:"subject,omitempty"`
	Body           *itemBody                 `json:"body"`
	Importance     string                    `json:"importance,omitempty"`
	MessageType    string                    `json:"messageType,omitempty"`
	Attachments    []attachmentWire          `json:"attachments,omitempty"`
	Mentions       []mentionWire             `json:"mentions,omitempty"`
	HostedContents []hostedContentCreateWire `json:"hostedContents,omitempty"`
	EventDetail    json.RawMessage           `json:"eventDetail,omitempty"`
}

// hostedContentCreateWire is one hostedContents[] entry. The temporaryId is the
// id the message body must reference as ../hostedContents/{temporaryId}/$value
// (refs/graph/api-reference/v1.0/resources/chatmessagehostedcontent.md;
// docs/spike/phase1.md:90).
type hostedContentCreateWire struct {
	TemporaryID  string `json:"@microsoft.graph.temporaryId"`
	ContentBytes string `json:"contentBytes,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
}

// messagePatchWire is the PATCH body: a chatMessage carrying the properties to
// change. The CLI sends the body, the subject and the importance, which are the
// ones a reader sees (refs/graph/api-reference/v1.0/api/chatmessage-update.md).
type messagePatchWire struct {
	Body       *itemBody `json:"body,omitempty"`
	Subject    *string   `json:"subject,omitempty"`
	Importance *string   `json:"importance,omitempty"`
}

// reactionWireRequest is the setReaction/unsetReaction body: the reactionType
// as unicode (refs/graph/api-reference/v1.0/api/chatmessage-setreaction.md:68).
type reactionWireRequest struct {
	ReactionType string `json:"reactionType"`
}

// handleChannelMessages serves GET
// /teams/{team-id}/channels/{channel-id}/messages.
//
// Only `$top` (default 20, max 50) and `$expand` are supported; the other OData
// parameters "aren't currently supported", and in the live service `$filter`
// answers 400 "Parameter 'Filter' not supported" instead of being ignored
// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:33-38;
// docs/spike/phase1.md:52-53).
//
// Root order is spike-observed: descending by the later of the root's
// lastModifiedDateTime and its newest reply's createdDateTime. For 8 of the 20
// roots the spike sampled, the root's own lastModifiedDateTime was older than
// its newest reply (docs/spike/phase1.md:55). Sorting is deterministic, with
// the message id as the tie-break.
func handleChannelMessages(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	ch, ok := st.channel(c.param("team-id"), c.param("channel-id"))
	if !ok {
		c.fail(notFoundf("The channel %q was not found.", c.param("channel-id")))
		return
	}
	p, perr := parsePage(c.query, defaultTopMessages, maxTopMessages)
	if perr != nil {
		c.fail(perr)
		return
	}
	roots := sortChannelRoots(ch.messages)
	items, next := window(roots, p)

	expandReplies := strings.EqualFold(c.query.Get("$expand"), "replies")
	prefer := c.preferUnknownEnums()
	out := make([]messageWire, 0, len(items))
	for _, root := range items {
		w := st.renderMessage(root, prefer, false)
		if expandReplies {
			inline, link := c.s.inlineReplies(st, root, c.rel+"/"+root.id+"/replies", prefer)
			w.Replies = inline
			w.RepliesNextLink = link
		}
		out = append(out, w)
	}
	c.json(http.StatusOK, pageOf(c.s, c.rel, c.query, out, next, len(roots)))
}

// handleChannelMessage serves GET
// /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}.
func handleChannelMessage(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	msg, err := st.locateMessage(c.params)
	if err != nil {
		c.fail(err)
		return
	}
	c.json(http.StatusOK, st.renderMessage(msg, c.preferUnknownEnums(), false))
}

// handleReplies serves GET .../messages/{chatMessage-id}/replies. Only `$top`
// is supported and its maximum is 50
// (refs/graph/api-reference/v1.0/api/chatmessage-list-replies.md:30).
func handleReplies(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	ch, ok := st.channel(c.param("team-id"), c.param("channel-id"))
	if !ok {
		c.fail(notFoundf("The channel %q was not found.", c.param("channel-id")))
		return
	}
	root := ch.byID[c.param("chatMessage-id")]
	if root == nil || root.replyToID != "" {
		c.fail(notFoundf("The message %q was not found.", c.param("chatMessage-id")))
		return
	}
	p, perr := parsePage(c.query, defaultTopMessages, maxTopMessages)
	if perr != nil {
		c.fail(perr)
		return
	}
	replies := visibleReplies(root)
	items, next := window(replies, p)
	prefer := c.preferUnknownEnums()
	out := make([]messageWire, 0, len(items))
	for _, r := range items {
		out = append(out, st.renderMessage(r, prefer, false))
	}
	c.json(http.StatusOK, pageOf(c.s, c.rel, c.query, out, next, len(replies)))
}

// handleReply serves GET .../messages/{chatMessage-id}/replies/{reply-id}.
func handleReply(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	msg, err := st.locateMessage(c.params)
	if err != nil {
		c.fail(err)
		return
	}
	c.json(http.StatusOK, st.renderMessage(msg, c.preferUnknownEnums(), false))
}

// handlePostChannelMessage serves POST
// /teams/{team-id}/channels/{channel-id}/messages and answers 201 with the
// created message (refs/graph/api-reference/v1.0/api/channel-post-messages.md).
func handlePostChannelMessage(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	ch, ok := st.channel(c.param("team-id"), c.param("channel-id"))
	if !ok {
		c.fail(notFoundf("The channel %q was not found.", c.param("channel-id")))
		return
	}
	in, err := c.messageCreate()
	if err != nil {
		c.fail(err)
		return
	}
	msg := st.newMessageFromWire(in, c.s.now())
	msg.teamID = c.param("team-id")
	msg.channelID = c.param("channel-id")
	ch.messages = append(ch.messages, msg)
	ch.byID[msg.id] = msg
	c.json(http.StatusCreated, st.renderMessage(msg, c.preferUnknownEnums(), false))
}

// handlePostReply serves POST .../messages/{chatMessage-id}/replies.
func handlePostReply(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	ch, ok := st.channel(c.param("team-id"), c.param("channel-id"))
	if !ok {
		c.fail(notFoundf("The channel %q was not found.", c.param("channel-id")))
		return
	}
	root := ch.byID[c.param("chatMessage-id")]
	if root == nil || root.replyToID != "" {
		c.fail(notFoundf("The message %q was not found.", c.param("chatMessage-id")))
		return
	}
	in, err := c.messageCreate()
	if err != nil {
		c.fail(err)
		return
	}
	msg := st.newMessageFromWire(in, c.s.now())
	msg.teamID = c.param("team-id")
	msg.channelID = c.param("channel-id")
	msg.replyToID = root.id
	root.replies = append(root.replies, msg)
	ch.byID[msg.id] = msg
	c.json(http.StatusCreated, st.renderMessage(msg, c.preferUnknownEnums(), false))
}

// handlePatchMessage serves PATCH on a channel message, a reply or a chat
// message. Delegated PATCH answers 204 No Content
// (refs/graph/api-reference/v1.0/api/chatmessage-update.md:44-45; the spike saw
// 204 on the chat path, docs/spike/phase1.md:96).
func handlePatchMessage(c *handlerCtx) {
	var in messagePatchWire
	if !c.decodeBody(&in) {
		return
	}
	if in.Body == nil && in.Subject == nil && in.Importance == nil {
		c.fail(badRequestf("The request body must contain a 'body', 'subject' or 'importance' property."))
		return
	}
	if in.Importance != nil && *in.Importance != "normal" && *in.Importance != "high" && *in.Importance != "urgent" {
		c.fail(badRequestf("'importance' must be normal, high or urgent, got %q.", *in.Importance))
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	msg, err := st.locateMessage(c.params)
	if err != nil {
		c.fail(err)
		return
	}
	if in.Body != nil {
		msg.body = *in.Body
	}
	if in.Subject != nil {
		msg.subject = *in.Subject
	}
	if in.Importance != nil {
		msg.importance = *in.Importance
	}
	now := c.s.now()
	msg.edited = now
	msg.modified = now
	msg.rev++
	c.noContent()
}

// handleSoftDeleteMessage serves POST .../softDelete. The documented success is
// 204 (refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md:51), and the
// live service requires ChannelMessage.ReadWrite rather than
// ChannelMessage.Edit (docs/spike/phase1.md:27,93). Soft-deleted messages stay
// readable by id with a deletedDateTime but drop out of lists and search.
func handleSoftDeleteMessage(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	msg, err := st.locateMessage(c.params)
	if err != nil {
		c.fail(err)
		return
	}
	now := c.s.now()
	msg.deleted = now
	msg.modified = now
	msg.rev++
	c.noContent()
}

// handleSetReaction serves POST .../setReaction. The live service answers 204
// (docs/spike/phase1.md:92,96).
func handleSetReaction(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	msg, err := st.locateMessage(c.params)
	if err != nil {
		c.fail(err)
		return
	}
	var in reactionWireRequest
	if !c.decodeBody(&in) {
		return
	}
	if strings.TrimSpace(in.ReactionType) == "" {
		c.fail(badRequestf("The request body must contain a non-empty 'reactionType'."))
		return
	}
	for _, r := range msg.reactions {
		if r.ReactionType == in.ReactionType && reactionUserID(r) == st.me {
			c.noContent()
			return
		}
	}
	msg.reactions = append(msg.reactions, reactionWire{
		ReactionType:    in.ReactionType,
		CreatedDateTime: graphTime(c.s.now()),
		User:            &identity{ID: st.me},
	})
	msg.modified = c.s.now()
	msg.rev++
	c.noContent()
}

// handleUnsetReaction serves POST .../unsetReaction.
func handleUnsetReaction(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	msg, err := st.locateMessage(c.params)
	if err != nil {
		c.fail(err)
		return
	}
	var in reactionWireRequest
	if !c.decodeBody(&in) {
		return
	}
	if strings.TrimSpace(in.ReactionType) == "" {
		c.fail(badRequestf("The request body must contain a non-empty 'reactionType'."))
		return
	}
	kept := msg.reactions[:0]
	for _, r := range msg.reactions {
		if r.ReactionType == in.ReactionType && reactionUserID(r) == st.me {
			continue
		}
		kept = append(kept, r)
	}
	msg.reactions = kept
	msg.modified = c.s.now()
	msg.rev++
	c.noContent()
}

// handleListHostedContents serves the hostedContents collection. The response
// has a bare "value" key rather than the usual OData envelope, which is what
// chatmessage-list-hostedcontents.md documents.
func handleListHostedContents(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	msg, err := st.locateMessage(c.params)
	if err != nil {
		c.fail(err)
		return
	}
	out := make([]hostedContentWire, 0, len(msg.hosted))
	for _, h := range msg.hosted {
		out = append(out, hostedContentWire{ID: h.id, ContentType: h.contentType})
	}
	c.json(http.StatusOK, map[string]any{"value": out})
}

// handleGetHostedContentValue serves the byte fetch behind
// ../hostedContents/{id}/$value
// (refs/graph/api-reference/v1.0/api/chatmessagehostedcontent-get.md).
func handleGetHostedContentValue(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	msg, err := st.locateMessage(c.params)
	if err != nil {
		c.fail(err)
		return
	}
	id := c.param("hosted-content-id")
	for _, h := range msg.hosted {
		if h.id == id {
			if h.contentType != "" {
				c.w.Header().Set("Content-Type", h.contentType)
			}
			c.w.WriteHeader(http.StatusOK)
			_, _ = c.w.Write(h.content)
			return
		}
	}
	c.fail(notFoundf("The hosted content %q was not found.", id))
}

// messageCreate decodes and validates a message POST body.
func (c *handlerCtx) messageCreate() (messageCreateWire, *apiError) {
	var in messageCreateWire
	if err := decodeJSON(c.body, &in); err != nil {
		return in, badRequestf("The request body is not valid JSON: %v", err)
	}
	if in.Body == nil {
		// Only body is mandatory for a message POST
		// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:47).
		return in, badRequestf("The request body must contain a 'body' property.")
	}
	// A hosted content's temporaryId must equal the id the body references, or
	// the inline image silently has no bytes behind it
	// (refs/graph/api-reference/v1.0/resources/chatmessagehostedcontent.md;
	// docs/spike/phase1.md:90).
	for _, h := range in.HostedContents {
		if h.TemporaryID == "" {
			return in, badRequestf("hostedContents[] needs a '@microsoft.graph.temporaryId'.")
		}
		if !strings.Contains(in.Body.Content, h.TemporaryID) {
			return in, badRequestf("hostedContents[%s] is not referenced by the message body; the body must point at ../hostedContents/%s/$value", h.TemporaryID, h.TemporaryID)
		}
	}
	// Each mention's id is the {index} of its <at id="{index}"> tag
	// (refs/graph/api-reference/v1.0/resources/chatmessagemention.md). Check it
	// rather than dropping the mention, because a missing tag is exactly the
	// sanitizer bug the CLI must avoid (PLAN.md "sanitize before inserting <at>").
	for _, m := range in.Mentions {
		if !strings.Contains(in.Body.Content, `<at id="`+itoa(m.ID)+`">`) {
			return in, badRequestf("mentions[%d] has no matching <at id=\"%d\"> tag in the body", m.ID, m.ID)
		}
	}
	return in, nil
}

// newMessageFromWire turns a validated POST body into a stored message. The
// caller holds the write lock and sets the container ids.
func (s *store) newMessageFromWire(in messageCreateWire, now time.Time) *messageRecord {
	msg := &messageRecord{
		id:          s.newIDLocked(),
		authorID:    s.me,
		body:        *in.Body,
		subject:     in.Subject,
		importance:  firstNonEmpty(in.Importance, defaultImportance),
		messageType: firstNonEmpty(in.MessageType, defaultMessageType),
		locale:      defaultLocale,
		created:     now,
		modified:    now,
		attachments: in.Attachments,
		mentions:    in.Mentions,
		eventDetail: in.EventDetail,
		seq:         s.nextSeq,
	}
	s.nextSeq++
	for _, h := range in.HostedContents {
		content, err := decodeBase64(h.ContentBytes)
		if err != nil {
			// Graph would reject this, but the body was already accepted; keep
			// the id so the pairing is still visible and the bytes empty.
			content = nil
		}
		msg.hosted = append(msg.hosted, &hostedRecord{
			id:          h.TemporaryID,
			contentType: h.ContentType,
			content:     content,
			created:     now,
		})
	}
	return msg
}

// locateMessage resolves the message a path points at. Chat routes carry
// chat-id; channel routes carry team-id, channel-id and either
// chatMessage-id or a reply-id too.
func (s *store) locateMessage(params map[string]string) (*messageRecord, *apiError) {
	if chatID := params["chat-id"]; chatID != "" {
		chat := s.chats[chatID]
		if chat == nil {
			return nil, notFoundf("The chat %q was not found.", chatID)
		}
		msg := chat.byID[params["chatMessage-id"]]
		if msg == nil {
			return nil, notFoundf("The message %q was not found.", params["chatMessage-id"])
		}
		return msg, nil
	}
	ch, ok := s.channel(params["team-id"], params["channel-id"])
	if !ok {
		return nil, notFoundf("The channel %q was not found.", params["channel-id"])
	}
	if replyID := params["reply-id"]; replyID != "" {
		root := ch.byID[params["chatMessage-id"]]
		if root == nil || root.replyToID != "" {
			return nil, notFoundf("The message %q was not found.", params["chatMessage-id"])
		}
		for _, r := range root.replies {
			if r.id == replyID {
				return r, nil
			}
		}
		return nil, notFoundf("The reply %q was not found.", replyID)
	}
	msg := ch.byID[params["chatMessage-id"]]
	if msg == nil {
		return nil, notFoundf("The message %q was not found.", params["chatMessage-id"])
	}
	return msg, nil
}

// sortChannelRoots orders roots the way the live service does: descending by
// the later of the root's lastModifiedDateTime and its newest reply's
// createdDateTime (docs/spike/phase1.md:55). Deleted roots drop out.
func sortChannelRoots(roots []*messageRecord) []*messageRecord {
	out := make([]*messageRecord, 0, len(roots))
	for _, r := range roots {
		if !r.deleted.IsZero() {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, aj := chainActivity(out[i]), chainActivity(out[j])
		if !ai.Equal(aj) {
			return ai.After(aj)
		}
		return out[i].id > out[j].id
	})
	return out
}

// chainActivity is the timestamp a channel root is ordered by.
func chainActivity(root *messageRecord) time.Time {
	best := root.modified
	for _, r := range root.replies {
		if r.created.After(best) {
			best = r.created
		}
	}
	return best
}

// visibleReplies returns the non-deleted replies in created order.
func visibleReplies(root *messageRecord) []*messageRecord {
	out := make([]*messageRecord, 0, len(root.replies))
	for _, r := range root.replies {
		if r.deleted.IsZero() {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].created.Equal(out[j].created) {
			return out[i].created.Before(out[j].created)
		}
		return out[i].id < out[j].id
	})
	return out
}

// inlineReplies renders the first [RepliesExpandedMax] replies of a root and
// links to the rest, which is what $expand=replies does after 200 replies
// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:37-38).
func (srv *Server) inlineReplies(st *store, root *messageRecord, repliesPath string, prefer bool) ([]messageWire, string) {
	replies := visibleReplies(root)
	inline := replies
	rest := 0
	if len(replies) > RepliesExpandedMax {
		inline = replies[:RepliesExpandedMax]
		rest = RepliesExpandedMax
	}
	out := make([]messageWire, 0, len(inline))
	for _, r := range inline {
		out = append(out, st.renderMessage(r, prefer, false))
	}
	if rest == 0 {
		return out, ""
	}
	return out, srv.nextLink(repliesPath, nil, rest)
}

// hashOrder is the deliberate default ordering of a chat message list: Graph
// returns chat messages in neither created- nor modified-order (the spike saw
// 150 unsorted messages, docs/spike/phase1.md:60), so the fake scrambles them
// deterministically instead of pretending an order the client must not rely on.
func hashOrder(msgs []*messageRecord) []*messageRecord {
	out := make([]*messageRecord, 0, len(msgs))
	for _, m := range msgs {
		if !m.deleted.IsZero() {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		hi, hj := fnvHash(out[i].id), fnvHash(out[j].id)
		if hi != hj {
			return hi < hj
		}
		return out[i].id < out[j].id
	})
	return out
}

func fnvHash(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// reactionUserID returns the reacting user's id.
func reactionUserID(r reactionWire) string {
	if r.User == nil {
		return ""
	}
	return r.User.ID
}

// itoa is a tiny int formatter that avoids importing strconv in two files.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
