package ref

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// The Teams deep-link shapes this parser accepts, and where each rule comes from:
//
//   - refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md —
//     the authoritative page for every URL format (channel message, chat message,
//     /l/chat and its compose form, /l/team, /l/channel, /l/file).
//   - refs/msteams/msteams-platform/concepts/build-and-test/deep-links.md — the two
//     protocol handlers: "1. **HTTPS** … A deep link must start with
//     https://teams.microsoft.com/l/" and "2. **MSTEAMS**" (msteams:// skips the
//     client selector and opens the desktop client directly).
//   - refs/INDEX.md section 5 ("Teams platform") — the parser notes: Graph's own
//     webUrl percent-encodes the channel id and orders the parameters differently,
//     so both encodings and any parameter order are accepted; /l/message carries
//     chat messages too, so the query string is the discriminator; a message link
//     without groupId is unresolvable, so the resolver decides (PLAN.md:164).
//   - PLAN.md:164 ("Smart references") — parse tolerantly, reject the non-message
//     families with a usage error, and accept the newer teams.cloud.microsoft host
//     defensively because the mirror does not document it.
const (
	// urlschemeHTTPS is the default handler in most deep links
	// (refs/msteams/msteams-platform/concepts/build-and-test/deep-links.md:53).
	urlschemeHTTPS = "https"
	// urlschemeMsteams is the second handler; it skips the browser client selector
	// (refs/msteams/msteams-platform/concepts/build-and-test/deep-links.md:63 and
	// refs/INDEX.md section 5).
	urlschemeMsteams = "msteams"

	// urlhostTeams is the only web host the docs use: "A deep link must start with
	// https://teams.microsoft.com/l/"
	// (refs/msteams/msteams-platform/concepts/build-and-test/deep-links.md:61).
	urlhostTeams = "teams.microsoft.com"
	// urlhostTeamsCloud is the newer host. It is deliberately absent from the
	// mirror (refs/INDEX.md "teams.cloud.microsoft deep links"), so accepting it is
	// a defensive extension of PLAN.md:164 rather than a documented shape.
	urlhostTeamsCloud = "teams.cloud.microsoft"

	// urlpathPrefix is the path every documented deep link starts with
	// (refs/msteams/msteams-platform/concepts/build-and-test/deep-links.md:61).
	urlpathPrefix = "/l/"
)

// pathContext is the JSON object that /l/message carries in its context query
// parameter to mark a chat message:
// https://teams.microsoft.com/l/message/<chatId>/<messageId>?context={"contextType":"chat"}
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md,
// "Deep link to navigate to chat messages"). The docs say "Specify the contextType
// as chat"; the surrounding schema is undocumented, so only contextType is read.
type pathContext struct {
	ContextType string `json:"contextType"`
}

// ParseURL parses a Teams deep link. ok is false when raw is not a Teams
// URL at all (the caller then tries the other reference shapes).
//
// ok is true for every URL that is shaped like a Teams deep link, even when the
// parser refuses it: err is then set for the families the CLI cannot open
// (/l/app, /l/entity, /l/task, /l/call, /l/meeting*, PLAN.md:164), so the caller
// maps one error class to exit code 2 instead of silently treating a pasted URL
// as a raw id. A message link without groupId parses with an empty TeamID: the
// resolver decides whether to fail with exit 4 or ask for --team (PLAN.md:164 and
// refs/INDEX.md section 5).
func ParseURL(raw string) (ref Ref, ok bool, err error) {
	ref.Raw = raw

	// url.Parse never panics and accepts almost anything, including msteams://
	// URLs with unencoded ":" in their path (see parsePath).
	u, perr := url.Parse(raw)
	if perr != nil {
		return Ref{Raw: raw}, false, nil
	}
	if !isTeamsScheme(u.Scheme) || !isTeamsHost(u.Host) {
		return Ref{Raw: raw}, false, nil
	}
	ref.Host = u.Host
	segments := parsePath(u.EscapedPath())
	if len(segments) == 0 {
		return ref, false, nil
	}

	// Kind and Raw are filled in here; each family parser adds its own fields.
	base := Ref{Raw: raw, Host: u.Host}

	switch segments[0] {
	case "message":
		return parseMessage(u, segments, base)
	case "channel":
		return parseChannel(u, segments, base)
	case "team":
		return parseTeam(u, segments, base)
	case "chat":
		return parseChat(u, segments, base)
	case "file":
		return parseFile(u, segments, base)
	case "app", "entity", "task", "call":
		return base, true, urlFamilyNotOpenable(segments[0])
	case "meeting", "meeting-join", "meeting-lobby":
		// /l/meeting* is three shapes in the docs — /l/meeting-join, /l/meeting-lobby
		// and /l/meeting/ — and none of them is a reference the CLI can resolve.
		return base, true, urlFamilyNotOpenable("meeting")
	default:
		// An unknown family is not a reference shape we know, so the caller falls
		// through to the id, name-path and e-mail parsers.
		return base, false, nil
	}
}

