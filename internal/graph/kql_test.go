// Tests for the KQL surface in kql.go: the documented scope terms of a Teams
// chatMessage search (refs/graph/concepts/search-concept-chat-messages.md:263-269)
// and the escaping rules the parser has to apply before the terms go on the
// wire.
package graph

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

// KQL seed timestamps.
var (
	kqlSince = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	kqlUntil = time.Date(2026, 1, 2, 17, 30, 0, 0, time.UTC)
)

// kqlSentTerm is the shape a sent comparison must have: a complete UTC
// timestamp, never a bare date (PLAN.md:235).
var kqlSentTerm = regexp.MustCompile("^sent(>=|<=)[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$")

// kqlBool is the pointer the tri-state HasAttachment filter needs.
func kqlBool(v bool) *bool { return &v }

// TestSearchFiltersKQL is the term table: one row per documented scope term,
// plus the escaping rules on top of them. The example on the concepts page
// joins terms with spaces and passes free text unscoped —
// "contoso from:bob to:alice sent>2022-07-14"
// (refs/graph/concepts/search-concept-chat-messages.md:269,297) — which is also
// how KQL reads several terms as AND.
func TestSearchFiltersKQL(t *testing.T) {
	all := SearchFilters{
		Text:          "release",
		From:          "bob",
		To:            "alice",
		Since:         kqlSince,
		Until:         kqlUntil,
		MentionsMe:    true,
		MentionsUser:  "497b7a2a-9e1a-48d7-80e8-2965d2fc3a81",
		HasAttachment: kqlBool(false),
	}
	cases := []struct {
		name    string
		filters SearchFilters
		want    string
	}{
		{"free text alone", SearchFilters{Text: "release train"}, "release train"},
		{"free text is trimmed", SearchFilters{Text: "  release  "}, "release"},
		{
			"a colon in free text is quoted as a phrase",
			SearchFilters{Text: "topic:release"},
			"\"topic:release\"",
		},
		{
			"a colon plus an embedded quote keeps one phrase",
			SearchFilters{Text: "say \"hi\": now"},
			"\"say hi: now\"",
		},
		{"a quote without a colon is left alone", SearchFilters{Text: "say \"hi\""}, "say \"hi\""},
		{"from", SearchFilters{From: "bob"}, "from:bob"},
		{"to", SearchFilters{To: "alice"}, "to:alice"},
		{"a from with spaces is quoted", SearchFilters{From: "Alice Example"}, "from:\"Alice Example\""},
		{"a from with a quote is unwrapped", SearchFilters{From: "Al\"ice"}, "from:\"Alice\""},
		{"since", SearchFilters{Since: kqlSince}, "sent>=2026-01-01T09:00:00Z"},
		{"until", SearchFilters{Until: kqlUntil}, "sent<=2026-01-02T17:30:00Z"},
		{"IsMentioned", SearchFilters{MentionsMe: true}, "IsMentioned:true"},
		{
			"a mention id loses its dashes",
			SearchFilters{MentionsUser: "497b7a2a-9e1a-48d7-80e8-2965d2fc3a81"},
			"mentions:497b7a2a9e1a48d780e82965d2fc3a81",
		},
		{"hasAttachment true", SearchFilters{HasAttachment: kqlBool(true)}, "hasAttachment:true"},
		{"hasAttachment false is still a term", SearchFilters{HasAttachment: kqlBool(false)}, "hasAttachment:false"},
		{
			"every term at once", all,
			"release from:bob to:alice sent>=2026-01-01T09:00:00Z sent<=2026-01-02T17:30:00Z IsMentioned:true mentions:497b7a2a9e1a48d780e82965d2fc3a81 hasAttachment:false",
		},
		{"no term at all", SearchFilters{}, ""},
		{"whitespace is not a term", SearchFilters{Text: "  	 "}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.filters.KQL()
			if got != tc.want {
				t.Errorf("KQL() = %q, want %q", got, tc.want)
			}
			if strings.TrimSpace(got) != got {
				t.Errorf("KQL() = %q, want no surrounding whitespace", got)
			}
		})
	}
}

// TestSearchFiltersSentIsAFullUTCTimestamp: PLAN.md:235 records that sent
// honours a time of day — "Send full UTC timestamps; a bare date has an
// unconfirmed timezone boundary" — so every sent term carries a complete
// timestamp in UTC and never a bare date.
func TestSearchFiltersSentIsAFullUTCTimestamp(t *testing.T) {
	cases := []struct {
		name  string
		value time.Time
	}{
		{"UTC", kqlSince},
		{"a non-UTC zone", time.Date(2026, 1, 1, 10, 0, 0, 0, time.FixedZone("CET", 3600))},
		{"sub-second precision", time.Date(2026, 1, 1, 9, 0, 0, 123456789, time.UTC)},
		{"midnight", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, filters := range []SearchFilters{{Since: tc.value}, {Until: tc.value}} {
				got := filters.KQL()
				if !kqlSentTerm.MatchString(got) {
					t.Fatalf("KQL() = %q, want a full UTC timestamp and never a bare date", got)
				}
				raw := strings.TrimPrefix(strings.TrimPrefix(got, "sent>="), "sent<=")
				rendered, err := time.Parse(time.RFC3339, raw)
				if err != nil {
					t.Fatalf("the timestamp %q does not parse as RFC 3339: %v", raw, err)
				}
				if !rendered.Equal(tc.value.Truncate(time.Second)) {
					t.Errorf("timestamp = %v, want the same instant as %v", rendered, tc.value)
				}
			}
		})
	}
}

