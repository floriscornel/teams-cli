package fakegraph

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"time"
)

// This file serves the chat endpoints. Shapes come from
// refs/graph/api-reference/v1.0/resources/chat.md; the query rules from
// chat-list.md, chat-list-messages.md and the other api-reference pages cited
// per handler.

// chatMemberWire is one entry of a chat's members[] on create. The create body
// documents roles as mandatory: "Each member must be assigned a role of owner
// or guest" (refs/graph/api-reference/v1.0/api/chat-post.md:47, PLAN.md "Chat
// quirks" records that teams-mcp needs roles:["owner"]).
type chatMemberWire struct {
	ODataType     string   `json:"@odata.type,omitempty"`
	ID            string   `json:"id,omitempty"`
	UserID        string   `json:"userId,omitempty"`
	DisplayName   string   `json:"displayName,omitempty"`
	Email         string   `json:"email,omitempty"`
	Roles         []string `json:"roles,omitempty"`
	UserOdataBind string   `json:"user@odata.bind,omitempty"`
}

// chatCreateWire is the POST /chats body.
type chatCreateWire struct {
	ChatType string           `json:"chatType"`
	Topic    string           `json:"topic,omitempty"`
	Members  []chatMemberWire `json:"members"`
}

// replyWithQuoteWire is the POST .../messages/replyWithQuote body: messageIds
// plus the reply message. "When replying with a quote to multiple messages, a
// maximum of 10 messages can be used for the reply"
// (refs/graph/api-reference/v1.0/api/chatmessage-replywithquote.md:18).
type replyWithQuoteWire struct {
	MessageIDs   []string           `json:"messageIds"`
	ReplyMessage *messageCreateWire `json:"replyMessage"`
}

// markChatUserWire is the markChatReadForUser/markChatUnreadForUser body:
// {user: {id: "..."}}
// (refs/graph/api-reference/v1.0/api/chat-markchatreadforuser.md:45).
type markChatUserWire struct {
	User *struct {
		ID string `json:"id"`
	} `json:"user"`
}

// handleListChats serves GET /me/chats (and the /chats and
// /users/{id}/chats forms). `$top` caps at 50 and pages with
// @odata.nextLink; `$expand` supports members and lastMessagePreview; `$orderby`
// supports lastMessagePreview/createdDateTime descending only
// (refs/graph/api-reference/v1.0/api/chat-list.md:28-36).
func handleListChats(c *handlerCtx) {
	expand, err := parseChatExpand(c.query.Get("$expand"))
	if err != nil {
		c.fail(err)
		return
	}
	filter, ferr := parseChatFilter(c.query.Get("$filter"))
	if ferr != nil {
		c.fail(ferr)
		return
	}
	orderByLastMessage, oerr := parseChatOrderBy(c.query.Get("$orderby"))
	if oerr != nil {
		c.fail(oerr)
		return
	}
	p, perr := parsePage(c.query, maxTopMessages, maxTopMessages)
	if perr != nil {
		c.fail(perr)
		return
	}

	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()

	userID := st.me
	if memberID := c.param("user-id"); memberID != "" {
		u := st.lookupUser(memberID)
		if u == nil {
			c.fail(notFoundf("The user %q was not found.", memberID))
			return
		}
		userID = u.id
	}
	all := make([]*chatRec, 0, len(st.chatOrder))
	for _, id := range st.chatOrder {
		chat := st.chats[id]
		if chat.deleted {
			continue
		}
		if chat.hidden && chat.hiddenForAll() {
			continue
		}
		if !hasMember(chat.members, userID) {
			continue
		}
		if !filter.matches(chat) {
			continue
		}
		all = append(all, chat)
	}
	if orderByLastMessage {
		sort.SliceStable(all, func(i, j int) bool {
			return lastMessageTime(all[i]).After(lastMessageTime(all[j]))
		})
	}
	items, next := window(all, p)
	out := make([]chatWire, 0, len(items))
	for _, chat := range items {
		out = append(out, st.renderChat(chat, expand))
	}
	c.json(http.StatusOK, pageOf(c.s, c.rel, c.query, out, next, len(all)))
}

// handleGetChat serves GET .../{chat-id}.
func handleGetChat(c *handlerCtx) {
	expand, err := parseChatExpand(c.query.Get("$expand"))
	if err != nil {
		c.fail(err)
		return
	}
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	c.json(http.StatusOK, st.renderChat(chat, expand))
}