// isTeamsScheme reports whether scheme is one of the two documented deep-link
// protocol handlers (refs/msteams/msteams-platform/concepts/build-and-test/deep-links.md,
// "protocol handlers": https and msteams).
func isTeamsScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case urlschemeHTTPS, urlschemeMsteams:
		return true
	default:
		return false
	}
}

// isTeamsHost reports whether host is a host this parser accepts:
// teams.microsoft.com, which the docs require
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-links.md:61), and
// teams.cloud.microsoft, which the mirror does not document at all
// (refs/INDEX.md "teams.cloud.microsoft deep links") but which PLAN.md:164 accepts
// defensively. The port and IPv6 brackets are stripped, and the comparison is
// case-insensitive because hosts are.
func isTeamsHost(host string) bool {
	host = strings.ToLower(host)
	if rest, port, ok := strings.Cut(host, ":"); ok && isDigits(port) {
		host = rest
	}
	switch host {
	case urlhostTeams, urlhostTeamsCloud:
		return true
	default:
		// Any other host, including look-alikes such as teams.microsoft.com.evil.io,
		// is not a Teams deep link.
		return false
	}
}

// isDigits reports whether s is a non-empty run of ASCII digits, which is how a
// valid port looks after strings.Cut.
func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parsePath splits the /l/<family>/<id>… part into unescaped segments: ["message",
// <containerId>, <messageId>] for the documented message links
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md) and
// ["channel", <channelId>, <channelName>] for the channel form. It returns nil
// when the path is not under /l/, accepts a trailing slash, and ignores path
// segments past the ones a family documents — the extra-segment tolerance is
// PLAN.md:164's "parse tolerantly", not a documented shape. Percent-escapes are
// decoded per segment because both encodings occur: the docs show a raw
// 19:…@thread.tacv2 while Graph's own webUrl and the /l/team example show
// 19%3A…%40thread.tacv2 (refs/INDEX.md section 5).
func parsePath(escapedPath string) []string {
	if !strings.HasPrefix(escapedPath, urlpathPrefix) {
		return nil
	}
	raw := strings.Split(strings.Trim(escapedPath[len(urlpathPrefix):], "/"), "/")
	segments := make([]string, 0, len(raw))
	for _, seg := range raw {
		if s := unescapeSegment(seg); s != "" {
			segments = append(segments, s)
		}
	}
	return segments
}

// unescapeSegment percent-decodes one path segment. A segment that carries no
// "%" is returned as is, so an id that was already decoded (and may contain a
// literal "%") is never decoded twice; a malformed escape is kept verbatim
// because decoding is best-effort tolerance, not validation (PLAN.md:164).
func unescapeSegment(seg string) string {
	if !strings.Contains(seg, "%") {
		return seg
	}
	decoded, err := url.PathUnescape(seg)
	if err != nil {
		return seg
	}
	return decoded
}

