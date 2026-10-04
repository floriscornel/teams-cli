package fakegraph

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// This file is the seed DSL. A Model is a plain, explicit description of the
// state the fake starts with; it is deterministic by construction (no
// time.Now() anywhere) so every test run sees the same data. Timestamps are
// supplied by the caller, usually from a frozen clock
// (internal/clock.NewFake).

// DefaultMe is the id of the signed-in user when a seed does not name one.
const DefaultMe = "me"

// DefaultTenantID is the tenant id stamped on teams, channels and chats.
const DefaultTenantID = "11111111-1111-1111-1111-111111111111"

// SelfChatID is the documented id of the "chat with yourself" chat
// (docs/spike/phase1.md:103; PLAN.md "Smart references").
const SelfChatID = "48:notes"

// Chat types Graph documents (refs/graph/api-reference/v1.0/resources/chat.md,
// "chatType values"). There is no "self" member: the self chat is the
// one-on-one chat whose only member is the signed-in user, with the
// documented id 48:notes.
const (
	ChatTypeOneOnOne = "oneOnOne"
	ChatTypeGroup    = "group"
	ChatTypeMeeting  = "meeting"
)

// Channel membership types Graph documents
// (refs/graph/api-reference/v1.0/resources/channel.md,
// "channelMembershipType values").
const (
	ChannelStandard = "standard"
	ChannelPrivate  = "private"
	ChannelShared   = "shared"
)

// Model is the seed for a fake server.
//
//	srv := fakegraph.New(t, fakegraph.Options{
//	    Model: fakegraph.Model{
//	        Users: []fakegraph.User{{ID: "u1", DisplayName: "Alice"}},
//	        Teams: []fakegraph.Team{{
//	            ID: "t1", DisplayName: "Engineering",
//	            Channels: []fakegraph.Channel{{
//	                ID: "c1", DisplayName: "General",
//	                Messages: []fakegraph.Message{{ID: "m1", AuthorID: "u1", Body: "<p>hi</p>"}},
//	            }},
//	        }},
//	    },
//	})
//
// Additional state can be merged later with [Server.Seed].
type Model struct {
	// Me is the signed-in user id; it defaults to [DefaultMe]. If no user
	// with that id is seeded, a user is created for it.
	Me string
	// Users are the directory users.
	Users []User
	// Teams carry their channels and channel messages.
	Teams []Team
	// Chats carry their members and messages.
	Chats []Chat
	// Drives carry drive items (files and folders).
	Drives []Drive
	// MyDriveID is the drive GET /me/drive reports, which is where a chat file
	// attachment is uploaded (refs/teams-mcp/src/utils/file-upload.ts:256). It
	// defaults to [DefaultMyDriveID]; seed a Drive with that id to give the
	// user's drive pre-existing items.
	MyDriveID string
}

// User is a directory user.
type User struct {
	ID                string
	DisplayName       string
	GivenName         string
	Surname           string
	UserPrincipalName string
	Mail              string
	JobTitle          string
	Department        string
	OfficeLocation    string
	PreferredLanguage string
	// Relevance orders this user in /me/people: the higher the score, the
	// earlier the person appears. The People API is documented as "ordered by
	// relevance … determined by the user's communication and collaboration
	// patterns" (refs/graph/api-reference/v1.0/api/user-list-people.md:12).
	Relevance float64
}

// Member is a conversation member. Roles are the documented strings: "owner",
// "guest" or empty for a plain member
// (refs/graph/api-reference/v1.0/resources/conversationmember.md).
type Member struct {
	UserID string
	Roles  []string
}

// Team is a team with its channels.
type Team struct {
	ID          string
	DisplayName string
	Description string
	// Visibility defaults to "public".
	Visibility string
	Created    time.Time
	// Members defaults to the signed-in user as owner when empty.
	Members  []Member
	Channels []Channel
}