// handleChatMembers serves GET .../{chat-id}/members. The api-reference says
// the operation takes no OData query parameters and the spike saw all 37
// members come back in one response (docs/spike/phase1.md:68), so the fake
// returns the whole roster with no paging.
func handleChatMembers(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	c.json(http.StatusOK, map[string]any{"value": st.renderMembers(chat.members)})
}

// handleCreateChat serves POST /chats. A one-on-one chat between two members
// already exists at most once, and the documented behaviour is to return the
// existing chat rather than creating a second one
// (refs/graph/api-reference/v1.0/api/chat-post.md:55 and the note on one-on-one
// chats).
func handleCreateChat(c *handlerCtx) {
	var in chatCreateWire
	if !c.decodeBody(&in) {
		return
	}
	chatType := in.ChatType
	if chatType != ChatTypeOneOnOne && chatType != ChatTypeGroup {
		c.fail(badRequestf("'chatType' must be 'oneOnOne' or 'group', got %q.", in.ChatType))
		return
	}
	if len(in.Members) == 0 {
		c.fail(badRequestf("'members' must list every participant, including the caller."))
		return
	}
	if chatType == ChatTypeOneOnOne && len(in.Members) != 2 {
		c.fail(badRequestf("A one-on-one chat needs exactly two members, got %d.", len(in.Members)))
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()

	members := make([]memberRec, 0, len(in.Members))
	for _, m := range in.Members {
		userID := m.UserID
		if userID == "" {
			userID = userIDFromOdataBind(m.UserOdataBind)
		}
		if userID == "" {
			userID = m.ID
		}
		if userID == "" {
			c.fail(badRequestf("A member must carry a userId or a user@odata.bind."))
			return
		}
		if st.users[userID] == nil {
			c.fail(notFoundf("The user %q was not found.", userID))
			return
		}
		roles := m.Roles
		// The docs require an explicit owner or guest role on every member
		// (refs/graph/api-reference/v1.0/api/chat-post.md:47).
		if len(roles) == 0 {
			c.fail(badRequestf("Every member needs a role of 'owner' or 'guest'."))
			return
		}
		valid := false
		for _, role := range roles {
			if role == "owner" || role == "guest" {
				valid = true
			}
		}
		if !valid {
			c.fail(badRequestf("Member roles must contain 'owner' or 'guest', got %v.", roles))
			return
		}
		members = append(members, memberRec{userID: userID, roles: append([]string(nil), roles...)})
	}

	if chatType == ChatTypeOneOnOne {
		if existing := st.findOneOnOne(members); existing != nil {
			c.json(http.StatusCreated, st.renderChat(existing, chatExpandMembers))
			return
		}
	}
	now := c.s.now()
	chat := &chatRec{
		id:       st.newChatIDFor(chatType, members),
		chatType: chatType,
		topic:    in.Topic,
		created:  now,
		updated:  now,
		members:  members,
		byID:     map[string]*messageRecord{},
	}
	st.chats[chat.id] = chat
	st.chatOrder = append(st.chatOrder, chat.id)
	c.json(http.StatusCreated, st.renderChat(chat, chatExpandMembers))
}

// handleAddChatMember serves POST .../{chat-id}/members
// (refs/graph/api-reference/v1.0/api/chat-post-members.md).
func handleAddChatMember(c *handlerCtx) {
	var in chatMemberWire
	if !c.decodeBody(&in) {
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	userID := firstNonEmpty(in.UserID, userIDFromOdataBind(in.UserOdataBind), in.ID)
	if userID == "" {
		c.fail(badRequestf("The member needs a userId or a user@odata.bind."))
		return
	}
	u := st.users[userID]
	if u == nil {
		c.fail(notFoundf("The user %q was not found.", userID))
		return
	}
	if hasMember(chat.members, userID) {
		c.fail(conflictf("The user %q is already a member of the chat.", userID))
		return
	}
	roles := in.Roles
	if len(roles) == 0 {
		roles = []string{"owner"}
	}
	chat.members = append(chat.members, memberRec{userID: userID, roles: roles})
	chat.updated = c.s.now()
	members := st.renderMembers([]memberRec{{userID: userID, roles: roles}})
	c.json(http.StatusCreated, members[0])
}

// handleDeleteChat serves DELETE /chats/{chat-id}. Deleting a chat needs
// Chat.ManageDeletion.All, which is admin-consented and requested
// incrementally by `teams chat delete` (refs/INDEX.md "Chats";
// internal/config/scopes.go, IncrementalScopes).
func handleDeleteChat(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	chat.deleted = true
	c.noContent()
}

// handleSoftDeleteChat serves POST /users/{user-id}/chats/{chat-id}/softDelete.
// The documented chat soft delete path goes through /users/{id}/chats; the
// /chats form answers 405 in the live service
// (refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md;
// docs/spike/phase1.md:99).
func handleSoftDeleteChat(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	chat.deleted = true
	c.noContent()
}

// handleMarkChatRead serves POST .../markChatReadForUser. It moves
// viewpoint.lastMessageReadDateTime for that chat, which is what `teams
// unread` reads back (refs/INDEX.md "Chats", chatviewpoint.md).
func handleMarkChatRead(c *handlerCtx) { c.markChatReadState(true) }

// handleMarkChatUnread serves POST .../markChatUnreadForUser.
func handleMarkChatUnread(c *handlerCtx) { c.markChatReadState(false) }

func (c *handlerCtx) markChatReadState(read bool) {
	var in markChatUserWire
	if len(c.body) > 0 && !c.decodeBody(&in) {
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	if in.User != nil && in.User.ID != "" && in.User.ID != st.me && st.users[in.User.ID] == nil {
		c.fail(notFoundf("The user %q was not found.", in.User.ID))
		return
	}
	if read {
		chat.lastRead = c.s.now()
	} else {
		// Unread means the read watermark is cleared; Graph documents
		// lastMessageReadDateTime as nullable (chatviewpoint.md).
		chat.lastRead = time.Time{}
	}
	c.noContent()
}

// handleChatMessages serves GET .../{chat-id}/messages: the chat message list
// with the $orderby/$filter matrix described in chatquery.go.
func handleChatMessages(c *handlerCtx) {
	query, qerr := parseChatMessageQuery(c.query)
	if qerr != nil {
		c.fail(qerr)
		return
	}
	p, perr := parsePage(c.query, defaultTopMessages, maxTopMessages)
	if perr != nil {
		c.fail(perr)
		return
	}
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	ordered := query.apply(chat.messages)
	items, next := window(ordered, p)
	prefer := c.preferUnknownEnums()
	expandHosted := strings.Contains(strings.ToLower(c.query.Get("$expand")), "hostedcontents")
	out := make([]messageWire, 0, len(items))
	for _, m := range items {
		out = append(out, st.renderMessage(m, prefer, expandHosted))
	}
	c.json(http.StatusOK, pageOf(c.s, c.rel, c.query, out, next, len(ordered)))
}

// handleChatMessage serves GET .../{chat-id}/messages/{chatMessage-id}.
func handleChatMessage(c *handlerCtx) {
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

// handlePostChatMessage serves POST .../{chat-id}/messages and answers 201
// (refs/graph/api-reference/v1.0/api/chat-post-messages.md). The subject is
// stored on chats too, which the spike confirmed (docs/spike/phase1.md:94).
func handlePostChatMessage(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	in, err := c.messageCreate()
	if err != nil {
		c.fail(err)
		return
	}
	msg := st.newMessageFromWire(in, c.s.now())
	msg.chatID = chat.id
	chat.messages = append(chat.messages, msg)
	chat.byID[msg.id] = msg
	chat.updated = msg.created
	c.json(http.StatusCreated, st.renderMessage(msg, c.preferUnknownEnums(), false))
}

// handleReplyWithQuote serves POST .../{chat-id}/messages/replyWithQuote.
// Chats have no threads, so a chat reply is a quote reply; at most 10 quoted
// messages are allowed
// (refs/graph/api-reference/v1.0/api/chatmessage-replywithquote.md:18;
// PLAN.md "Notes that the docs force on this surface").
func handleReplyWithQuote(c *handlerCtx) {
	var in replyWithQuoteWire
	if !c.decodeBody(&in) {
		return
	}
	if len(in.MessageIDs) == 0 {
		c.fail(badRequestf("'messageIds' must contain at least one message id."))
		return
	}
	if len(in.MessageIDs) > 10 {
		c.fail(badRequestf("A quote reply can reference at most 10 messages, got %d.", len(in.MessageIDs)))
		return
	}
	if in.ReplyMessage == nil || in.ReplyMessage.Body == nil {
		c.fail(badRequestf("'replyMessage' with a 'body' is required."))
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	chat := st.chats[c.param("chat-id")]
	if chat == nil {
		c.fail(notFoundf("The chat %q was not found.", c.param("chat-id")))
		return
	}
	for _, id := range in.MessageIDs {
		if chat.byID[id] == nil {
			c.fail(notFoundf("The quoted message %q was not found in this chat.", id))
			return
		}
	}
	in.ReplyMessage.Mentions = validateQuoteMentions(in.ReplyMessage)
	msg := st.newMessageFromWire(*in.ReplyMessage, c.s.now())
	msg.chatID = chat.id
	if len(in.MessageIDs) == 1 {
		msg.replyToID = in.MessageIDs[0]
	}
	chat.messages = append(chat.messages, msg)
	chat.byID[msg.id] = msg
	chat.updated = msg.created
	c.json(http.StatusCreated, st.renderMessage(msg, c.preferUnknownEnums(), false))
}

// validateQuoteMentions keeps a quote reply's mentions that do have an <at>
// tag; a quote body is assembled by Graph from the quoted messages, so the tag
// check a plain POST gets does not apply to it.
func validateQuoteMentions(in *messageCreateWire) []mentionWire {
	if in.Body == nil {
		return in.Mentions
	}
	kept := in.Mentions[:0]
	for _, m := range in.Mentions {
		if strings.Contains(in.Body.Content, `<at id="`+itoa(m.ID)+`">`) {
			kept = append(kept, m)
		}
	}
	return kept
}

// chatExpandMembers is the expansion set a chat create/add-member response
// carries, so a caller learns the roster in the same round trip.
var chatExpandMembers = map[string]bool{"members": true}

// parseChatExpand parses `$expand` for chats. Only members and
// lastMessagePreview are documented
// (refs/graph/api-reference/v1.0/api/chat-list.md:30).
func parseChatExpand(raw string) (map[string]bool, *apiError) {
	out := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, part := range strings.Split(raw, ",") {
		name := strings.TrimSpace(part)
		switch strings.ToLower(name) {
		case "members":
			out["members"] = true
		case "lastmessagepreview":
			out["lastMessagePreview"] = true
		default:
			return nil, badRequestf("The '$expand' value '%s' is not supported. Use 'members' or 'lastMessagePreview'.", name)
		}
	}
	return out, nil
}

// chatListFilter is the subset of `$filter` the fake evaluates on /me/chats.
type chatListFilter struct {
	prop  string
	value string
}

// parseChatFilter parses `topic eq '…'` and `chatType eq '…'`. The api-reference
// lists `$filter` as supported for chats without enumerating it
// (refs/graph/api-reference/v1.0/api/chat-list.md:32); anything the fake does
// not evaluate is rejected rather than silently ignored.
func parseChatFilter(raw string) (chatListFilter, *apiError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return chatListFilter{}, nil
	}
	fields := splitODataFields(raw)
	if len(fields) != 3 || strings.ToLower(fields[1]) != "eq" {
		return chatListFilter{}, badRequestf("The '$filter' value '%s' is not supported. Use '<property> eq <value>'.", raw)
	}
	prop := fields[0]
	if prop != "topic" && prop != "chatType" {
		return chatListFilter{}, badRequestf("The '$filter' value '%s' is not supported. Use 'topic' or 'chatType'.", raw)
	}
	return chatListFilter{prop: prop, value: strings.Trim(fields[2], "'\"")}, nil
}

func (f chatListFilter) matches(c *chatRec) bool {
	switch f.prop {
	case "":
		return true
	case "topic":
		return c.topic == f.value
	case "chatType":
		return c.chatType == f.value
	default:
		return true
	}
}

// parseChatOrderBy accepts the one documented ordering,
// lastMessagePreview/createdDateTime desc
// (refs/graph/api-reference/v1.0/api/chat-list.md:33).
func parseChatOrderBy(raw string) (bool, *apiError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, nil
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "lastMessagePreview/createdDateTime") {
		return false, badRequestf("The '$orderby' value '%s' is not supported. Use 'lastMessagePreview/createdDateTime desc'.", raw)
	}
	if len(fields) == 2 && !strings.EqualFold(fields[1], "desc") {
		return false, badRequestf("The '$orderby' value '%s' is not supported. Only descending order is supported.", raw)
	}
	return true, nil
}

// renderChat converts a chat record to its wire shape. viewpoint is always
// populated because the fake is a delegated context, which is the only context
// Graph populates it in (refs/graph/api-reference/v1.0/resources/chat.md).
func (s *store) renderChat(c *chatRec, expand map[string]bool) chatWire {
	w := chatWire{
		ID:                  c.id,
		ChatType:            c.chatType,
		CreatedDateTime:     graphTime(c.created),
		LastUpdatedDateTime: graphTime(c.updated),
		TenantID:            s.tenant,
		WebURL:              "https://teams.microsoft.com/l/chat/" + c.id + "/conversations",
		Viewpoint: &chatViewpointWire{
			LastMessageReadDateTime: graphTimePtr(c.lastRead),
			IsHidden:                c.hidden,
		},
		Topic: topicPtr(c.topic),
	}
	if expand["members"] {
		members := c.members
		if s.membersExpandCap > 0 && len(members) > s.membersExpandCap {
			// The documented cap is 25 (refs/graph/api-reference/v1.0/api/chat-list.md:31),
			// but the spike saw 116 members come back with no cap
			// (docs/spike/phase1.md:59), so the cap is opt-in.
			members = members[:s.membersExpandCap]
		}
		w.Members = s.renderMembers(members)
	}
	if expand["lastMessagePreview"] {
		if last := lastMessage(c); last != nil {
			preview := s.renderMessage(last, true, false)
			w.LastMessagePreview = &preview
		}
	}
	return w
}

// lastMessage returns the newest non-deleted message of a chat.
func lastMessage(c *chatRec) *messageRecord {
	var best *messageRecord
	for _, m := range c.messages {
		if !m.deleted.IsZero() {
			continue
		}
		if best == nil || m.created.After(best.created) {
			best = m
		}
	}
	return best
}

func lastMessageTime(c *chatRec) time.Time {
	if last := lastMessage(c); last != nil {
		return last.created
	}
	return c.created
}

// hiddenForAll reports whether a hidden chat is hidden for every member.
func (c *chatRec) hiddenForAll() bool { return c.hidden }

// findOneOnOne returns an existing one-on-one chat with exactly these members,
// which is what Graph returns instead of creating a duplicate
// (refs/graph/api-reference/v1.0/api/chat-post.md, the one-on-one note).
func (s *store) findOneOnOne(members []memberRec) *chatRec {
	for _, id := range s.chatOrder {
		chat := s.chats[id]
		if chat.deleted || chat.chatType != ChatTypeOneOnOne || len(chat.members) != len(members) {
			continue
		}
		same := true
		for _, m := range members {
			if !hasMember(chat.members, m.userID) {
				same = false
				break
			}
		}
		if same {
			return chat
		}
	}
	return nil
}

// newChatIDFor builds a chat id. A one-on-one chat's id is derived from its
// member set, so the same pair always maps to the same chat, which is the
// documented "only one one-on-one chat can exist between two members" property.
func (s *store) newChatIDFor(chatType string, members []memberRec) string {
	if chatType != ChatTypeOneOnOne {
		return s.newChatIDLocked()
	}
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.userID)
	}
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "|")))
	return "19:" + hex.EncodeToString(sum[:16]) + "@thread.v2"
}

// userIDFromOdataBind extracts the user id from a
// "https://graph.microsoft.com/v1.0/users('id')" binding.
func userIDFromOdataBind(bind string) string {
	if bind == "" {
		return ""
	}
	i := strings.LastIndex(bind, "('")
	j := strings.LastIndex(bind, "')")
	if i >= 0 && j > i+2 {
		return bind[i+2 : j]
	}
	i = strings.LastIndex(bind, "/users/")
	if i >= 0 {
		return strings.Trim(bind[i+len("/users/"):], "/'\"")
	}
	return ""
}

func topicPtr(topic string) *string {
	if topic == "" {
		return nil
	}
	return &topic
}
