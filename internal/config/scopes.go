package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Scope preset names accepted by `scopes`. Each preset is a *preset for the app
// registration*, not a promise that the tenant will let a user consent to it:
// in the tenant the spike ran against, every scope needs an admin
// (docs/spike/phase1.md:26).
const (
	ScopePresetChats    = "chats"
	ScopePresetReadOnly = "read-only"
	ScopePresetFull     = "full"
)

// Presets are the ordered scope lists, corrected from the MCP's sets as PLAN.md
// "Scopes" describes:
//   - read-only adds Files.Read.All, because a channel's files live in the
//     team's SharePoint drive and Files.Read only reaches the signed-in user's
//     files (refs/graph/api-reference/v1.0/includes/permissions/channel-get-filesfolder-permissions.md:9).
//   - People.Read is what makes "@yuki" resolve to the Yuki you actually work
//     with (refs/graph/api-reference/v1.0/includes/permissions/user-list-people-permissions.md:9).
//   - full keeps Files.ReadWrite.All for channel uploads and adds
//     ChatMessage.Send, the documented least-privileged scope for
//     POST /chats/{id}/messages and the only delegated scope listed for
//     replyWithQuote (chatmessage-replywithquote-permissions.md:9).
//   - ChannelMessage.Edit is deliberately absent: it does not authorise edit or
//     delete (refs/graph/api-reference/v1.0/api/chatmessage-update.md:34 lists
//     ChannelMessage.ReadWrite; the spike got 403 with ChannelMessage.Edit).
var Presets = map[string][]string{
	ScopePresetChats: {
		"User.Read",
		"User.ReadBasic.All",
		"People.Read",
		"Chat.ReadBasic",
		"Chat.Read",
		"Chat.ReadWrite",
		"ChatMessage.Send",
		"Files.ReadWrite",
	},
	ScopePresetReadOnly: {
		"User.Read",
		"User.ReadBasic.All",
		"People.Read",
		"Team.ReadBasic.All",
		"Channel.ReadBasic.All",
		"ChannelMessage.Read.All",
		"TeamMember.Read.All",
		"Chat.Read",
		"Files.Read.All",
	},
	ScopePresetFull: {
		"User.Read",
		"User.ReadBasic.All",
		"People.Read",
		"Team.ReadBasic.All",
		"Channel.ReadBasic.All",
		"ChannelMessage.Read.All",
		"TeamMember.Read.All",
		"Chat.Read",
		"Files.Read.All",
		"ChannelMessage.Send",
		"ChannelMessage.ReadWrite",
		"Chat.ReadWrite",
		"ChatMessage.Send",
		"Files.ReadWrite.All",
	},
}

// IncrementalScopes are requested at run time by the one command that needs
// them, never by a preset. `chat delete` asks for Chat.ManageDeletion.All
// (refs/graph/api-reference/v1.0/includes/permissions/chat-delete-permissions.md:9).
var IncrementalScopes = map[string]string{
	"Chat.ManageDeletion.All": "teams chat delete",
	// The calendar scopes are requested at first use and belong to no preset:
	// adding one to a preset makes every existing login ask for it, which is the
	// AADSTS65001 failure commit f7bd612 fixed (PLAN.md "Authentication design",
	// plans/calendar.md §4.8, decision D3).
	"Calendars.Read":             "teams calendar list/show/search",
	"Calendars.Read.Shared":      "teams calendar list/show/search",
	"Calendars.ReadWrite":        "teams calendar write commands",
	"Calendars.ReadWrite.Shared": "teams calendar list --user",
	"OnlineMeetings.Read":        "teams calendar list --chat / show",
}