// Channel is a channel (standard, private or shared) with its root messages.
type Channel struct {
	ID          string
	DisplayName string
	Description string
	// MembershipType defaults to [ChannelStandard].
	MembershipType string
	Created        time.Time
	// Members defaults to the team members when empty.
	Members []Member
	// Messages are root messages; each may carry Replies.
	Messages []Message
	// DriveID and FilesFolderID override the filesFolder this channel points
	// at. When empty the fake derives DriveID "drive-<teamID>" and
	// FilesFolderID "folder-<channelID>" and creates that folder, so
	// GET /teams/{id}/channels/{id}/filesFolder works without extra seeding.
	DriveID       string
	FilesFolderID string
}

// Message is a channel message, chat message or reply. ID and Body are the
// only fields a caller normally sets; everything else has a documented default.
type Message struct {
	ID          string
	AuthorID    string
	Body        string
	ContentType string
	Subject     string
	Summary     string
	// Importance defaults to "normal" (normal, high or urgent).
	Importance string
	// MessageType defaults to "message"; set it to "systemEventMessage" to
	// exercise the Prefer: include-unknown-enum-members behaviour, where
	// Graph reports unknownFutureValue without that header
	// (docs/spike/phase1.md:54).
	MessageType string
	Locale      string
	Created     time.Time
	Modified    time.Time
	Edited      time.Time
	Deleted     time.Time
	Attachments []Attachment
	Mentions    []Mention
	Reactions   []Reaction
	// HostedContents are seeded inline images. CreatedAt defaults to Created.
	HostedContents []HostedContent
	// EventDetail is the raw eventDetail object of a systemEventMessage.
	EventDetail string
	// Replies are the replies of a channel root message.
	Replies []Message
}

// Attachment is a chatMessageAttachment
// (refs/graph/api-reference/v1.0/resources/chatmessageattachment.md).
type Attachment struct {
	ID           string
	Name         string
	ContentType  string
	ContentURL   string
	Content      string
	ThumbnailURL string
	TeamsAppID   string
}

// Mention is a chatMessageMention. The <at id="N"> tag in the body must match
// ID (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
type Mention struct {
	ID               int
	Text             string
	UserID           string
	UserDisplayName  string
	UserIdentityType string
}

// Reaction is a chatMessageReaction
// (refs/graph/api-reference/v1.0/resources/chatmessagereaction.md).
type Reaction struct {
	Type    string
	UserID  string
	Created time.Time
}

// HostedContent is an inline image. ID is the id the body reference uses; for
// an inbound POST it is the body's
// @microsoft.graph.temporaryId, which must equal the id in
// ../hostedContents/{id}/$value (refs/INDEX.md, "Inline images have no create
// endpoint in the v1.0 API reference"; docs/spike/phase1.md:90).
type HostedContent struct {
	ID          string
	ContentType string
	Content     []byte
	Created     time.Time
}

// Chat is a one-on-one, group or meeting chat.
type Chat struct {
	ID string
	// ChatType defaults to [ChatTypeGroup]; use [ChatTypeOneOnOne] for a 1:1
	// chat. A chat whose members are exactly the signed-in user is the self
	// chat; seed it with the id [SelfChatID].
	ChatType string
	Topic    string
	Created  time.Time
	Updated  time.Time
	// Members defaults to the signed-in user when empty.
	Members []Member
	// Messages are the chat messages, oldest first in the seed.
	Messages []Message
	// LastRead seeds viewpoint.lastMessageReadDateTime for the signed-in user.
	LastRead time.Time
	// Hidden seeds viewpoint.isHidden.
	Hidden bool
	// Deleted seeds a soft-deleted chat (softDelete), which stays readable but
	// is hidden from /me/chats.
	Deleted bool
}

// Drive is a drive with its items.
type Drive struct {
	ID    string
	Items []DriveItem
}

// DriveItem is a file or folder. ParentID "" means the drive root; the root
// item itself is created automatically with the id "root".
type DriveItem struct {
	ID          string
	Name        string
	ParentID    string
	Folder      bool
	Content     []byte
	ContentType string
	Created     time.Time
	Modified    time.Time
	WebURL      string
}

// Default values applied by [Model.withDefaults].
const (
	defaultImportance  = "normal"
	defaultMessageType = "message"
	defaultContentType = "html"
	defaultLocale      = "en-us"
	defaultVisibility  = "public"
	defaultDrivePrefix = "drive-"
	// DefaultMyDriveID is the id GET /me/drive reports when the seed does not
	// name one.
	DefaultMyDriveID = "drive-me"
)

