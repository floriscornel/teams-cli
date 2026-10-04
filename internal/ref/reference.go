// Package ref turns the flexible references the CLI accepts into concrete
// Microsoft Graph ids: Teams deep links, "Team/Channel/Message" name paths,
// "@person", e-mail addresses, aliases and raw ids.
//
// PLAN.md "Smart references" is the specification for this package. Its two
// halves are the parser (ParseURL, Parse) and the resolver (Resolver), which is
// the only part that talks to Graph or the entity cache.
//
// Everything here is tolerant by design. A pasted "Copy link" URL may carry the
// channel id raw (19:...@thread.tacv2) or percent-encoded, its query parameters
// in any order, and parameters we do not know; the parser accepts all of it and
// the resolver rejects only what cannot be resolved at all.
package ref

import "strings"

// Kind is what a reference points at.
type Kind string

// Reference kinds. KindUnknown is what a parse produces when the string is a
// bare Graph id whose meaning depends on the command that accepted it ("19:..."
// is a channel id in one command and a chat id in another), so the resolver,
// not the parser, decides.
const (
	KindUnknown Kind = ""
	KindTeam    Kind = "team"
	KindChannel Kind = "channel"
	KindChat    Kind = "chat"
	KindMessage Kind = "message"
	KindUser    Kind = "user"
	KindFile    Kind = "file"
)

// Ref is a parsed, unresolved reference. Only the fields that the input carried
// are set; everything else is filled in by the Resolver.
type Ref struct {
	Kind Kind
	// Raw is the input, kept for error messages and for the entity cache key.
	Raw string

	// TeamID and TeamName come from a deep link (groupId, teamName or the name
	// path's first element).
	TeamID   string
	TeamName string

	// ChannelID and ChannelName come from a deep link or a name path.
	ChannelID   string
	ChannelName string

	// ChatID is a chat's 19:... id, from a deep link or a raw id; ChatTopic is
	// the display name when the chat was matched by one.
	ChatID    string
	ChatTopic string

	// MessageID and ParentMessageID are the message a link or a name path points
	// at. InChat records that the message lives in a chat: /l/message/ carries
	// both families and the query string is the only discriminator
	// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md,
	// "Deep link to navigate to chat messages").
	MessageID       string
	ParentMessageID string
	InChat          bool

	// User is the person form as typed: a display name, an e-mail, a UPN or a
	// user id. UserID, UserName and UserMail are filled in by the resolver.
	User     string
	UserID   string
	UserName string
	UserMail string
	// IsEmail records that User looks like an address, which resolves faster
	// (an exact GET /users/{id} instead of a directory search).
	IsEmail bool

	// FileID is a /l/file/ link's file id.
	FileID string

	// Path is a name path split on "/": ["Engineering", "General"] or
	// ["Engineering", "General", "1758..."].
	Path []string

	// Target is what an alias points at, when the reference came from one; it is
	// empty for a direct reference. Raw stays the text the user typed, so an error
	// message quotes what they wrote, while a name lookup searches Target: an alias
	// for a chat topic has to match the topic, not the alias name (PLAN.md:168).
	Target string

	// Host is the deep link's host, so the parser can accept the newer
	// teams.cloud.microsoft host defensively (PLAN.md:164).
	Host string
	// TenantID is the deep link's tenantId parameter, when present.
	TenantID string
}

// IsZero reports whether nothing was parsed out of the input.
func (r Ref) IsZero() bool {
	return r.Kind == KindUnknown && len(r.Path) == 0 && r.TeamID == "" && r.ChannelID == "" &&
		r.ChatID == "" && r.MessageID == "" && r.User == ""
}

// NamePath returns the "/"-joined name path, or "" when the input was not one.
func (r Ref) NamePath() string { return strings.Join(r.Path, "/") }

// LooksLikeChatID reports whether s has the shape of a chat id. Graph chat ids
// start with "19:" and end in a thread marker; a channel id shares the prefix
// but uses @thread.tacv2, so the suffix is what separates the two families
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md).
func LooksLikeChatID(s string) bool {
	if !strings.HasPrefix(s, "19:") {
		return false
	}
	lower := strings.ToLower(s)
	return !strings.Contains(lower, "@thread.tacv2")
}

// LooksLikeChannelID reports whether s has the shape of a channel id.
func LooksLikeChannelID(s string) bool {
	return strings.HasPrefix(s, "19:") && strings.Contains(strings.ToLower(s), "@thread.tacv2")
}