// parseMessage handles /l/message/{containerId}/{messageId}, the documented
// channel-message and chat-message link. The path is identical for both families,
// so "the ONLY discriminator is the query string": context={"contextType":"chat"}
// means a chat message, anything else a channel message
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md, "Deep
// link to navigate to chat messages", and refs/INDEX.md section 5).
//
// A channel message without groupId still parses, with an empty TeamID: the
// parser is tolerant (PLAN.md:164) and the resolver owns the exit-4 decision.
func parseMessage(u *url.URL, segments []string, base Ref) (Ref, bool, error) {
	if len(segments) < 3 || segments[1] == "" || segments[2] == "" {
		return base, true, fmt.Errorf("URL %q is not a usable Teams deep link: it is missing the container or message id of /l/message/{containerId}/{messageId}", u.String())
	}
	containerID, messageID := segments[1], segments[2]
	groupID := u.Query().Get("groupId")
	ref := Ref{Raw: base.Raw, Host: base.Host, Kind: KindMessage, MessageID: messageID}

	// chatID is the same path element as the channel id; the query string decides
	// which of the two it is.
	if isChatContext(u) || (groupID == "" && LooksLikeChatID(containerID)) {
		ref.InChat = true
		ref.ChatID = containerID
		return ref, true, nil
	}

	ref.ChannelID = containerID
	ref.TeamID = groupID
	ref.TeamName = u.Query().Get("teamName")
	ref.ChannelName = u.Query().Get("channelName")
	ref.TenantID = u.Query().Get("tenantId")
	// The docs' own example repeats the message id as parentMessageId; when a link
	// omits the parameter the root message is its own thread root, so the message id
	// is what a reply needs.
	ref.ParentMessageID = u.Query().Get("parentMessageId")
	if ref.ParentMessageID == "" {
		ref.ParentMessageID = messageID
	}
	return ref, true, nil
}

// isChatContext reports whether the link carries the documented chat discriminator
// context={"contextType":"chat"} (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md,
// "Deep link to navigate to chat messages"). Query() already percent-decodes, so
// the example's context=%7B%22contextType%22:%22chat%22%7D arrives as JSON. An
// unparseable or absent context means "not a chat": an unknown parameter is
// ignored rather than an error (PLAN.md:164).
func isChatContext(u *url.URL) bool {
	raw := u.Query().Get("context")
	if raw == "" {
		return false
	}
	var ctx pathContext
	if err := json.Unmarshal([]byte(raw), &ctx); err != nil {
		return false
	}
	return strings.EqualFold(ctx.ContextType, "chat")
}

// parseChannel handles the documented channel links
// /l/channel/{channelId}/{channelName}?groupId=…
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md, "Deep
// link to navigate to channel"), including the private-channel &ngc=true and the
// shared-channel &ngc=true&allowXTenantAccess=true variants: both parameters are
// unknown to this parser and therefore ignored (PLAN.md:164).
func parseChannel(u *url.URL, segments []string, base Ref) (Ref, bool, error) {
	if len(segments) < 2 {
		return base, true, fmt.Errorf("URL %q is not a usable Teams deep link: it is missing the channel id of /l/channel/{channelId}/{channelName}", u.String())
	}
	ref := Ref{Raw: base.Raw, Host: base.Host, Kind: KindChannel, ChannelID: segments[1]}
	if len(segments) > 2 {
		ref.ChannelName = segments[2]
	}
	q := u.Query()
	ref.TeamID = q.Get("groupId")
	ref.TenantID = q.Get("tenantId")
	return ref, true, nil
}

