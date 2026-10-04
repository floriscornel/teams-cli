package graph

import (
	"encoding/json"
	"time"
)

// This file holds the Microsoft Graph resources the read commands use, in the
// shape Graph returns them. Every type cites the api-reference page it comes
// from under refs/graph/api-reference/v1.0/resources/, which is also what the
// Layer 6 contract tests validate the wire traffic against.
//
// The types are the CLI's --json schema, so a field is never renamed for
// convenience and read-only properties Graph returns stay in.

// ItemBody is itemBody: the message body plus how to read it
// (refs/graph/api-reference/v1.0/resources/itembody.md). contentType is "html"
// or "text"; Teams sends HTML whenever the message carries a mention.
type ItemBody struct {
	Content     string `json:"content"`
	ContentType string `json:"contentType,omitempty"`
}

// Identity is microsoft.graph.identity: an id and a display name
// (refs/graph/api-reference/v1.0/resources/identityset.md).
type Identity struct {
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// TeamworkUserIdentity is teamworkUserIdentity, which a mention's
// mentioned.user carries. userIdentityType is "aadUser" for a person
// (refs/graph/api-reference/v1.0/resources/teamworkuseridentity.md).
type TeamworkUserIdentity struct {
	ID               string `json:"id,omitempty"`
	DisplayName      string `json:"displayName,omitempty"`
	TenantID         string `json:"tenantId,omitempty"`
	UserIdentityType string `json:"userIdentityType,omitempty"`
}

// IdentitySet is identitySet, which channel messages carry in "from"
// (refs/graph/api-reference/v1.0/resources/identityset.md).
type IdentitySet struct {
	User         *Identity `json:"user,omitempty"`
	Application  *Identity `json:"application,omitempty"`
	Device       *Identity `json:"device,omitempty"`
	Conversation *Identity `json:"conversation,omitempty"`
}

// Sender returns the human behind a message, preferring the user identity and
// falling back to the application, which is what a bot message carries.
func (s *IdentitySet) Sender() Identity {
	if s == nil {
		return Identity{}
	}
	switch {
	case s.User != nil && (s.User.ID != "" || s.User.DisplayName != ""):
		return *s.User
	case s.Application != nil:
		return *s.Application
	case s.Conversation != nil:
		return *s.Conversation
	case s.Device != nil:
		return *s.Device
	default:
		return Identity{}
	}
}

// MentionedIdentitySet is chatMessageMentionedIdentitySet
// (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
type MentionedIdentitySet struct {
	Application  *Identity             `json:"application,omitempty"`
	Conversation *Identity             `json:"conversation,omitempty"`
	Tag          *Identity             `json:"tag,omitempty"`
	User         *TeamworkUserIdentity `json:"user,omitempty"`
}

// Mention is chatMessageMention. Its id is the index the body's
// <at id="N"> tag refers to, so the two must always be read together
// (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
type Mention struct {
	ID          int                   `json:"id"`
	MentionText string                `json:"mentionText,omitempty"`
	Mentioned   *MentionedIdentitySet `json:"mentioned,omitempty"`
}

// DisplayName is the name to render for a mention: the mentioned user's display
// name when Graph sends one, otherwise the text Teams substituted into the body.
func (m Mention) DisplayName() string {
	if m.Mentioned != nil && m.Mentioned.User != nil && m.Mentioned.User.DisplayName != "" {
		return m.Mentioned.User.DisplayName
	}
	return m.MentionText
}

// UserID is the mentioned user's id, when the mention names a person.
func (m Mention) UserID() string {
	if m.Mentioned != nil && m.Mentioned.User != nil {
		return m.Mentioned.User.ID
	}
	return ""
}

// Reaction is chatMessageReaction
// (refs/graph/api-reference/v1.0/resources/chatmessagereaction.md).
type Reaction struct {
	ReactionType       string    `json:"reactionType,omitempty"`
	CreatedDateTime    time.Time `json:"createdDateTime,omitempty"`
	DisplayName        string    `json:"displayName,omitempty"`
	ReactionContentURL string    `json:"reactionContentUrl,omitempty"`
	User               *Identity `json:"user,omitempty"`
}

// Attachment is chatMessageAttachment
// (refs/graph/api-reference/v1.0/resources/chatmessageattachment.md).
type Attachment struct {
	ID           string `json:"id,omitempty"`
	ContentType  string `json:"contentType,omitempty"`
	ContentURL   string `json:"contentUrl,omitempty"`
	Content      string `json:"content,omitempty"`
	Name         string `json:"name,omitempty"`
	ThumbnailURL string `json:"thumbnailUrl,omitempty"`
	TeamsAppID   string `json:"teamsAppId,omitempty"`
}

// HostedContent is chatMessageHostedContent: an inline image. contentBytes is
// write-only and never appears in a list response
// (refs/graph/api-reference/v1.0/resources/chatmessagehostedcontent.md).
type HostedContent struct {
	ID          string `json:"id"`
	ContentType string `json:"contentType,omitempty"`
}

// ChannelIdentity is channelIdentity, present on a channel message
// (refs/graph/api-reference/v1.0/resources/channelidentity.md).
type ChannelIdentity struct {
	ChannelID string `json:"channelId,omitempty"`
	TeamID    string `json:"teamId,omitempty"`
}

// Message is chatMessage, the resource every read command renders
// (refs/graph/api-reference/v1.0/resources/chatmessage.md). Nullable timestamps
// are pointers, exactly as Graph sends them (null when unset).
type Message struct {
	ID                   string           `json:"id"`
	ReplyToID            string           `json:"replyToId,omitempty"`
	ETag                 string           `json:"etag,omitempty"`
	MessageType          string           `json:"messageType,omitempty"`
	CreatedDateTime      time.Time        `json:"createdDateTime,omitzero"`
	LastModifiedDateTime time.Time        `json:"lastModifiedDateTime,omitzero"`
	LastEditedDateTime   *time.Time       `json:"lastEditedDateTime,omitempty"`
	DeletedDateTime      *time.Time       `json:"deletedDateTime,omitempty"`
	Subject              string           `json:"subject,omitempty"`
	Summary              string           `json:"summary,omitempty"`
	Importance           string           `json:"importance,omitempty"`
	Locale               string           `json:"locale,omitempty"`
	WebURL               string           `json:"webUrl,omitempty"`
	From                 *IdentitySet     `json:"from,omitempty"`
	Body                 ItemBody         `json:"body"`
	Attachments          []Attachment     `json:"attachments,omitempty"`
	Mentions             []Mention        `json:"mentions,omitempty"`
	Reactions            []Reaction       `json:"reactions,omitempty"`
	HostedContents       []HostedContent  `json:"hostedContents,omitempty"`
	ChannelIdentity      *ChannelIdentity `json:"channelIdentity,omitempty"`
	ChatID               string           `json:"chatId,omitempty"`
	EventDetail          json.RawMessage  `json:"eventDetail,omitempty"`
	// Replies arrives only through $expand=replies on the channel message list
	// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:37); it is not
	// a chatMessage property.
	Replies []Message `json:"replies,omitempty"`
	// RepliesNextLink is replies@odata.nextLink, present when the expansion
	// inlined only the first page of a long thread.
	RepliesNextLink string `json:"replies@odata.nextLink,omitempty"`
}

// IsReply reports whether this message is a reply inside a channel thread.
func (m Message) IsReply() bool { return m.ReplyToID != "" }

// IsSystemEvent reports whether Teams generated the message rather than a
// person (a member added, a call ended, a tab created, ...). Reading it requires
// the Prefer: include-unknown-enum-members header
// (refs/graph/api-reference/v1.0/resources/chatmessage.md:81).
func (m Message) IsSystemEvent() bool { return m.MessageType == "systemEventMessage" }

// IsDeleted reports whether the message was soft-deleted. Soft-deleted messages
// stay in a listing with a deletedDateTime, and Teams shows a tombstone.
func (m Message) IsDeleted() bool { return m.DeletedDateTime != nil && !m.DeletedDateTime.IsZero() }

// LastActivity is the timestamp a listing is ordered by: the later of the
// message's own modification and its newest reply. Channel root messages are
// documented as sorted by "the last modified date of the entire reply chain"
// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:65), and the spike
// confirmed that a root's own lastModifiedDateTime misses reply activity
// (docs/spike/phase1.md:55). The order sentence is at
// refs/graph/api-reference/v1.0/api/channel-list-messages.md:65.
func (m Message) LastActivity() time.Time {
	out := m.LastModifiedDateTime
	if out.IsZero() {
		out = m.CreatedDateTime
	}
	for _, reply := range m.Replies {
		if t := reply.LastActivity(); t.After(out) {
			out = t
		}
	}
	return out
}

// ChatViewpoint is chatViewpoint, the per-user read state of a chat. Only
// List chats returns it, and only for delegated callers
// (refs/graph/api-reference/v1.0/resources/chatviewpoint.md).
type ChatViewpoint struct {
	IsHidden                *bool      `json:"isHidden,omitempty"`
	LastMessageReadDateTime *time.Time `json:"lastMessageReadDateTime,omitempty"`
}

// ConversationMember is aadUserConversationMember: the member shape both chat and
// team member listings return
// (refs/graph/api-reference/v1.0/resources/aaduserconversationmember.md).
type ConversationMember struct {
	ODataType                   string     `json:"@odata.type,omitempty"`
	ID                          string     `json:"id"`
	DisplayName                 string     `json:"displayName,omitempty"`
	Email                       string     `json:"email,omitempty"`
	Roles                       []string   `json:"roles,omitempty"`
	TenantID                    string     `json:"tenantId,omitempty"`
	UserID                      string     `json:"userId,omitempty"`
	VisibleHistoryStartDateTime *time.Time `json:"visibleHistoryStartDateTime,omitempty"`
}

// Chat is microsoft.graph.chat (refs/graph/api-reference/v1.0/resources/chat.md).
// Members and LastMessagePreview are present only when the caller asked for the
// matching $expand; Viewpoint comes with every delegated listing.
type Chat struct {
	ID                  string               `json:"id"`
	ChatType            string               `json:"chatType,omitempty"`
	Topic               *string              `json:"topic,omitempty"`
	CreatedDateTime     time.Time            `json:"createdDateTime,omitzero"`
	LastUpdatedDateTime time.Time            `json:"lastUpdatedDateTime,omitzero"`
	TenantID            string               `json:"tenantId,omitempty"`
	WebURL              string               `json:"webUrl,omitempty"`
	Viewpoint           *ChatViewpoint       `json:"viewpoint,omitempty"`
	Members             []ConversationMember `json:"members,omitempty"`
	LastMessagePreview  *Message             `json:"lastMessagePreview,omitempty"`
}

// Title is what a chat is called: its topic for a group chat, otherwise the
// other members' names.
func (c Chat) Title(other ...ConversationMember) string {
	if c.Topic != nil && *c.Topic != "" {
		return *c.Topic
	}
	names := make([]string, 0, len(other))
	for _, m := range other {
		names = append(names, m.DisplayName)
	}
	return joinNames(names)
}

// LastActivity is when the chat last moved: its newest message when the preview
// was expanded, otherwise the chat's own lastUpdatedDateTime. A chat with
// neither (no messages at all) reports the zero time, which any window treats as
// "older than everything".
func (c Chat) LastActivity() time.Time {
	if c.LastMessagePreview != nil && !c.LastMessagePreview.CreatedDateTime.IsZero() {
		return c.LastMessagePreview.CreatedDateTime
	}
	return c.LastUpdatedDateTime
}

// Unread reports whether the chat's newest message is newer than the read
// watermark. A chat that has never been read counts as unread; a chat with no
// messages does not. PLAN.md:149 documents this rule: "A chat is unread when its
// last message is newer than that timestamp."
func (c Chat) Unread() bool {
	if c.LastMessagePreview == nil {
		return false
	}
	if c.Viewpoint == nil || c.Viewpoint.LastMessageReadDateTime == nil {
		return true
	}
	return c.LastMessagePreview.CreatedDateTime.After(*c.Viewpoint.LastMessageReadDateTime)
}

// Team is microsoft.graph.team
// (refs/graph/api-reference/v1.0/resources/team.md).
type Team struct {
	ID              string    `json:"id"`
	DisplayName     string    `json:"displayName,omitempty"`
	Description     string    `json:"description,omitempty"`
	Visibility      string    `json:"visibility,omitempty"`
	IsArchived      bool      `json:"isArchived,omitempty"`
	WebURL          string    `json:"webUrl,omitempty"`
	CreatedDateTime time.Time `json:"createdDateTime,omitzero"`
	TenantID        string    `json:"tenantId,omitempty"`
}

// Channel is microsoft.graph.channel
// (refs/graph/api-reference/v1.0/resources/channel.md).
type Channel struct {
	ID              string    `json:"id"`
	DisplayName     string    `json:"displayName"`
	Description     string    `json:"description,omitempty"`
	MembershipType  string    `json:"membershipType,omitempty"`
	WebURL          string    `json:"webUrl,omitempty"`
	Email           string    `json:"email,omitempty"`
	CreatedDateTime time.Time `json:"createdDateTime,omitzero"`
}

// User is the directory user subset the CLI reads. The default property set is
// the one user-list.md documents
// (refs/graph/api-reference/v1.0/api/user-list.md:39).
type User struct {
	ID                string   `json:"id"`
	DisplayName       string   `json:"displayName,omitempty"`
	GivenName         string   `json:"givenName,omitempty"`
	Surname           string   `json:"surname,omitempty"`
	UserPrincipalName string   `json:"userPrincipalName,omitempty"`
	Mail              string   `json:"mail,omitempty"`
	JobTitle          string   `json:"jobTitle,omitempty"`
	Department        string   `json:"department,omitempty"`
	OfficeLocation    string   `json:"officeLocation,omitempty"`
	PreferredLanguage string   `json:"preferredLanguage,omitempty"`
	MobilePhone       string   `json:"mobilePhone,omitempty"`
	BusinessPhones    []string `json:"businessPhones,omitempty"`
}

// Address is the mail to show for a user: mail, then the UPN.
func (u User) Address() string {
	if u.Mail != "" {
		return u.Mail
	}
	return u.UserPrincipalName
}

// ScoredEmailAddress is scoredEmailAddress, which a person carries
// (refs/graph/api-reference/v1.0/resources/scoredemailaddress.md).
type ScoredEmailAddress struct {
	Address string  `json:"address,omitempty"`
	Score   float64 `json:"relevanceScore"`
}

// Person is the subset of microsoft.graph.person that /me/people returns. The
// API orders people by relevance, "determined by the user's communication and
// collaboration patterns", which is why it beats a directory search when
// "@yuki" should mean the Yuki you work with
// (refs/graph/api-reference/v1.0/api/user-list-people.md:12).
type Person struct {
	ID                   string               `json:"id"`
	DisplayName          string               `json:"displayName,omitempty"`
	GivenName            string               `json:"givenName,omitempty"`
	Surname              string               `json:"surname,omitempty"`
	CompanyName          string               `json:"companyName,omitempty"`
	JobTitle             string               `json:"jobTitle,omitempty"`
	Department           string               `json:"department,omitempty"`
	OfficeLocation       string               `json:"officeLocation,omitempty"`
	UserPrincipalName    string               `json:"userPrincipalName,omitempty"`
	ScoredEmailAddresses []ScoredEmailAddress `json:"scoredEmailAddresses,omitempty"`
}

// Address is the person's best e-mail: the highest-scored address.
func (p Person) Address() string {
	best := ""
	bestScore := -1.0
	for _, a := range p.ScoredEmailAddresses {
		if a.Address != "" && a.Score > bestScore {
			best, bestScore = a.Address, a.Score
		}
	}
	return best
}

// DriveItem is the driveItem subset the CLI reads
// (refs/graph/api-reference/v1.0/resources/driveitem.md).
type DriveItem struct {
	ID                   string         `json:"id"`
	Name                 string         `json:"name"`
	Size                 int64          `json:"size,omitempty"`
	CreatedDateTime      time.Time      `json:"createdDateTime,omitzero"`
	LastModifiedDateTime time.Time      `json:"lastModifiedDateTime,omitzero"`
	WebURL               string         `json:"webUrl,omitempty"`
	WebDavURL            string         `json:"webDavUrl,omitempty"`
	ETag                 string         `json:"eTag,omitempty"`
	DownloadURL          string         `json:"@microsoft.graph.downloadUrl,omitempty"`
	File                 *FileFacet     `json:"file,omitempty"`
	Folder               *FolderFacet   `json:"folder,omitempty"`
	ParentReference      *ItemReference `json:"parentReference,omitempty"`
}

// FileFacet is microsoft.graph.file.
type FileFacet struct {
	MimeType string `json:"mimeType,omitempty"`
}

// FolderFacet is microsoft.graph.folder.
type FolderFacet struct {
	ChildCount int `json:"childCount"`
}

// ItemReference is microsoft.graph.itemReference, which tells us the drive a
// channel's file lives in.
type ItemReference struct {
	DriveID   string `json:"driveId,omitempty"`
	DriveType string `json:"driveType,omitempty"`
	ID        string `json:"id,omitempty"`
	Path      string `json:"path,omitempty"`
}

// IsFolder reports whether the item is a folder rather than a file.
func (d DriveItem) IsFolder() bool { return d.Folder != nil }

// MimeType is the item's content type, when Graph reports one.
func (d DriveItem) MimeType() string {
	if d.File != nil {
		return d.File.MimeType
	}
	return ""
}

// joinNames renders a list of names the way a chat title reads.
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	case 2:
		return names[0] + ", " + names[1]
	default:
		out := ""
		for i, n := range names {
			if i > 0 {
				out += ", "
			}
			out += n
		}
		return out
	}
}
