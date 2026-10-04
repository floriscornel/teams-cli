package fakegraph

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// This file is the in-memory store behind the fake. Every collection keeps its
// insertion order in a slice, so list responses are deterministic; handlers
// apply the ordering Graph documents on top of that.

// Defaults for generated identifiers. They are fixed so a test that creates
// objects gets the same ids on every run: the spike saw opaque, numeric message
// ids (docs/spike/phase1.md:83), so generated message ids look like them.
const (
	idBase          = int64(1750000000000)
	generatedDrive  = "drive-%d"
	generatedFolder = "folder-%s"
)

type userRec struct {
	id, displayName, givenName, surname, upn, mail          string
	jobTitle, department, officeLocation, preferredLanguage string
	businessPhones                                          []string
	mobilePhone                                             string
	relevance                                               float64
	order                                                   int
}

type memberRec struct {
	userID              string
	roles               []string
	visibleHistoryStart time.Time
}

type messageRecord struct {
	id          string
	replyToID   string
	authorID    string
	body        itemBody
	subject     string
	summary     string
	importance  string
	messageType string
	locale      string
	created     time.Time
	modified    time.Time
	edited      time.Time
	deleted     time.Time
	attachments []attachmentWire
	mentions    []mentionWire
	reactions   []reactionWire
	hosted      []*hostedRecord
	eventDetail json.RawMessage
	replies     []*messageRecord
	teamID      string
	channelID   string
	chatID      string
	seq         int
	// rev counts mutations (edit, reaction, soft delete) so the etag changes
	// even when the injected clock is frozen.
	rev int
}

type hostedRecord struct {
	id          string
	contentType string
	content     []byte
	created     time.Time
}

type channelRec struct {
	id             string
	teamID         string
	displayName    string
	description    string
	membershipType string
	created        time.Time
	members        []memberRec
	driveID        string
	filesFolderID  string
	messages       []*messageRecord
	byID           map[string]*messageRecord
}

type teamRec struct {
	id           string
	displayName  string
	description  string
	visibility   string
	created      time.Time
	members      []memberRec
	channels     map[string]*channelRec
	channelOrder []string
}

type chatRec struct {
	id       string
	chatType string
	topic    string
	created  time.Time
	updated  time.Time
	deleted  bool
	hidden   bool
	lastRead time.Time
	members  []memberRec
	messages []*messageRecord
	byID     map[string]*messageRecord
}

type driveItemRec struct {
	id          string
	name        string
	parentID    string
	folder      bool
	content     []byte
	contentType string
	created     time.Time
	modified    time.Time
	webURL      string
	order       int
}

type driveRec struct {
	id    string
	items map[string]*driveItemRec
	order []string
}

// store is the mutable state. Every method that reads or writes it takes the
// lock, because httptest serves requests concurrently.
type store struct {
	mu sync.RWMutex

	me     string
	tenant string
	// membersExpandCap caps the members a chat list inlines with
	// $expand=members; 0 means no cap (see Options.ChatMembersExpandCap).
	membersExpandCap int
	users            map[string]*userRec
	userOrder        []string

	teams     map[string]*teamRec
	teamOrder []string

	chats     map[string]*chatRec
	chatOrder []string

	drives     map[string]*driveRec
	driveOrder []string
	// myDrive is the drive GET /me/drive reports (Model.MyDriveID), which is
	// where a chat file attachment lands.
	myDrive string

	// uploads holds the in-flight upload sessions (handlers_files.go).
	uploads map[string]*uploadSession

	// nextSeq orders messages across every container; it makes the default
	// (deliberately unsorted) chat-message order and the search result order
	// deterministic.
	nextSeq int
	nextID  int64
}

func newStore(model Model, tenant string, membersExpandCap int) *store {
	s := &store{
		me:               model.Me,
		tenant:           tenant,
		membersExpandCap: membersExpandCap,
		users:            map[string]*userRec{},
		teams:            map[string]*teamRec{},
		chats:            map[string]*chatRec{},
		drives:           map[string]*driveRec{},
		myDrive:          model.MyDriveID,
		nextID:           idBase,
	}
	s.seedLocked(model)
	return s
}

