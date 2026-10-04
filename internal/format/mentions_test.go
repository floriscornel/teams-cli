package format

import (
	"errors"
	"strings"
	"testing"
)

// The fixtures are the MCP's own mention tests
// (refs/teams-mcp/src/utils/__tests__/users.test.ts:300-355) plus the cases
// PLAN.md:178 asks this port to fix: one <at> per user, a documented
// mentioned.user shape, and no mention entry without a matching tag.

func TestApplyMentionsCanonicalFixture(t *testing.T) {
	body, mentions, err := ApplyMentions(`<p>Hello @john.doe, how are you?</p>`, []MentionTarget{{
		UserID:      "1",
		DisplayName: "John Doe",
		Handles:     []string{"john.doe"},
	}})
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	want := `<p>Hello <at id="0">John Doe</at>, how are you?</p>`
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if len(mentions) != 1 {
		t.Fatalf("mentions = %+v, want one entry", mentions)
	}
	got := mentions[0]
	if got.ID != 0 || got.UserID != "1" || got.DisplayName != "John Doe" || got.Matched != "@john.doe" {
		t.Errorf("mention = %+v, want the id-0 entry for John Doe", got)
	}
}

func TestApplyMentionsQuotedForm(t *testing.T) {
	// The MCP accepts @"Full Name" as well as @token (users.ts:150-153). The
	// handle list is tried in order, so the e-mail form is looked for first and
	// the display name is what actually occurs.
	body, mentions, err := ApplyMentions(`<p>Hello @"John Doe"!</p>`, []MentionTarget{{
		UserID:      "1",
		DisplayName: "John Doe",
		Handles:     []string{"john.doe", "John Doe"},
	}})
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	if want := `<p>Hello <at id="0">John Doe</at>!</p>`; body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if len(mentions) != 1 {
		t.Fatalf("mentions = %+v, want one entry", mentions)
	}
}

func TestApplyMentionsTwoPeople(t *testing.T) {
	body, mentions, err := ApplyMentions(`<p>Hello @john.doe and @jane.smith!</p>`, []MentionTarget{
		{UserID: "1", DisplayName: "John Doe", Handles: []string{"john.doe"}},
		{UserID: "2", DisplayName: "Jane Smith", Handles: []string{"jane.smith"}},
	})
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	want := `<p>Hello <at id="0">John Doe</at> and <at id="1">Jane Smith</at>!</p>`
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if len(mentions) != 2 || mentions[0].ID != 0 || mentions[1].ID != 1 {
		t.Errorf("mentions = %+v, want ids 0 and 1 in request order", mentions)
	}
}

func TestApplyMentionsOneAtPerUser(t *testing.T) {
	// The MCP would emit an entry per mapping; PLAN.md:178 wants one per user.
	// Every occurrence of the person's text gets the same id, and the duplicate
	// target contributes nothing more than a second handle to look for.
	body, mentions, err := ApplyMentions(`<p>@alice and @alice again</p>`, []MentionTarget{
		{UserID: "user-2", DisplayName: "Alice Example", Handles: []string{"alice"}},
		{UserID: "user-2", DisplayName: "Alice Example", Handles: []string{"a.example"}},
	})
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	want := `<p><at id="0">Alice Example</at> and <at id="0">Alice Example</at> again</p>`
	if body != want {
		t.Errorf("body = %q, want %q", body, want)
	}
	if len(mentions) != 1 {
		t.Fatalf("mentions = %+v, want a single entry for the user", mentions)
	}
}

func TestApplyMentionsEscapesTheDisplayName(t *testing.T) {
	body, _, err := ApplyMentions(`<p>hi @a</p>`, []MentionTarget{{
		UserID:      "user-2",
		DisplayName: "A & B <C>",
		Handles:     []string{"a"},
	}})
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	if want := `<at id="0">A &amp; B &lt;C&gt;</at>`; !strings.Contains(body, want) {
		t.Errorf("body = %q, want it to contain %q", body, want)
	}
}

