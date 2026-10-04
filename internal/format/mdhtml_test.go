package format

import (
	"strings"
	"testing"
)

// These tests are the Phase 4 half of the format layer: markdown and
// user-supplied HTML become the sanitized body a Teams message carries. The
// fixtures come from the MCP's own tests, which is the only place the exact
// allow-list and the mention payload are pinned
// (refs/teams-mcp/src/utils/__tests__/markdown.test.ts,
// refs/teams-mcp/src/utils/__tests__/users.test.ts:300-355).

func TestMarkdownToHTMLFormats(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		want     []string
	}{
		{
			name:     "bold and italic",
			markdown: "**Bold** and _italic_ text",
			want:     []string{"<strong>Bold</strong>", "<em>italic</em>"},
		},
		{
			// The MCP asserts the exact anchor with no rel and no target
			// (markdown.test.ts:12-16); bluemonday's default would add both.
			name:     "link keeps the author's attributes",
			markdown: "[Google](https://google.com)",
			want:     []string{`<a href="https://google.com">Google</a>`},
		},
		{
			// breaks:true in the MCP's marked options.
			name:     "a single newline is a line break",
			markdown: "one\ntwo",
			want:     []string{"one<br>", "two"},
		},
		{
			name:     "heading",
			markdown: "# Heading",
			want:     []string{"<h1>Heading</h1>"},
		},
		{
			name:     "gfm table and strikethrough",
			markdown: "| a | b |\n|---|---|\n| 1 | 2 |\n\n~~gone~~",
			want:     []string{"<table>", "<th>a</th>", "<td>1</td>", "<del>gone</del>"},
		},
		{
			name:     "code block",
			markdown: "```\nx = 1\n```",
			want:     []string{"<pre>", "<code>"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MarkdownToHTML(tc.markdown)
			if err != nil {
				t.Fatalf("MarkdownToHTML(%q): %v", tc.markdown, err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("MarkdownToHTML(%q) = %q, want it to contain %q", tc.markdown, got, want)
				}
			}
		})
	}
}

func TestMarkdownToHTMLEmpty(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n"} {
		got, err := MarkdownToHTML(in)
		if err != nil {
			t.Fatalf("MarkdownToHTML(%q): %v", in, err)
		}
		if got != "" {
			t.Errorf("MarkdownToHTML(%q) = %q, want an empty body", in, got)
		}
	}
}

func TestMarkdownToHTMLSanitisesRawHTML(t *testing.T) {
	// A markdown body can carry raw HTML, which marked passes through and the
	// sanitizer is what makes safe. The script's *content* must go too, which is
	// what the MCP's own assertion checks (markdown.test.ts:53-59).
	got, err := MarkdownToHTML("<script>alert(\"xss\")</script>\n\n**fine**")
	if err != nil {
		t.Fatalf("MarkdownToHTML: %v", err)
	}
	if strings.Contains(got, "script") || strings.Contains(strings.ToLower(got), "alert") {
		t.Errorf("MarkdownToHTML kept the script content: %q", got)
	}
	if !strings.Contains(got, "<strong>fine</strong>") {
		t.Errorf("MarkdownToHTML dropped the surrounding markup: %q", got)
	}
}

func TestSanitizeHTMLAllowList(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "allowed markup is untouched",
			in:   "<p><strong>Bold</strong> and <em>italic</em></p>",
			want: "<p><strong>Bold</strong> and <em>italic</em></p>",
		},
		{
			// target is in the allow-list and DOMPurify injects no rel
			// (markdown.test.ts:88-92).
			name: "target survives",
			in:   `<a href="https://example.com" target="_blank">Link</a>`,
			want: `<a href="https://example.com" target="_blank">Link</a>`,
		},
		{
			name: "event handlers and styles are stripped",
			in:   `<p onclick="x()" style="color:red">Text</p>`,
			want: "<p>Text</p>",
		},
		{
			name: "image attributes survive",
			in:   `<img src="https://example.com/image.jpg" alt="Image" width="100">`,
			want: `<img src="https://example.com/image.jpg" alt="Image" width="100">`,
		},
		{
			// Relative URLs are how a Teams body references hosted content.
			name: "a relative src survives",
			in:   `<img src="../hostedContents/1/$value" alt="chart">`,
			want: `<img src="../hostedContents/1/$value" alt="chart">`,
		},
		{
			// Deliberately narrower than DOMPurify, which keeps the element and
			// drops only the attribute; see the divergence note in mdhtml.go.
			name: "a javascript URL is dropped with its element",
			in:   `<a href="javascript:alert(1)">click</a>`,
			want: `click`,
		},
		{
			name: "a data URL in an image is dropped with its element",
			in:   `<img src="data:image/png;base64,AAAA">`,
			want: ``,
		},
		{
			name: "an unknown element loses its tag and keeps its text",
			in:   "<p>before <blink>blink</blink> after</p>",
			want: "<p>before blink after</p>",
		},
		{
			// Pinned deliberately: this is why ApplyMentions runs afterwards.
			name: "a mention tag is dropped by the sanitizer",
			in:   `<p>Hello <at id="0">Jane Smith</at></p>`,
			want: "<p>Hello Jane Smith</p>",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeHTML(tc.in); got != tc.want {
				t.Errorf("SanitizeHTML(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEscapeText(t *testing.T) {
	got := EscapeText(`& <div> "x" 'y'`)
	want := "&amp; &lt;div&gt; &quot;x&quot; &#39;y&#39;"
	if got != want {
		t.Errorf("EscapeText = %q, want %q", got, want)
	}
}

func TestMarkdownToHTMLThenMentionsKeepsTheTags(t *testing.T) {
	// The golden test PLAN.md:179 asks for: build a body with mentions, run it
	// through the whole format path, and assert the tags survive. Sanitizing the
	// assembled body would strip them (see the allow-list test above), so the
	// order in this test is the contract.
	html, err := MarkdownToHTML("hello @alice, please review")
	if err != nil {
		t.Fatalf("MarkdownToHTML: %v", err)
	}
	body, mentions, err := ApplyMentions(html, []MentionTarget{{
		UserID:      "user-2",
		DisplayName: "Alice Example",
		Handles:     []string{"alice"},
	}})
	if err != nil {
		t.Fatalf("ApplyMentions: %v", err)
	}
	if !strings.Contains(body, `<at id="0">Alice Example</at>`) {
		t.Fatalf("the mention tag did not survive: %q", body)
	}
	if len(mentions) != 1 || mentions[0].UserID != "user-2" {
		t.Fatalf("mentions = %+v, want one entry for user-2", mentions)
	}
}