// seedLocked merges a model into the store. Callers must hold the write lock
// (or be constructing the store, before it is shared).
func (s *store) seedLocked(m Model) {
	m = m.withDefaults()
	if _, ok := s.users[m.Me]; !ok {
		s.addUserLocked(User{ID: m.Me, DisplayName: m.Me, UserPrincipalName: m.Me + "@contoso.example"})
	}
	for _, u := range m.Users {
		if existing, ok := s.users[u.ID]; ok {
			s.mergeUserLocked(existing, u)
			continue
		}
		s.addUserLocked(u)
	}
	for _, t := range m.Teams {
		s.addTeamLocked(t)
	}
	for _, c := range m.Chats {
		s.addChatLocked(c)
	}
	for _, d := range m.Drives {
		s.addDriveLocked(d)
	}
	if s.myDrive == "" {
		s.myDrive = m.MyDriveID
	}
	// Channel file folders are derived after drives are seeded, so a drive
	// seeded with the same id keeps its items.
	for _, t := range m.Teams {
		for _, c := range t.Channels {
			s.ensureFolderLocked(c.DriveID, c.FilesFolderID, c.DisplayName, c.Created)
		}
	}
}

func (s *store) mergeUserLocked(dst *userRec, u User) {
	dst.displayName = firstNonEmpty(u.DisplayName, dst.displayName)
	dst.givenName = firstNonEmpty(u.GivenName, dst.givenName)
	dst.surname = firstNonEmpty(u.Surname, dst.surname)
	dst.upn = firstNonEmpty(u.UserPrincipalName, dst.upn)
	dst.mail = firstNonEmpty(u.Mail, dst.mail)
	dst.jobTitle = firstNonEmpty(u.JobTitle, dst.jobTitle)
	dst.department = firstNonEmpty(u.Department, dst.department)
	dst.officeLocation = firstNonEmpty(u.OfficeLocation, dst.officeLocation)
	dst.preferredLanguage = firstNonEmpty(u.PreferredLanguage, dst.preferredLanguage)
	if u.Relevance != 0 {
		dst.relevance = u.Relevance
	}
}

func (s *store) addUserLocked(u User) {
	rec := &userRec{
		id:                u.ID,
		displayName:       u.DisplayName,
		givenName:         u.GivenName,
		surname:           u.Surname,
		upn:               u.UserPrincipalName,
		mail:              u.Mail,
		jobTitle:          u.JobTitle,
		department:        u.Department,
		officeLocation:    u.OfficeLocation,
		preferredLanguage: u.PreferredLanguage,
		relevance:         u.Relevance,
		order:             len(s.userOrder),
	}
	if rec.displayName == "" {
		rec.displayName = rec.id
	}
	if rec.upn == "" {
		rec.upn = rec.id + "@contoso.example"
	}
	s.users[rec.id] = rec
	s.userOrder = append(s.userOrder, rec.id)
}

func (s *store) addTeamLocked(t Team) {
	if existing, ok := s.teams[t.ID]; ok {
		// Merging into an existing team only adds the new channels; a test
		// that seeds the same team twice is a test bug, so keep it simple.
		_ = existing
	}
	rec := &teamRec{
		id:          t.ID,
		displayName: t.DisplayName,
		description: t.Description,
		visibility:  t.Visibility,
		created:     t.Created,
		members:     toMemberRecs(t.Members),
		channels:    map[string]*channelRec{},
	}
	if _, ok := s.teams[t.ID]; !ok {
		s.teamOrder = append(s.teamOrder, t.ID)
	}
	s.teams[t.ID] = rec
	for _, c := range t.Channels {
		ch := &channelRec{
			id:             c.ID,
			teamID:         t.ID,
			displayName:    c.DisplayName,
			description:    c.Description,
			membershipType: c.MembershipType,
			created:        c.Created,
			members:        toMemberRecs(c.Members),
			driveID:        c.DriveID,
			filesFolderID:  c.FilesFolderID,
			byID:           map[string]*messageRecord{},
		}
		rec.channels[c.ID] = ch
		rec.channelOrder = append(rec.channelOrder, c.ID)
		for _, msg := range c.Messages {
			root := s.newMessageLocked(msg)
			root.teamID = t.ID
			root.channelID = c.ID
			for _, reply := range msg.Replies {
				r := s.newMessageLocked(reply)
				r.teamID = t.ID
				r.channelID = c.ID
				r.replyToID = root.id
				root.replies = append(root.replies, r)
				ch.byID[r.id] = r
			}
			ch.messages = append(ch.messages, root)
			ch.byID[root.id] = root
		}
	}
}