func TestApplyMentionsLeavesAttributesAlone(t *testing.T) {
	// The DOM-based replacement is what keeps a mailto attribute intact: only
	// text nodes are rewritten, so the address in href is not a mention.
	body, _, err := ApplyMentions(`<p><a href="mailto:alice@example.com">mail</a> hi @alice</p>`, []MentionTarget{{
		UserID:      "user-2",
		DisplayName: "Alice Example",
		Handles:     []string{"alice"},
	}})
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	if !strings.Contains(body, `href="mailto:alice@example.com"`) {
		t.Errorf("body = %q, want the href left alone", body)
	}
	if !strings.Contains(body, `<at id="0">Alice Example</at>`) {
		t.Errorf("body = %q, want the mention inserted", body)
	}
}

func TestApplyMentionsMatchesWholeTokensOnly(t *testing.T) {
	target := MentionTarget{UserID: "user-2", DisplayName: "Alice Example", Handles: []string{"alice"}}
	tests := []struct {
		name string
		body string
	}{
		{"inside another token", `<p>@alicebob says hi</p>`},
		{"inside an address", `<p>bob@alice.example says hi</p>`},
		{"inside a longer name", `<p>@alice.smith says hi</p>`},
		{"not there at all", `<p>nothing to see</p>`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ApplyMentions(tc.body, []MentionTarget{target})
			var unmatched *UnmatchedMentionError
			if !errors.As(err, &unmatched) {
				t.Fatalf("ApplyMentions(%q) error = %v, want an UnmatchedMentionError", tc.body, err)
			}
			if !strings.Contains(unmatched.Error(), "@alice") {
				t.Errorf("error %q should name the spelling that was looked for", unmatched)
			}
		})
	}
}

func TestApplyMentionsCaseInsensitive(t *testing.T) {
	body, _, err := ApplyMentions(`<p>hi @ALICE</p>`, []MentionTarget{{
		UserID: "user-2", DisplayName: "Alice Example", Handles: []string{"alice"},
	}})
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	if want := `<at id="0">Alice Example</at>`; !strings.Contains(body, want) {
		t.Errorf("body = %q, want it to contain %q", body, want)
	}
}

func TestApplyMentionsNoTargets(t *testing.T) {
	body, mentions, err := ApplyMentions(`<p>plain</p>`, nil)
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	if body != `<p>plain</p>` || len(mentions) != 0 {
		t.Errorf("ApplyMentions = (%q, %+v), want the body unchanged and no mentions", body, mentions)
	}
}

func TestApplyMentionsRejectsATargetWithoutAnID(t *testing.T) {
	if _, _, err := ApplyMentions(`<p>@alice</p>`, []MentionTarget{{DisplayName: "Alice", Handles: []string{"alice"}}}); err == nil {
		t.Fatal("ApplyMentions accepted a mention target with no user id")
	}
}

func TestMatchHandle(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		handle string
		want   []string
	}{
		{"bare", "hello @alice, ok", "alice", []string{"@alice"}},
		{"quoted", `hello @"Alice Example" ok`, "Alice Example", []string{`@"Alice Example"`}},
		{"twice", "@alice and @alice", "alice", []string{"@alice", "@alice"}},
		{"case", "@ALICE", "alice", []string{"@ALICE"}},
		{"quoted is not bare", `@"Alice Example"`, "Alice", nil},
		{"word boundary after", "@alicebob", "alice", nil},
		{"word boundary before", "bob@alice", "alice", nil},
		{"email form", "mail bob@alice.example", "alice", nil},
		{"start of line", "@alice", "alice", []string{"@alice"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			locs := matchHandle(tc.text, tc.handle)
			got := make([]string, 0, len(locs))
			for _, loc := range locs {
				got = append(got, tc.text[loc[0]:loc[1]])
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Errorf("matchHandle(%q, %q) = %v, want %v", tc.text, tc.handle, got, tc.want)
			}
		})
	}
}