func (m Model) withDefaults() Model {
	if strings.TrimSpace(m.Me) == "" {
		m.Me = DefaultMe
	}
	for i := range m.Users {
		if m.Users[i].ID == "" {
			m.Users[i].ID = fmt.Sprintf("user-%d", i+1)
		}
		if m.Users[i].DisplayName == "" {
			m.Users[i].DisplayName = m.Users[i].ID
		}
	}
	for ti := range m.Teams {
		t := &m.Teams[ti]
		if t.ID == "" {
			t.ID = fmt.Sprintf("team-%d", ti+1)
		}
		if t.DisplayName == "" {
			t.DisplayName = t.ID
		}
		if t.Visibility == "" {
			t.Visibility = defaultVisibility
		}
		if len(t.Members) == 0 {
			t.Members = []Member{{UserID: m.Me, Roles: []string{"owner"}}}
		}
		for ci := range t.Channels {
			c := &t.Channels[ci]
			if c.ID == "" {
				c.ID = fmt.Sprintf("channel-%d-%d", ti+1, ci+1)
			}
			if c.DisplayName == "" {
				c.DisplayName = c.ID
			}
			if c.MembershipType == "" {
				c.MembershipType = ChannelStandard
			}
			if len(c.Members) == 0 {
				c.Members = append([]Member(nil), t.Members...)
			}
			if c.DriveID == "" {
				c.DriveID = defaultDrivePrefix + t.ID
			}
			if c.FilesFolderID == "" {
				c.FilesFolderID = "folder-" + c.ID
			}
			for mi := range c.Messages {
				c.Messages[mi] = c.Messages[mi].withDefaults()
			}
		}
	}
	for chi := range m.Chats {
		c := &m.Chats[chi]
		if c.ID == "" {
			c.ID = fmt.Sprintf("chat-%d", chi+1)
		}
		if c.ChatType == "" {
			c.ChatType = ChatTypeGroup
		}
		if len(c.Members) == 0 {
			c.Members = []Member{{UserID: m.Me, Roles: []string{"owner"}}}
		}
		for mi := range c.Messages {
			c.Messages[mi] = c.Messages[mi].withDefaults()
		}
	}
	for di := range m.Drives {
		d := &m.Drives[di]
		if d.ID == "" {
			d.ID = fmt.Sprintf("drive-%d", di+1)
		}
		for ii := range d.Items {
			it := &d.Items[ii]
			if it.ID == "" {
				it.ID = fmt.Sprintf("item-%d-%d", di+1, ii+1)
			}
			if it.Modified.IsZero() {
				it.Modified = it.Created
			}
		}
	}
	if m.MyDriveID == "" {
		m.MyDriveID = DefaultMyDriveID
	}
	return m
}

func (msg Message) withDefaults() Message {
	if msg.Importance == "" {
		msg.Importance = defaultImportance
	}
	if msg.MessageType == "" {
		msg.MessageType = defaultMessageType
	}
	if msg.ContentType == "" {
		msg.ContentType = defaultContentType
	}
	if msg.Locale == "" {
		msg.Locale = defaultLocale
	}
	if msg.Modified.IsZero() {
		msg.Modified = msg.Created
	}
	for i := range msg.Replies {
		msg.Replies[i] = msg.Replies[i].withDefaults()
	}
	return msg
}

// validate reports seed problems early, with enough context to fix the seed.
// [New] panics on a non-nil error because a broken seed is a test bug, not a
// condition to handle.
func (m Model) validate() error { return m.validateWith(nil) }