func (s *store) addChatLocked(c Chat) {
	rec := &chatRec{
		id:       c.ID,
		chatType: c.ChatType,
		topic:    c.Topic,
		created:  c.Created,
		updated:  c.Updated,
		deleted:  c.Deleted,
		hidden:   c.Hidden,
		lastRead: c.LastRead,
		members:  toMemberRecs(c.Members),
		byID:     map[string]*messageRecord{},
	}
	if _, ok := s.chats[c.ID]; !ok {
		s.chatOrder = append(s.chatOrder, c.ID)
	}
	s.chats[c.ID] = rec
	for _, msg := range c.Messages {
		m := s.newMessageLocked(msg)
		m.chatID = c.ID
		rec.messages = append(rec.messages, m)
		rec.byID[m.id] = m
	}
}

func (s *store) addDriveLocked(d Drive) {
	rec := &driveRec{id: d.ID, items: map[string]*driveItemRec{}}
	if _, ok := s.drives[d.ID]; !ok {
		s.driveOrder = append(s.driveOrder, d.ID)
	}
	s.drives[d.ID] = rec
	s.ensureRootLocked(d.ID)
	for _, it := range d.Items {
		rec.items[it.ID] = &driveItemRec{
			id:          it.ID,
			name:        it.Name,
			parentID:    it.ParentID,
			folder:      it.Folder,
			content:     it.Content,
			contentType: it.ContentType,
			created:     it.Created,
			modified:    it.Modified,
			webURL:      it.WebURL,
			order:       len(rec.order),
		}
		rec.order = append(rec.order, it.ID)
	}
}

// ensureRootLocked makes sure a drive has its root folder item.
func (s *store) ensureRootLocked(driveID string) {
	d, ok := s.drives[driveID]
	if !ok {
		d = &driveRec{id: driveID, items: map[string]*driveItemRec{}}
		s.drives[driveID] = d
		s.driveOrder = append(s.driveOrder, driveID)
	}
	if _, ok := d.items["root"]; ok {
		return
	}
	d.items["root"] = &driveItemRec{id: "root", name: "root", folder: true, order: -1}
}

// ensureFolderLocked creates a channel's filesFolder item if it does not exist.
func (s *store) ensureFolderLocked(driveID, folderID, name string, created time.Time) {
	s.ensureRootLocked(driveID)
	d := s.drives[driveID]
	if _, ok := d.items[folderID]; ok {
		return
	}
	d.items[folderID] = &driveItemRec{
		id:       folderID,
		name:     name,
		parentID: "root",
		folder:   true,
		created:  created,
		modified: created,
		order:    len(d.order),
	}
	d.order = append(d.order, folderID)
}

func toMemberRecs(members []Member) []memberRec {
	out := make([]memberRec, 0, len(members))
	for _, m := range members {
		out = append(out, memberRec{userID: m.UserID, roles: append([]string(nil), m.Roles...)})
	}
	return out
}

// newMessageLocked converts a seeded Message into a record, assigning a
// deterministic sequence number.
func (s *store) newMessageLocked(msg Message) *messageRecord {
	rec := &messageRecord{
		id:          msg.ID,
		authorID:    msg.AuthorID,
		body:        itemBody{Content: msg.Body, ContentType: msg.ContentType},
		subject:     msg.Subject,
		summary:     msg.Summary,
		importance:  msg.Importance,
		messageType: msg.MessageType,
		locale:      msg.Locale,
		created:     msg.Created,
		modified:    msg.Modified,
		edited:      msg.Edited,
		deleted:     msg.Deleted,
		attachments: attachmentsFromSeed(msg.Attachments),
		mentions:    mentionsFromSeed(msg.Mentions),
		reactions:   reactionsFromSeed(msg.Reactions),
		seq:         s.nextSeq,
	}
	s.nextSeq++
	for _, hc := range msg.HostedContents {
		created := hc.Created
		if created.IsZero() {
			created = msg.Created
		}
		rec.hosted = append(rec.hosted, &hostedRecord{
			id:          hc.ID,
			contentType: hc.ContentType,
			content:     hc.Content,
			created:     created,
		})
	}
	if msg.EventDetail != "" {
		rec.eventDetail = json.RawMessage(msg.EventDetail)
	}
	return rec
}

