package config

import (
	"sort"
	"strings"
)

// Feature is one row of the feature matrix: what a command needs, which preset
// grants it, and where that claim comes from. The scope list is an *any-of*
// list (the documented least-privileged permission plus its listed
// alternatives), because Graph accepts any one of them.
//
// The matrix exists for two reasons in PLAN.md: `auth status` and `doctor` read
// the token's `scp` claim against it (no extra probing needed), and a command
// whose scope is missing must fail before it calls Graph, naming the scope and
// the preset that has it.
type Feature struct {
	// ID is a stable identifier, also used by tests.
	ID string
	// Command is the CLI shape, for `doctor` output.
	Command string
	// Scopes are the accepted alternatives, least privileged first.
	Scopes []string
	// Preset is the smallest preset that contains one of Scopes, or "" when no
	// preset does (incremental consent).
	Preset string
	// AdminConsent marks the features the docs say need an admin grant.
	AdminConsent bool
	// Phase is the implementation phase from PLAN.md.
	Phase int
	// Source cites the refs/ page the permissions came from.
	Source string
}

// matrix is the Phase 3/4 command surface, from refs/INDEX.md section 1 and the
// per-endpoint permission includes. Rows for commands that do not exist yet are
// still listed: `doctor` reports them as "not granted", which is how an admin
// learns what the ticket needs to cover.
var matrix = []Feature{
	{
		ID: "team-list", Command: "teams team list", Scopes: []string{"Team.ReadBasic.All"}, Preset: ScopePresetReadOnly, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/user-list-joinedteams-permissions.md:9",
	},
	{
		ID: "team-show", Command: "teams team show <team>", Scopes: []string{"Team.ReadBasic.All"}, Preset: ScopePresetReadOnly, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/team-get-permissions.md:9",
	},
	{
		ID: "channel-list", Command: "teams channel list <team>", Scopes: []string{"Channel.ReadBasic.All"}, Preset: ScopePresetReadOnly, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/channel-list-permissions.md:9",
	},
	{
		ID: "channel-show", Command: "teams channel show <channel>", Scopes: []string{"Channel.ReadBasic.All"}, Preset: ScopePresetReadOnly, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/channel-get-permissions.md:9",
	},
	{
		ID: "channel-read", Command: "teams channel read <channel>", Scopes: []string{"ChannelMessage.Read.All"}, Preset: ScopePresetReadOnly, AdminConsent: true, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/channel-list-messages-permissions.md:9",
	},
	{
		ID: "channel-files", Command: "teams channel files <channel>", Scopes: []string{"Files.Read.All", "Files.Read"}, Preset: ScopePresetReadOnly, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/channel-get-filesfolder-permissions.md:9",
	},
	{
		ID: "thread-read-channel", Command: "teams thread read <message> (channel)", Scopes: []string{"ChannelMessage.Read.All"}, Preset: ScopePresetReadOnly, AdminConsent: true, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/api/chatmessage-get.md:27",
	},
	{
		ID: "thread-read-chat", Command: "teams thread read <message> (chat)", Scopes: []string{"Chat.Read", "Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/api/chatmessage-get.md:37",
	},
	{
		ID: "chat-list", Command: "teams chat list", Scopes: []string{"Chat.ReadBasic", "Chat.Read", "Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/api/chat-list.md:31",
	},
	{
		ID: "chat-show", Command: "teams chat show <chat>", Scopes: []string{"Chat.ReadBasic", "Chat.Read"}, Preset: ScopePresetChats, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chat-get-permissions.md:9",
	},
	{
		ID: "chat-read", Command: "teams chat read <chat>", Scopes: []string{"Chat.Read", "Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chat-list-messages-permissions.md:9",
	},
	{
		ID: "unread", Command: "teams unread", Scopes: []string{"Chat.Read", "Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/resources/chatviewpoint.md:23",
	},
	{
		ID: "search", Command: "teams search <query>", Scopes: []string{"Chat.Read", "Chat.ReadWrite", "ChannelMessage.Read.All"}, Preset: ScopePresetChats, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/resources/search-api-overview.md:47",
	},
	{
		ID: "mentions", Command: "teams mentions", Scopes: []string{"Chat.Read", "Chat.ReadWrite", "ChannelMessage.Read.All"}, Preset: ScopePresetChats, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/resources/search-api-overview.md:47",
	},
	{
		ID: "user-search", Command: "teams user search <q>", Scopes: []string{"User.ReadBasic.All"}, Preset: ScopePresetReadOnly, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/api/user-list.md:28",
	},
	{
		ID: "user-show", Command: "teams user show <user>", Scopes: []string{"User.ReadBasic.All", "User.Read"}, Preset: ScopePresetReadOnly, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/api/user-get.md:31",
	},
	{
		ID: "whoami", Command: "teams whoami", Scopes: []string{"User.Read"}, Preset: ScopePresetChats, Phase: 2,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/user-get-permissions.md:3",
	},
	{
		ID: "file-download", Command: "teams file download <message>", Scopes: []string{"Files.Read", "Files.Read.All"}, Preset: ScopePresetReadOnly, Phase: 3,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/driveitem-get-content-permissions.md:9",
	},
	{
		ID: "post-channel", Command: "teams post <channel>", Scopes: []string{"ChannelMessage.Send"}, Preset: ScopePresetFull, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chatmessage-post-permissions.md:9",
	},
	{
		ID: "post-chat", Command: "teams post <chat>", Scopes: []string{"ChatMessage.Send", "Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chat-post-messages-permissions.md:9",
	},
	{
		ID: "reply-channel", Command: "teams reply <message> (channel)", Scopes: []string{"ChannelMessage.Send"}, Preset: ScopePresetFull, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chatmessage-post-replies-permissions.md:9",
	},
	{
		ID: "reply-chat", Command: "teams reply <message> (chat, replyWithQuote)", Scopes: []string{"ChatMessage.Send", "Chat.ReadWrite"}, Preset: ScopePresetFull, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chatmessage-replywithquote-permissions.md:9",
	},
	{
		ID: "edit-channel", Command: "teams edit <message> (channel)", Scopes: []string{"ChannelMessage.ReadWrite"}, Preset: ScopePresetFull, AdminConsent: true, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/api/chatmessage-update.md:34",
	},
	{
		ID: "edit-chat", Command: "teams edit <message> (chat)", Scopes: []string{"Chat.ReadWrite"}, Preset: ScopePresetFull, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/api/chatmessage-update.md:45",
	},
	{
		ID: "delete-channel", Command: "teams delete <message> (channel)", Scopes: []string{"ChannelMessage.ReadWrite"}, Preset: ScopePresetFull, AdminConsent: true, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md:29",
	},
	{
		ID: "delete-chat", Command: "teams delete <message> (chat)", Scopes: []string{"Chat.ReadWrite"}, Preset: ScopePresetFull, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md:37",
	},
	{
		ID: "react-channel", Command: "teams react <message> (channel)", Scopes: []string{"ChannelMessage.Send"}, Preset: ScopePresetFull, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/api/chatmessage-setreaction.md:27",
	},
	{
		ID: "react-chat", Command: "teams react <message> (chat)", Scopes: []string{"Chat.ReadWrite", "ChatMessage.Send"}, Preset: ScopePresetFull, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/api/chatmessage-setreaction.md:35",
	},
	{
		ID: "chat-create", Command: "teams chat create", Scopes: []string{"Chat.Create", "Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chat-post-permissions.md:9",
	},
	{
		ID: "chat-add-member", Command: "teams chat add-member", Scopes: []string{"Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chat-post-members-permissions.md:9",
	},
	{
		ID: "chat-delete", Command: "teams chat delete", Scopes: []string{"Chat.ManageDeletion.All"}, AdminConsent: true, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chat-delete-permissions.md:9",
	},
	{
		ID: "chat-mark-read", Command: "teams chat mark-read", Scopes: []string{"Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chat-markchatreadforuser-permissions.md:9",
	},
	{
		ID: "chat-mark-unread", Command: "teams chat mark-unread", Scopes: []string{"Chat.ReadWrite"}, Preset: ScopePresetChats, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/chat-markchatunreadforuser-permissions.md:9",
	},
	{
		ID: "post-file-channel", Command: "teams post --file (channel)", Scopes: []string{"Files.ReadWrite.All"}, Preset: ScopePresetFull, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/driveitem-put-content-permissions.md:9",
	},
	{
		ID: "post-file-chat", Command: "teams post --file (chat)", Scopes: []string{"Files.ReadWrite", "Files.ReadWrite.All"}, Preset: ScopePresetChats, Phase: 4,
		Source: "refs/graph/api-reference/v1.0/includes/permissions/driveitem-put-content-permissions.md:9",
	},
}

// Features returns every row of the matrix, ordered by phase then command so
// `teams doctor` output and golden files are stable.
func Features() []Feature {
	out := append([]Feature(nil), matrix...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Phase != out[j].Phase {
			return out[i].Phase < out[j].Phase
		}
		return out[i].Command < out[j].Command
	})
	return out
}

// FeatureByID looks one row up.
func FeatureByID(id string) (Feature, bool) {
	for _, f := range matrix {
		if f.ID == id {
			return f, true
		}
	}
	return Feature{}, false
}

// FeatureStatus is one matrix row evaluated against a token's granted scopes.
type FeatureStatus struct {
	Feature
	// Granted is true when at least one of Scopes is in the token.
	Granted bool
	// Missing lists the alternatives the token does not carry.
	Missing []string
}

// Evaluate computes which features the granted scopes unlock.
func Evaluate(granted []string) []FeatureStatus {
	out := make([]FeatureStatus, 0, len(matrix))
	for _, f := range Features() {
		missing := Scopes.Missing(granted, f.Scopes)
		out = append(out, FeatureStatus{
			Feature: f,
			Granted: len(missing) < len(f.Scopes),
			Missing: missing,
		})
	}
	return out
}

// ScopeHint explains how to obtain a missing scope: which preset carries it, or
// that only `teams chat delete` requests it incrementally.
func ScopeHint(scope string) string {
	if preset := Scopes.Containing(scope); preset != "" {
		hint := "add it to the app registration and re-consent, then set scopes = \"" + preset + "\" for this profile"
		if Scopes.RequiresAdminConsent(scope) {
			hint = "an admin must consent to " + scope + " (preset \"" + preset + "\"); see `teams auth status --admin-request`"
		}
		return hint
	}
	if command, ok := IncrementalScopes[scope]; ok {
		return scope + " is requested on demand by " + command + ", which needs interactive sign-in"
	}
	if reason, ok := AlternativeScopes[scope]; ok {
		return scope + " is not in any preset: " + reason
	}
	return "no preset includes " + scope + "; add it explicitly to the profile's scopes"
}

// MissingScope is the error a command returns when the access token does not
// carry a scope the command needs. The CLI maps it to exit code 3 and prints
// Hint, so the user learns which preset (or admin ticket) closes the gap before
// Graph answers 403.
type MissingScope struct {
	// Scope is the first missing scope.
	Scope string
	// Message names the command and every acceptable scope.
	Message string
	// Hint is the fix, from ScopeHint.
	Hint string
}

// Error implements error.
func (e *MissingScope) Error() string { return e.Message }

// NewMissingScopeError builds the exit-3 error PLAN.md requires: the command
// fails before any Graph call, naming the scope and the preset that has it.
func NewMissingScopeError(command string, missing []string) *MissingScope {
	if len(missing) == 0 {
		return nil
	}
	msg := command + " needs " + strings.Join(missing, ", ")
	if len(missing) > 1 {
		msg = command + " needs one of " + strings.Join(missing, ", ")
	}
	return &MissingScope{Scope: missing[0], Message: msg, Hint: ScopeHint(missing[0])}
}

// EnsureScope reports whether granted scopes satisfy a feature, returning the
// ready-to-return error when they do not. Commands call it before building a
// request; the `TEAMS_ACCESS_TOKEN` escape hatch skips it, because that token is
// not checked for scopes at all (PLAN.md:105).
func EnsureScope(command string, granted, required []string) error {
	if len(required) == 0 {
		return nil
	}
	missing := Scopes.Missing(granted, required)
	if len(missing) == len(required) {
		return NewMissingScopeError(command, missing)
	}
	return nil
}