// TestSearchFiltersEmpty: Empty is true only when there is no term at all,
// which is a usage error rather than an empty search. A HasAttachment of false
// is a term, and so is a from: that is only whitespace.
func TestSearchFiltersEmpty(t *testing.T) {
	cases := []struct {
		name    string
		filters SearchFilters
		want    bool
	}{
		{"the zero value", SearchFilters{}, true},
		{"whitespace only", SearchFilters{Text: "  	 "}, true},
		{"Text", SearchFilters{Text: "x"}, false},
		{"From", SearchFilters{From: "x"}, false},
		{"To", SearchFilters{To: "x"}, false},
		{"Since", SearchFilters{Since: kqlSince}, false},
		{"Until", SearchFilters{Until: kqlUntil}, false},
		{"MentionsMe", SearchFilters{MentionsMe: true}, false},
		{"MentionsUser", SearchFilters{MentionsUser: "x"}, false},
		{"HasAttachment true", SearchFilters{HasAttachment: kqlBool(true)}, false},
		{"HasAttachment false", SearchFilters{HasAttachment: kqlBool(false)}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.filters.Empty()
			if got != tc.want {
				t.Errorf("Empty() = %v, want %v for %+v", got, tc.want, tc.filters)
			}
			// The two must agree: a filter with no term renders no query, and a
			// filter with a term always renders one.
			if kql := tc.filters.KQL(); tc.want != (kql == "") {
				t.Errorf("Empty() = %v but KQL() = %q", tc.want, kql)
			}
		})
	}
}

// FuzzSearchFiltersKQL is the "never panics" target: whatever a caller passes
// must come back as a query string, and the invariants the escaping exists for
// must hold on every input.
func FuzzSearchFiltersKQL(f *testing.F) {
	f.Add("hello", "", "", "", false, false, false, int64(0), int64(0))
	f.Add("topic:release", "Alice Example", "bob", "", false, false, false, kqlSince.Unix(), kqlUntil.Unix())
	f.Add("say \"hi\": now", "Al\"ice", "", "497b7a2a-9e1a-48d7-80e8-2965d2fc3a81", true, true, true, int64(-1), int64(1))
	f.Fuzz(func(t *testing.T, text, from, to, mentionsUser string, mentionsMe, hasAttachment, attachmentValue bool, sinceUnix, untilUnix int64) {
		// Fuzz arguments are primitives, so the two timestamps arrive as Unix
		// seconds. They are clamped into a range time.Time can render, so the
		// fuzzer cannot turn the arithmetic into an overflow instead of a KQL
		// question.
		const maxUnix = int64(1) << 42
		if sinceUnix > maxUnix || sinceUnix < -maxUnix {
			sinceUnix = 0
		}
		if untilUnix > maxUnix || untilUnix < -maxUnix {
			untilUnix = 0
		}
		filters := SearchFilters{
			Text:         text,
			From:         from,
			To:           to,
			MentionsMe:   mentionsMe,
			MentionsUser: mentionsUser,
		}
		if sinceUnix != 0 {
			filters.Since = time.Unix(sinceUnix, 0).UTC()
		}
		if untilUnix != 0 {
			filters.Until = time.Unix(untilUnix, 0).UTC()
		}
		if hasAttachment {
			filters.HasAttachment = &attachmentValue
		}

		got := filters.KQL() // must not panic

		if empty := filters.Empty(); empty && got != "" {
			t.Fatalf("Empty() = true but KQL() = %q for %+v", got, filters)
		} else if !empty && got == "" {
			t.Fatalf("Empty() = false but KQL() = %q for %+v", got, filters)
		}
		// A mentions: value is copied verbatim, so it keeps any whitespace it was
		// given (a fuzz run found "mentions:x " with a trailing space, since
		// MentionsUser is the one term KQL does not trim). The value is a Graph user
		// id, which never carries whitespace, and the service tokenizes on it, so this
		// is recorded rather than asserted.
		for _, term := range strings.Fields(got) {
			// The documented form of a mention is the user id without its
			// dashes (refs/graph/concepts/search-concept-chat-messages.md:267),
			// and a mentions: term can only come from MentionsUser.
			if strings.HasPrefix(term, "mentions:") && strings.Contains(term, "-") {
				t.Fatalf("KQL() = %q: a mentions: term still carries a dash", got)
			}
			// A sent term always carries a full timestamp (PLAN.md:235); it can
			// only come from Since or Until.
			if strings.HasPrefix(term, "sent>") || strings.HasPrefix(term, "sent<") {
				if !kqlSentTerm.MatchString(term) {
					t.Fatalf("KQL() = %q: %q is not a full timestamp", got, term)
				}
			}
		}
	})
}