// validateWith checks the seed, additionally accepting the listed user ids as
// known. [Server.Seed] passes the users already in the store, because an
// incremental seed legitimately references them.
func (m Model) validateWith(extraUsers []string) error {
	// Defaults are applied first so the checks see the same derived ids
	// (channel drive and filesFolder names) the store will create.
	m = m.withDefaults()
	users := map[string]bool{}
	for _, id := range extraUsers {
		users[id] = true
	}
	for _, u := range m.Users {
		if u.ID == "" {
			return errors.New("fakegraph: user without an id")
		}
		if users[u.ID] {
			// A user already known from an earlier seed may be re-listed to
			// fill in more properties, so only duplicates inside this model
			// are an error.
			if _, listed := userIDs(m.Users)[u.ID]; listed {
				return fmt.Errorf("fakegraph: duplicate user id %q", u.ID)
			}
		}
		users[u.ID] = true
	}
	teams := map[string]bool{}
	for _, t := range m.Teams {
		if teams[t.ID] {
			return fmt.Errorf("fakegraph: duplicate team id %q", t.ID)
		}
		teams[t.ID] = true
		for _, c := range t.Channels {
			if err := validateMembers("channel "+c.ID, c.Members, users, m.Me); err != nil {
				return err
			}
			if err := validateMessages("channel "+c.ID, c.Messages, users); err != nil {
				return err
			}
		}
	}
	chats := map[string]bool{}
	for _, c := range m.Chats {
		if chats[c.ID] {
			return fmt.Errorf("fakegraph: duplicate chat id %q", c.ID)
		}
		chats[c.ID] = true
		if err := validateMembers("chat "+c.ID, c.Members, users, m.Me); err != nil {
			return err
		}
		if err := validateMessages("chat "+c.ID, c.Messages, users); err != nil {
			return err
		}
	}
	// A drive item may hang under a folder that a channel creates implicitly,
	// so collect those ids per drive before checking parents.
	autoFolders := map[string]map[string]bool{}
	for _, t := range m.Teams {
		for _, c := range t.Channels {
			if autoFolders[c.DriveID] == nil {
				autoFolders[c.DriveID] = map[string]bool{}
			}
			autoFolders[c.DriveID][c.FilesFolderID] = true
		}
	}
	drives := map[string]bool{}
	for _, d := range m.Drives {
		if drives[d.ID] {
			return fmt.Errorf("fakegraph: duplicate drive id %q", d.ID)
		}
		drives[d.ID] = true
		items := map[string]bool{}
		for _, it := range d.Items {
			if items[it.ID] {
				return fmt.Errorf("fakegraph: duplicate drive item id %q in drive %q", it.ID, d.ID)
			}
			items[it.ID] = true
		}
		for _, it := range d.Items {
			if it.ParentID == "" || it.ParentID == "root" {
				continue
			}
			if items[it.ParentID] || autoFolders[d.ID][it.ParentID] {
				continue
			}
			return fmt.Errorf("fakegraph: drive item %q has unknown parent %q in drive %q", it.ID, it.ParentID, d.ID)
		}
	}
	return nil
}

// userIDs indexes a user list by id.
func userIDs(users []User) map[string]bool {
	out := make(map[string]bool, len(users))
	for _, u := range users {
		out[u.ID] = true
	}
	return out
}

// validateMembers checks that every member user exists. m.Me is always
// allowed, because withDefaults creates the signed-in user when the seed does
// not name it.
func validateMembers(where string, members []Member, users map[string]bool, me string) error {
	for _, m := range members {
		if m.UserID == "" {
			return fmt.Errorf("fakegraph: %s has a member without a user id", where)
		}
		if !users[m.UserID] && m.UserID != me {
			return fmt.Errorf("fakegraph: %s references unknown user %q", where, m.UserID)
		}
	}
	return nil
}

func validateMessages(where string, msgs []Message, users map[string]bool) error {
	seen := map[string]bool{}
	var walk func([]Message) error
	walk = func(list []Message) error {
		for _, msg := range list {
			if msg.ID == "" {
				return fmt.Errorf("fakegraph: %s has a message without an id", where)
			}
			if seen[msg.ID] {
				return fmt.Errorf("fakegraph: %s has duplicate message id %q", where, msg.ID)
			}
			seen[msg.ID] = true
			if msg.AuthorID != "" && !users[msg.AuthorID] {
				return fmt.Errorf("fakegraph: %s message %q references unknown author %q", where, msg.ID, msg.AuthorID)
			}
			for _, men := range msg.Mentions {
				if men.UserID != "" && !users[men.UserID] {
					return fmt.Errorf("fakegraph: %s message %q mentions unknown user %q", where, msg.ID, men.UserID)
				}
			}
			if err := walk(msg.Replies); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(msgs)
}