func attachmentsFromSeed(in []Attachment) []attachmentWire {
	if len(in) == 0 {
		return nil
	}
	out := make([]attachmentWire, 0, len(in))
	for _, a := range in {
		out = append(out, attachmentWire{
			Content:      a.Content,
			ContentType:  a.ContentType,
			ContentURL:   a.ContentURL,
			ID:           a.ID,
			Name:         a.Name,
			TeamsAppID:   a.TeamsAppID,
			ThumbnailURL: a.ThumbnailURL,
		})
	}
	return out
}

func mentionsFromSeed(in []Mention) []mentionWire {
	if len(in) == 0 {
		return nil
	}
	out := make([]mentionWire, 0, len(in))
	for _, m := range in {
		mw := mentionWire{ID: m.ID, MentionText: m.Text}
		if m.UserID != "" || m.UserDisplayName != "" {
			mw.Mentioned = &mentionedIdentitySet{User: &teamworkUserIdentity{
				ID:               m.UserID,
				DisplayName:      m.UserDisplayName,
				UserIdentityType: firstNonEmpty(m.UserIdentityType, "aadUser"),
			}}
		}
		out = append(out, mw)
	}
	return out
}

func reactionsFromSeed(in []Reaction) []reactionWire {
	if len(in) == 0 {
		return nil
	}
	out := make([]reactionWire, 0, len(in))
	for _, r := range in {
		out = append(out, reactionWire{
			ReactionType:    r.Type,
			CreatedDateTime: graphTime(r.Created),
			User:            &identity{ID: r.UserID},
		})
	}
	return out
}

// newIDLocked returns the next deterministic object id.
func (s *store) newIDLocked() string {
	s.nextID++
	return fmt.Sprint(s.nextID)
}

// newChatIDLocked returns a deterministic chat id in the documented
// "19:<hex>@thread.v2" shape (docs/spike/phase1.md:103).
func (s *store) newChatIDLocked() string {
	n := s.nextSeq
	s.nextSeq++
	return fmt.Sprintf("19:%032x@thread.v2", n+1)
}

// userIDsLocked lists the ids of the users already in the store.
func (s *store) userIDsLocked() []string {
	out := make([]string, 0, len(s.userOrder))
	out = append(out, s.userOrder...)
	return out
}

// lookupUser resolves a user by id or by userPrincipalName, which is what the
// users/{id|user-principal-name} routes accept
// (refs/graph/api-reference/v1.0/api/user-get.md:22).
func (s *store) lookupUser(key string) *userRec {
	if u, ok := s.users[key]; ok {
		return u
	}
	lower := strings.ToLower(key)
	for _, id := range s.userOrder {
		u := s.users[id]
		if strings.ToLower(u.upn) == lower || strings.ToLower(u.mail) == lower {
			return u
		}
	}
	return nil
}

// displayAuthor renders the identitySet of a message author.
func (s *store) displayAuthor(authorID string) *identitySet {
	if authorID == "" {
		return nil
	}
	u := s.users[authorID]
	if u == nil {
		return &identitySet{User: &identity{ID: authorID, DisplayName: authorID}}
	}
	return &identitySet{User: &identity{ID: u.id, DisplayName: u.displayName}}
}

func (s *store) renderUser(u *userRec) userWire {
	return userWire{
		ID:                u.id,
		DisplayName:       u.displayName,
		GivenName:         u.givenName,
		Surname:           u.surname,
		UserPrincipalName: u.upn,
		Mail:              u.mail,
		JobTitle:          u.jobTitle,
		Department:        u.department,
		OfficeLocation:    u.officeLocation,
		PreferredLanguage: u.preferredLanguage,
		MobilePhone:       u.mobilePhone,
		BusinessPhones:    u.businessPhones,
	}
}

func (s *store) renderMembers(members []memberRec) []conversationMemberWire {
	out := make([]conversationMemberWire, 0, len(members))
	for _, m := range members {
		w := conversationMemberWire{
			ODataType: "#microsoft.graph.aadUserConversationMember",
			ID:        m.userID,
			UserID:    m.userID,
			Roles:     append([]string(nil), m.roles...),
		}
		if u := s.users[m.userID]; u != nil {
			w.DisplayName = u.displayName
			w.Email = firstNonEmpty(u.mail, u.upn)
			w.TenantID = s.tenant
		}
		if !m.visibleHistoryStart.IsZero() {
			w.VisibleHistoryStartDateTime = graphTime(m.visibleHistoryStart)
		}
		out = append(out, w)
	}
	return out
}

