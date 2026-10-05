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
	// CalendarEvents are the seeded calendar events, keyed by the mailbox owner
	// they belong to. An event with OwnerID "" belongs to [Model.Me], whose
	// calendar is always readable by the signed-in user.
	//
	// The calendar routes reproduce the behaviour the handoff verified live on
	// 2026-10-05 (plans/calendar.md §3): a 1825-day calendarView window, an
	// onlineMeeting that is dropped unless isOnlineMeeting is selected, floating
	// all-day events, a 62-day getSchedule window, a null scheduleItems plus a
	// "5016" error, and the /users/{id}/calendarView access failures.
	CalendarEvents []CalendarEvent
	// CalendarAccess says how the signed-in user may see another user's
	// calendar. The key is the owner's user id and the value is one of the
	// [CalendarAccessNone], [CalendarAccessFreeBusy] or [CalendarAccessRead]
	// constants; a missing entry means "not shared", which is the 403
	// ErrorAccessDenied case.
	CalendarAccess map[string]string
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
	// NoMailbox marks a user that exists but has no Exchange Online mailbox.
	// /users/{id}/calendarView then answers 404 MailboxNotEnabledForRESTAPI and
	// getSchedule answers a "5016" per-schedule error, which is the pair of
	// cases the handoff could not tell apart with only one mailbox missing
	// (plans/calendar.md §3, F4 and F7).
	NoMailbox bool
	// OnlineMeetingProviders is the mailbox's allowedOnlineMeetingProviders,
	// which GET /me/calendar reports. It defaults to
	// [DefaultOnlineMeetingProviders]; set it to a list without
	// "teamsForBusiness" to exercise the silently-dropped Teams meeting
	// (plans/calendar.md §3, F10).
	OnlineMeetingProviders []string
}

// Calendar access levels, as [Model.CalendarAccess] values.
const (
	// CalendarAccessNone is a calendar that is not shared at all: the calendar
	// view answers 403 ErrorAccessDenied and the CLI falls back to free/busy.
	CalendarAccessNone = "none"
	// CalendarAccessFreeBusy is a calendar shared as free/busy only, which the
	// live tenant answered with 404 ErrorItemNotFound rather than 403.
	CalendarAccessFreeBusy = "freeBusy"
	// CalendarAccessRead is a calendar shared in full, so /users/{id}/calendarView
	// returns the events.
	CalendarAccessRead = "read"
)

// Calendar event kinds, as [CalendarEvent.Kind] values.
const (
	// CalendarEventTimed is a normal timed event.
	CalendarEventTimed = "timed"
	// CalendarEventAllDay is a floating all-day event: Graph always returns it
	// as 00:00:00 to 00:00:00 on its own dates, whatever zone was requested
	// (plans/calendar.md §3, F5).
	CalendarEventAllDay = "allDay"
)

// CalendarEvent is one seeded calendar event.
//
// Start and End are instants for a timed event and the event's own dates for an
// all-day one; [Model.withDefaults] converts the latter from [CalendarEvent.Start]
// and [CalendarEvent.End] (or [CalendarEvent.Days]).
type CalendarEvent struct {
	// ID defaults to "event-<n>".
	ID string
	// OwnerID is the mailbox the event lives in; "" means [Model.Me].
	OwnerID string
	// Subject is the event subject.
	Subject string
	// Kind defaults to [CalendarEventTimed].
	Kind string
	// Start and End are the event's instants, in any location.
	Start time.Time
	End   time.Time
	// Days is the length in days of an all-day event. Zero means one day.
	Days int
	// ShowAs defaults to "busy".
	ShowAs string
	// IsCancelled marks a cancelled event, which the CLI hides unless
	// --include-cancelled.
	IsCancelled bool
	// OrganizerName is the organizer's display name; "" means the owner.
	OrganizerName string
	// IsOrganizer marks the event as organized by the mailbox owner, which is
	// what the Phase 6b pre-checks read.
	IsOrganizer bool
	// AllowNewTimeProposals is the Phase 6b pre-check field; nil means the
	// documented default of true.
	AllowNewTimeProposals *bool
	// Location is the location display name.
	Location string
	// Teams marks a Teams online meeting. It sets isOnlineMeeting,
	// onlineMeetingProvider and onlineMeeting.joinUrl.
	Teams bool
	// JoinURL overrides the generated join URL for a Teams meeting.
	JoinURL string
	// Response is the signed-in user's responseStatus.response.
	Response string
	// Type defaults to "singleInstance".
	Type string
	// SeriesMasterID is set when the event is part of a recurring series.
	SeriesMasterID string
	// WebLink overrides the generated webLink.
	WebLink string
	// NoMailbox makes every calendar lookup for this event's owner answer as if
	// the mailbox were missing, without touching the owner's User entry.
	NoMailbox bool

	// Derived by [Model.withDefaults]; not part of the seed.
	allDayStart string
	allDayEnd   string
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
	// defaultShowAs is the free/busy status of a seeded event that does not
	// name one (refs/graph/api-reference/v1.0/resources/event.md, "showAs").
	defaultShowAs = "busy"
	// defaultEventType is the type of a seeded event that does not name one.
	defaultEventType = "singleInstance"
)

// DefaultOnlineMeetingProviders is the allowedOnlineMeetingProviders list a
// mailbox reports unless the seed overrides it. It contains teamsForBusiness,
// so `teams calendar create --teams` succeeds by default; seed a User with a
// list without that entry to exercise the mailbox that silently ignores
// isOnlineMeeting (plans/calendar.md §3, F10).
var DefaultOnlineMeetingProviders = []string{"teamsForBusiness"}

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
	for ci := range m.CalendarEvents {
		ev := &m.CalendarEvents[ci]
		if ev.ID == "" {
			ev.ID = fmt.Sprintf("event-%d", ci+1)
		}
		if ev.OwnerID == "" {
			ev.OwnerID = m.Me
		}
		if ev.Kind == "" {
			ev.Kind = CalendarEventTimed
		}
		if ev.ShowAs == "" {
			ev.ShowAs = defaultShowAs
		}
		if ev.Type == "" {
			ev.Type = defaultEventType
		}
		if ev.Kind == CalendarEventAllDay {
			days := ev.Days
			if days < 1 {
				days = 1
			}
			// An all-day event is seeded by its own dates, so Start is only a
			// carrier for the date and the zone is irrelevant (plans/calendar.md
			// §3, F5).
			from := ev.Start
			if from.IsZero() {
				from = ev.End
			}
			ev.allDayStart = from.Format("2006-01-02")
			ev.allDayEnd = from.AddDate(0, 0, days).Format("2006-01-02")
		}
		if ev.Start.IsZero() && ev.Kind == CalendarEventTimed {
			ev.Start = ev.End
		}
		if ev.End.IsZero() && ev.Kind == CalendarEventTimed {
			ev.End = ev.Start.Add(30 * time.Minute)
		}
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
