package fakegraph

import (
	"strings"

	"github.com/floriscornel/teams-cli/internal/config"
)

// This file is the authorization model: the delegated scope each route needs,
// how a grant is resolved, and the admin-consent gate. It reuses
// internal/config so the presets the CLI asks for and the scopes the fake
// requires cannot drift apart.

// scopeAlternatives lists, per scope, the alternatives Graph offers in a 403
// body. They come from the least-privileged permission columns and their
// higher-privileged alternatives in refs/INDEX.md section 1 ("Graph endpoints,
// mapped to CLI commands") and the shared
// refs/graph/api-reference/v1.0/includes/permissions/ tables. The wording
// matters: internal/graph parses "API requires one of '<a>, <b>'" out of the
// message (internal/graph/errors.go:117).
var scopeAlternatives = map[string][]string{
	"User.Read":                {"User.Read"},
	"User.ReadBasic.All":       {"User.ReadBasic.All", "User.Read.All", "User.ReadWrite.All", "Directory.Read.All", "Directory.ReadWrite.All"},
	"People.Read":              {"People.Read"},
	"Team.ReadBasic.All":       {"Team.ReadBasic.All"},
	"TeamMember.Read.All":      {"TeamMember.Read.All"},
	"Channel.ReadBasic.All":    {"Channel.ReadBasic.All"},
	"ChannelMember.Read.All":   {"ChannelMember.Read.All"},
	"ChannelMessage.Read.All":  {"ChannelMessage.Read.All", "Group.Read.All", "Group.ReadWrite.All"},
	"ChannelMessage.Send":      {"ChannelMessage.Send"},
	"ChannelMessage.ReadWrite": {"ChannelMessage.ReadWrite", "Group.ReadWrite.All"},
	"ChannelSettings.Read.All": {"ChannelSettings.Read.All"},
	"Chat.ReadBasic":           {"Chat.ReadBasic", "Chat.Read", "Chat.ReadWrite"},
	"Chat.Read":                {"Chat.Read", "Chat.ReadWrite"},
	"Chat.ReadWrite":           {"Chat.ReadWrite"},
	"Chat.Create":              {"Chat.Create"},
	"ChatMessage.Send":         {"ChatMessage.Send"},
	"ChatMember.ReadWrite":     {"ChatMember.ReadWrite", "Chat.ReadWrite"},
	"Chat.ManageDeletion.All":  {"Chat.ManageDeletion.All"},
	"Files.Read.All":           {"Files.Read.All", "Files.Read", "Sites.Read.All"},
	"Files.ReadWrite.All":      {"Files.ReadWrite.All", "Files.ReadWrite", "Sites.ReadWrite.All"},
	// The alternatives themselves need an entry too: a route lists the
	// least-privileged scope and its higher-privileged alternatives, and a 403
	// names whichever one the caller is missing, so every scope that can
	// appear in a route list must be renderable.
	"Group.Read.All":      {"Group.Read.All", "Group.ReadWrite.All"},
	"Group.ReadWrite.All": {"Group.ReadWrite.All"},
	"Files.Read":          {"Files.Read", "Files.Read.All"},
	"Files.ReadWrite":     {"Files.ReadWrite", "Files.ReadWrite.All"},
	"Sites.Read.All":      {"Sites.Read.All", "Files.Read.All"},
	"Sites.ReadWrite.All": {"Sites.ReadWrite.All", "Files.ReadWrite.All"},
}

// scopeAlternativesOf returns the documented alternatives for a required
// scope, defaulting to the scope itself.
func scopeAlternativesOf(scope string) []string {
	if alts, ok := scopeAlternatives[scope]; ok {
		return alts
	}
	return []string{scope}
}

// missingScope returns the scope to name in a 403, or "" when the grant covers
// one of the required alternatives. The list is any-of, which is how Graph's
// permission tables read: a route lists the least-privileged scope and its
// higher-privileged alternatives. Comparison is case-insensitive, because Entra
// is not consistent about scope casing (internal/config/scopes.go,
// Scopes.Missing).
func missingScope(required, granted []string) string {
	for _, want := range required {
		if containsFold(granted, want) {
			return ""
		}
	}
	if len(required) == 0 {
		return ""
	}
	return required[0]
}

// containsFold reports whether list holds value, ignoring case.
func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

// needsAdminConsent reports whether the Graph permissions reference marks a
// scope as requiring admin consent in a delegated flow
// (refs/graph/concepts/permissions-reference.md, "AdminConsentRequired").
func needsAdminConsent(scope string) bool {
	return config.Scopes.RequiresAdminConsent(scope)
}

// adminConsentScopes returns the documented admin-consent scope set.
func adminConsentScopes() []string { return config.DelegatedAdminConsentScopes }

// defaultGrantedScopes is the permissive default grant: every scope in every
// preset plus the incremental scopes, so a test that does not exercise the
// scope matrix needs no setup. It deliberately mirrors the union of what the
// three presets and `teams chat delete` ask for (internal/config/scopes.go).
func defaultGrantedScopes() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, name := range config.Scopes.Names() {
		for _, s := range config.Scopes.Preset(name) {
			add(s)
		}
	}
	for s := range config.IncrementalScopes {
		add(s)
	}
	for _, s := range config.DelegatedAdminConsentScopes {
		add(s)
	}
	// Every scope a route names is granted too, so the permissive default
	// really covers the whole fake surface (ChannelMember.Read.All, for
	// example, is a route the CLI does not use yet and no preset names).
	// scopeAlternatives is the single list the route table draws from.
	for scope := range scopeAlternatives {
		add(scope)
	}
	return out
}