// AlternativeScopes are documented Graph scopes the CLI may name in the feature
// matrix but deliberately keeps out of every preset, with the reason. `Known`
// accepts them, so a profile can list one explicitly without the config
// validation complaining. The mirror's per-endpoint permission tables are the
// source for each one.
var AlternativeScopes = map[string]string{
	// Files.Read reaches only the signed-in user's files, so it cannot list a
	// channel's drive (refs/graph/api-reference/v1.0/includes/permissions/channel-get-filesfolder-permissions.md:9).
	"Files.Read": "narrower than Files.Read.All; usable for a chat attachment, not for a channel drive",
	// Chat.ReadWrite is the listed higher-privileged alternative, so Chat.Create
	// buys nothing (refs/graph/api-reference/v1.0/includes/permissions/chat-post-permissions.md:9).
	"Chat.Create": "covered by Chat.ReadWrite, which every chat preset already has",
	// Needs admin consent and is redundant with Chat.ReadWrite
	// (refs/graph/api-reference/v1.0/includes/permissions/chat-post-members-permissions.md:9).
	"ChatMember.ReadWrite": "needs admin consent; Chat.ReadWrite is the higher-privileged alternative",
	// Documented for edit/delete but never listed by the endpoint tables, and the
	// Phase 1 spike got 403 with it while holding it (docs/spike/phase1.md:27).
	"ChannelMessage.Edit": "does not authorise edit or delete; ChannelMessage.ReadWrite does",
	// Accepted by filesFolder but never least privileged, and needs admin
	// consent (refs/graph/concepts/permissions-reference.md:1669).
	"ChannelSettings.Read.All": "needs admin consent and is never least privileged",
	// Reaches every user in the tenant; User.ReadBasic.All is enough for us.
	"User.Read.All": "wider than needed; User.ReadBasic.All covers user search",
	// The calendarView permission table names Calendars.ReadBasic as least
	// privileged, but the live service needed Calendars.Read for the free/busy
	// detail and for subjects on your own calendar (plans/calendar.md §3, F1 and
	// F7), so the CLI asks for Calendars.Read.
	"Calendars.ReadBasic": "weaker than Calendars.Read, which the calendar commands need",
}

// DelegatedAdminConsentScopes are the scopes the Graph permissions reference
// marks as needing admin consent for delegated flows
// (refs/graph/concepts/permissions-reference.md, "AdminConsentRequired"). The
// list matters twice: `teams doctor` explains a 403, and `auth status
// --admin-request` prints the ticket.
//
// Files.Read.All and Files.ReadWrite.All are NOT in this list: the reference
// marks their admin-consent column as application-only, even though the live
// tenant still refused them without an admin grant (docs/spike/phase1.md:25).
// That is a tenant policy difference, so we report it as "may need consent"
// rather than "documented as admin consent".
var DelegatedAdminConsentScopes = []string{
	"ChannelMessage.Read.All",
	"ChannelMessage.ReadWrite",
	"TeamMember.Read.All",
	"ChatMember.ReadWrite",
	"Chat.ManageDeletion.All",
	"ChannelSettings.Read.All",
	"User.Read.All",
}

// MSALDefaultScopes are appended by MSAL Go itself and must never be requested
// by us: `AppendDefaultScopes` skips caller-supplied copies and adds all three
// anyway (refs/msal-go/apps/internal/oauth/ops/accesstokens/accesstokens.go:479-501).
// Requesting them would also never show up in `scp`, so an `scp`-based feature
// matrix would report them permanently missing.
var MSALDefaultScopes = []string{"openid", "profile", "offline_access"}

// scopeSet is the namespace for scope helpers; it is a struct so callers write
// `config.Scopes.For(...)` and read like a table.
type scopeSet struct{}

// Scopes provides preset lookup and scope validation.
var Scopes scopeSet

var scopeNameRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(\.[A-Za-z0-9]+)*$`)

// For resolves a `scopes` setting: empty means the full preset, a preset name
// means that preset, anything else is an explicit whitespace/comma separated
// list. The result is always a fresh slice.
func (scopeSet) For(spec string) ([]string, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return append([]string(nil), Presets[ScopePresetFull]...), nil
	}
	if preset, ok := Presets[strings.ToLower(spec)]; ok {
		return append([]string(nil), preset...), nil
	}
	items := strings.FieldsFunc(spec, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	if len(items) == 0 {
		return nil, fmt.Errorf("scopes is empty")
	}
	out := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		if err := validateScopeName(item); err != nil {
			return nil, err
		}
		if seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out, nil
}

func validateScopeName(scope string) error {
	// Checked before the shape test so the message explains the real problem:
	// MSAL appends these three itself.
	for _, def := range MSALDefaultScopes {
		if strings.EqualFold(scope, def) {
			return fmt.Errorf("%s is added by MSAL automatically and must not be requested (MSAL appends openid, profile and offline_access itself)", scope)
		}
	}
	if !scopeNameRE.MatchString(scope) {
		return fmt.Errorf("%q is not a Graph scope name (use chats, read-only, full, or a space separated list)", scope)
	}
	return nil
}

// Names lists the preset names, ordered from least to most access.
func (scopeSet) Names() []string {
	return []string{ScopePresetChats, ScopePresetReadOnly, ScopePresetFull}
}

// Preset returns a copy of one preset, or nil when the name is unknown.
func (scopeSet) Preset(name string) []string {
	preset, ok := Presets[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil
	}
	return append([]string(nil), preset...)
}

// Containing returns the name of the first preset holding scope, or "" when no
// preset does. Commands use it to say *which preset* would grant a missing
// scope, instead of just reporting a 403 later.
func (scopeSet) Containing(scope string) string {
	for _, name := range []string{ScopePresetChats, ScopePresetReadOnly, ScopePresetFull} {
		for _, s := range Presets[name] {
			if strings.EqualFold(s, scope) {
				return name
			}
		}
	}
	return ""
}

// Known reports whether a scope appears in any preset, in the incremental set,
// or in the documented alternatives table.
func (scopeSet) Known(scope string) bool {
	if (scopeSet{}).Containing(scope) != "" {
		return true
	}
	if _, ok := IncrementalScopes[scope]; ok {
		return true
	}
	_, ok := AlternativeScopes[scope]
	return ok
}

// RequiresAdminConsent reports whether the Graph permissions reference marks a
// scope as needing admin consent in a delegated flow.
func (scopeSet) RequiresAdminConsent(scope string) bool {
	for _, s := range DelegatedAdminConsentScopes {
		if strings.EqualFold(s, scope) {
			return true
		}
	}
	return false
}

// ParseGrantedScopes splits a `scp` claim into a sorted, de-duplicated list.
// The claim is documented as a space separated list
// (refs/entra/docs/identity-platform/access-token-claims-reference.md:59).
func (scopeSet) ParseGrantedScopes(scp string) []string {
	fields := strings.Fields(scp)
	seen := map[string]bool{}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// Missing returns the scopes in required that are absent from granted.
// Comparison is case-insensitive: Entra is not consistent about scope casing.
func (scopeSet) Missing(granted, required []string) []string {
	have := make(map[string]bool, len(granted))
	for _, g := range granted {
		have[strings.ToLower(g)] = true
	}
	var missing []string
	for _, r := range required {
		if !have[strings.ToLower(r)] {
			missing = append(missing, r)
		}
	}
	return missing
}

// writeScopes are the scopes the presets add that authorise a change in Teams,
// so a read-only session does not request them. Each one is checked against the
// read-only preset by TestWriteScopesAreNotReadScopes: a scope belongs here only
// if the read-only preset does not carry it, which keeps "narrowing" from ever
// removing a scope the CLI needs to read (Chat.ReadBasic, for example, is a read
// scope that the read-only preset happens not to list).
var writeScopes = []string{
	"ChannelMessage.Send",
	"ChannelMessage.ReadWrite",
	"Chat.ReadWrite",
	"ChatMessage.Send",
	"Files.ReadWrite",
	"Files.ReadWrite.All",
}

// ReadOnlyView returns the scopes a read-only session should *request*: it
// removes the write scopes and changes nothing else. It never adds a scope, and
// it never replaces an explicit list.
//
// It exists because the old behaviour — replacing the spec with the read-only
// preset — broke reads outright for a profile with an explicit `scopes` list.
// The preset asks for scopes the app registration was never consented for
// (Files.Read.All, User.ReadBasic.All, People.Read), Entra consent is
// all-or-nothing per request, and the sign-in then failed with AADSTS65001
// before any read happened: a read-only inbox sweep failed on every command,
// including `teams whoami`, while the same commands worked without the flag.
//
// Two cases:
//
//   - an empty or preset spec loses the write scopes the preset carries, so
//     `full` becomes the read-only preset exactly and `chats` keeps every read
//     scope it had;
//   - an explicit list is left exactly as configured. The CLI cannot tell which
//     of its scopes authorise writes, and the ones that do can be the only way to
//     read something (`Chat.ReadWrite`, `Files.ReadWrite.All`), so dropping one to
//     satisfy a cosmetic rule would break a read the user asked for. Writes are
//     refused before any call regardless (PLAN.md:180): narrowing is a courtesy to
//     the admin who reads the consent screen, not a security control.
func (scopeSet) ReadOnlyView(spec string, scopes []string) []string {
	if trimmed := strings.TrimSpace(spec); trimmed != "" {
		if _, isPreset := Presets[strings.ToLower(trimmed)]; !isPreset {
			return scopes
		}
	}
	writes := make(map[string]bool, len(writeScopes))
	for _, scope := range writeScopes {
		writes[strings.ToLower(scope)] = true
	}
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		if !writes[strings.ToLower(scope)] {
			out = append(out, scope)
		}
	}
	if len(out) == 0 {
		// No preset is write-only, so this is unreachable today; requesting no
		// scopes at all would be worse than requesting the configured set.
		return scopes
	}
	return out
}