// parseTeam handles /l/team/{channelId}/conversations?groupId=…&tenantId=…
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md, "Deep
// link to navigate to a team"). The path element is documented as a *channel* id
// ("channelId: Channel ID of the conversation (URL encoded)"), so it is recorded
// in ChannelID; the team id is the groupId query parameter, and the trailing
// /conversations is a constant, not an id.
func parseTeam(u *url.URL, segments []string, base Ref) (Ref, bool, error) {
	if len(segments) < 2 {
		return base, true, fmt.Errorf("URL %q is not a usable Teams deep link: it is missing the channel id of /l/team/{channelId}/conversations", u.String())
	}
	q := u.Query()
	ref := Ref{Raw: base.Raw, Host: base.Host, Kind: KindTeam, ChannelID: segments[1]}
	ref.TeamID = q.Get("groupId")
	ref.TenantID = q.Get("tenantId")
	return ref, true, nil
}

// parseChat handles both documented chat forms
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md):
// /l/chat/{chatId}/conversations, which names an existing 19:… chat, and the
// compose form /l/chat/0/0?users=a@x,b@x&topicName=…, whose "0/0" placeholders
// name participants rather than a chat. For the compose form the users (split on
// ",") become Path, because the resolver creates the one-on-one chat or scans for
// it; User is set only when exactly one user is named, as PLAN.md:166 requires a
// single person for the "@person" form.
func parseChat(u *url.URL, segments []string, base Ref) (Ref, bool, error) {
	if len(segments) < 2 {
		return base, true, fmt.Errorf("URL %q is not a usable Teams deep link: it is missing the chat id of /l/chat/{chatId}/conversations", u.String())
	}
	ref := Ref{Raw: base.Raw, Host: base.Host, Kind: KindChat}
	if segments[1] != "0" {
		ref.ChatID = segments[1]
		return ref, true, nil
	}

	// The /l/chat/0/0 compose form: the two zeros are placeholders and the
	// participants are in the users parameter.
	users := splitUsers(u.Query().Get("users"))
	if len(users) == 0 {
		return ref, false, nil
	}
	ref.Path = users
	if len(users) == 1 {
		ref.User = users[0]
	}
	return ref, true, nil
}

// splitUsers splits the compose form's comma-separated users parameter, dropping
// empty entries ("The User ID parameter supports the Microsoft Entra
// UserPrincipalName, such as an email address only":
// refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md,
// "Configure deep link to start a chat manually").
func splitUsers(users string) []string {
	if users == "" {
		return nil
	}
	out := make([]string, 0, strings.Count(users, ",")+1)
	for _, user := range strings.Split(users, ",") {
		if user = strings.TrimSpace(user); user != "" {
			out = append(out, user)
		}
	}
	return out
}

// parseFile handles /l/file/{fileId}?… (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md,
// "Generate deep link to a file in a channel"). The link also carries fileType,
// objectUrl, baseUrl, serviceName and threadId; all of them are query parameters
// this parser has no field for, so they are ignored (PLAN.md:164), and the team
// is the groupId, which the page documents as "Group ID of the file".
func parseFile(u *url.URL, segments []string, base Ref) (Ref, bool, error) {
	if len(segments) < 2 {
		return base, true, fmt.Errorf("URL %q is not a usable Teams deep link: it is missing the file id of /l/file/{fileId}", u.String())
	}
	ref := Ref{Raw: base.Raw, Host: base.Host, Kind: KindFile, FileID: segments[1]}
	ref.TeamID = u.Query().Get("groupId")
	return ref, true, nil
}

// urlFamilyNotOpenable builds the usage error for a Teams deep link family the CLI
// cannot resolve. PLAN.md:164 lists exactly these families — /l/app, /l/entity,
// /l/task, /l/call and /l/meeting* — and requires a usage error (exit code 2 per
// PLAN.md "Exit codes"), which the caller maps from this error; the parser still
// reports ok=true so the caller can tell a rejected deep link from a string that
// was never a URL.
func urlFamilyNotOpenable(family string) error {
	return fmt.Errorf("cannot open the Teams deep link family /l/%s: the teams CLI resolves only message, channel, team, chat and file links (PLAN.md \"Smart references\")", family)
}