// renderMessage converts a record to its wire shape. preferUnknownEnums
// reflects the Prefer: include-unknown-enum-members header: without it Graph
// collapses new evolvable-enum members to unknownFutureValue, so
// systemEventMessage only appears with the header
// (refs/graph/api-reference/v1.0/resources/chatmessage.md:81;
// docs/spike/phase1.md:54).
func (s *store) renderMessage(m *messageRecord, preferUnknownEnums, expandHosted bool) messageWire {
	messageType := m.messageType
	if messageType == "systemEventMessage" && !preferUnknownEnums {
		messageType = "unknownFutureValue"
	}
	w := messageWire{
		ID:                   m.id,
		ReplyToID:            m.replyToID,
		Etag:                 s.etag(m),
		MessageType:          messageType,
		CreatedDateTime:      graphTime(m.created),
		LastModifiedDateTime: graphTime(m.modified),
		LastEditedDateTime:   graphTimePtr(m.edited),
		DeletedDateTime:      graphTimePtr(m.deleted),
		Subject:              m.subject,
		Summary:              firstNonEmpty(m.summary, summaryOf(m.body.Content)),
		Importance:           m.importance,
		Locale:               m.locale,
		WebURL:               s.messageWebURL(m),
		From:                 s.displayAuthor(m.authorID),
		Body:                 m.body,
		Attachments:          m.attachments,
		Mentions:             m.mentions,
		Reactions:            m.reactions,
		EventDetail:          m.eventDetail,
	}
	switch {
	case m.chatID != "":
		w.ChatID = m.chatID
	case m.channelID != "":
		w.ChannelIdentity = &channelIdentity{ChannelID: m.channelID, TeamID: m.teamID}
	}
	if expandHosted {
		w.HostedContents = make([]hostedContentWire, 0, len(m.hosted))
		for _, h := range m.hosted {
			w.HostedContents = append(w.HostedContents, hostedContentWire{ID: h.id, ContentType: h.contentType})
		}
	}
	return w
}

// etag renders a version marker the way Graph does: a value that changes
// whenever the message changes (docs/spike/phase1.md:83 shows a numeric etag).
// The revision counter is added because a test's clock is frozen, so an edit
// and the original creation would otherwise share a timestamp.
func (s *store) etag(m *messageRecord) string {
	if m.modified.IsZero() {
		return m.id
	}
	return fmt.Sprint(m.modified.UnixMilli() + int64(m.rev))
}

// messageWebURL builds the deep link Graph's webUrl carries. The shapes are
// the documented ones: the channel form carries tenantId/groupId/parentMessageId
// and the chat form is discriminated by ?context={"contextType":"chat"}
// (refs/INDEX.md, "Channels and messages" and the deep-link notes).
func (s *store) messageWebURL(m *messageRecord) string {
	base := "https://teams.microsoft.com/l/message/"
	if m.chatID != "" {
		return base + m.chatID + "/" + m.id + `?context={"contextType":"chat"}`
	}
	var teamName, channelName string
	if t := s.teams[m.teamID]; t != nil {
		teamName = t.displayName
		if ch := t.channels[m.channelID]; ch != nil {
			channelName = ch.displayName
		}
	}
	url := base + m.channelID + "/" + m.id +
		"?tenantId=" + s.tenant +
		"&groupId=" + m.teamID +
		"&parentMessageId=" + m.replyToID +
		"&teamName=" + teamName +
		"&channelName=" + channelName
	return url
}

// plainText strips the tags of a message body, which is what the search API's
// summary and the CLI's --text rendering need without a full HTML parser.
func plainText(html string) string {
	var b strings.Builder
	b.Grow(len(html))
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(strings.Join(strings.Fields(b.String()), " "))
}

// summaryOf is Graph's fallback summary: a short plain-text preview
// (refs/graph/api-reference/v1.0/resources/chatmessage.md, the summary
// property).
func summaryOf(body string) string {
	text := plainText(body)
	const maxSummary = 120
	if len(text) <= maxSummary {
		return text
	}
	return text[:maxSummary]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// decodeBase64 decodes a hosted content's contentBytes. Graph documents the
// field as base64 (refs/graph/api-reference/v1.0/resources/chatmessagehostedcontent.md).
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
