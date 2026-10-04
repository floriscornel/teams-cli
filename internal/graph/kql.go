package graph

import (
	"strings"
	"time"
)

// This file builds the KQL query strings /search/query accepts. Only the scope
// terms the Teams page documents are emitted
// (refs/graph/concepts/search-concept-chat-messages.md:258-269): from, to,
// sent, IsMentioned, hasAttachment and mentions:<userId without dashes>.
//
// IsRead is documented but deliberately never used: it returned HTTP 500 every
// time in the spike, whatever its casing, alone or combined
// (docs/spike/phase1.md:79), and fakegraph reproduces that 500 so the CLI
// cannot start depending on it.

// SearchFilters is the CLI's search surface, which maps onto documented KQL
// scope terms.
type SearchFilters struct {
	// Text is free text, passed through as KQL so a user who types a scope term
	// of their own keeps it.
	Text string
	// From scopes to messages sent by that person (from:term).
	From string
	// To scopes to messages sent to that person; the docs mark it "partially
	// supported for the one-on-one message".
	To string
	// Since and Until become sent>= / sent<= comparisons with a full UTC
	// timestamp. PLAN.md:235 records that sent honors a time of day, so a bare
	// date is never sent.
	Since time.Time
	Until time.Time
	// MentionsMe adds IsMentioned:true.
	MentionsMe bool
	// MentionsUser adds mentions:<userId without dashes>.
	MentionsUser string
	// HasAttachment adds hasAttachment:true or hasAttachment:false.
	HasAttachment *bool
}

// Empty reports whether the filters carry no term at all, which is a usage
// error rather than an empty search.
func (f SearchFilters) Empty() bool {
	return strings.TrimSpace(f.Text) == "" && f.From == "" && f.To == "" &&
		f.Since.IsZero() && f.Until.IsZero() && !f.MentionsMe && f.MentionsUser == "" &&
		f.HasAttachment == nil
}

// KQL renders the filters as a KQL query string. Terms are joined with spaces,
// which KQL reads as AND.
func (f SearchFilters) KQL() string {
	parts := make([]string, 0, 8)
	if text := strings.TrimSpace(f.Text); text != "" {
		parts = append(parts, quoteKQLText(text))
	}
	if f.From != "" {
		parts = append(parts, "from:"+kqlTerm(f.From))
	}
	if f.To != "" {
		parts = append(parts, "to:"+kqlTerm(f.To))
	}
	if !f.Since.IsZero() {
		parts = append(parts, "sent>="+odataTime(f.Since))
	}
	if !f.Until.IsZero() {
		parts = append(parts, "sent<="+odataTime(f.Until))
	}
	if f.MentionsMe {
		parts = append(parts, "IsMentioned:true")
	}
	if f.MentionsUser != "" {
		// The documented form is a user id without its dashes.
		parts = append(parts, "mentions:"+StripDashes(strings.TrimSpace(f.MentionsUser)))
	}
	if f.HasAttachment != nil {
		parts = append(parts, "hasAttachment:"+boolTerm(*f.HasAttachment))
	}
	return strings.Join(parts, " ")
}

// StripDashes removes the hyphens from a GUID, which the mentions: term
// requires (refs/graph/concepts/search-concept-chat-messages.md:267).
func StripDashes(s string) string { return strings.ReplaceAll(s, "-", "") }

// quoteKQLText protects a free-text query from being read as a scope term: a
// property restriction looks like word:word, so a query that carries a colon is
// sent as a quoted phrase, which is the KQL reference's way of saying "this is
// text" (refs/kql/docs/general-development/keyword-query-language-kql-syntax-reference.md).
func quoteKQLText(text string) string {
	if !strings.Contains(text, ":") {
		return text
	}
	return "\"" + strings.ReplaceAll(text, "\"", "") + "\""
}

// kqlTerm renders a from:/to: value, quoting it when it carries spaces.
func kqlTerm(v string) string {
	v = strings.TrimSpace(v)
	if strings.ContainsAny(v, " \"") {
		return "\"" + strings.ReplaceAll(v, "\"", "") + "\""
	}
	return v
}

func boolTerm(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
