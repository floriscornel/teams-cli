package format

import (
	"strings"
	"sync"
	"testing"
)

// fixture is one message body and the markdown it must produce.
//
// The tables below port refs/teams-mcp/src/utils/__tests__/html-to-markdown.test.ts
// case by case and add the shapes the reference documents show. The MCP asserts
// most of these outputs with a substring match; the golden here is the exact
// string, which is stricter and catches an accidental blank line or separator.
type fixture struct {
	name  string
	body  string
	names map[int]string
	want  string
}

// fixtures is a function, not a package variable, because this package promises
// to hold no mutable state: a shared table would be one.
func fixtures() []fixture {
	auditService := "<p><at id='0'>Alice</at>&nbsp;<at id='1'>Bob</at>&nbsp;on the audit service</p>\n" +
		"<ul><li><strong>Latency</strong>: each call triggers validation</li></ul>\n" +
		"<table><thead><tr><th>Change</th><th>Verdict</th></tr></thead>\n" +
		"<tbody><tr><td>Remove auth</td><td>Approved</td></tr></tbody></table>"

	return []fixture{
		// - - - The MCP's own cases - - - //

		{name: "empty body", body: "", want: ""},
		{name: "whitespace-only body", body: "   ", want: ""},
		{name: "bold", body: "<strong>Bold</strong>", want: "**Bold**"},
		{name: "italic", body: "<em>italic</em>", want: "*italic*"},
		{name: "link", body: "<a href='https://example.com'>Link</a>", want: "[Link](https://example.com)"},
		{name: "unordered list", body: "<ul><li>Item 1</li><li>Item 2</li></ul>", want: "- Item 1\n- Item 2"},
		{name: "ordered list", body: "<ol><li>First</li><li>Second</li></ol>", want: "1. First\n2. Second"},
		{name: "headings", body: "<h1>Title</h1><h2>Subtitle</h2><h3>Section</h3>", want: "# Title\n\n## Subtitle\n\n### Section"},
		{name: "inline code", body: "<code>console.log()</code>", want: "`console.log()`"},
		{name: "code block", body: "<pre><code>const x = 1;\nconst y = 2;</code></pre>", want: "```\nconst x = 1;\nconst y = 2;\n```"},
		{name: "horizontal rule", body: "<hr>", want: "---"},
		{name: "blockquote", body: "<blockquote>Quoted text</blockquote>", want: "> Quoted text"},
		{name: "strikethrough", body: "<del>deleted</del>", want: "~~deleted~~"},
		{
			name: "table",
			body: "<table><thead><tr><th>Name</th><th>Value</th></tr></thead>" +
				"<tbody><tr><td>A</td><td>1</td></tr></tbody></table>",
			want: "| Name | Value |\n|---|---|\n| A | 1 |",
		},
		{name: "entities", body: "<p>A &amp; B &lt; C &gt; D</p>", want: "A & B < C > D"},
		{name: "named entities", body: "<p>He said &quot;hi&quot; &amp; left</p>", want: "He said \"hi\" & left"},
		{name: "paragraph", body: "<p>Hello world</p>", want: "Hello world"},
		{name: "mixed formatting", body: "<p><strong>Bold</strong> and <em>italic</em> and <code>code</code></p>", want: "**Bold** and *italic* and `code`"},
		{name: "mention", body: "<at id='0'>John Doe</at>", want: "@John Doe"},
		{name: "two mentions", body: "<p><at id='0'>Alice</at> and <at id='1'>Bob</at> are here</p>", want: "@Alice and @Bob are here"},
		{
			// The MCP's fixtures give the split tags one mentions[] entry per
			// word; here the display name is what groups them, see mergeMentionsIn.
			name:  "merged split mention",
			body:  "<p><at id='0'>Brunno</at>&nbsp;<at id='1'>Joyran</at>&nbsp;hello</p>",
			names: map[int]string{0: "Brunno Joyran", 1: "Brunno Joyran"},
			want:  "@Brunno Joyran hello",
		},
		{
			name:  "merged split mentions of two people",
			body:  "<p><at id='0'>Brunno</at>&nbsp;<at id='1'>Joyran</at>&nbsp;<at id='2'>Pereira</at>&nbsp;<at id='3'>Soares</at></p>",
			names: map[int]string{0: "Brunno Joyran", 1: "Brunno Joyran", 2: "Pereira Soares", 3: "Pereira Soares"},
			want:  "@Brunno Joyran @Pereira Soares",
		},
		{
			name:  "mentions of different people are not merged",
			body:  "<p><at id='0'>Alice</at>&nbsp;<at id='1'>Bob</at></p>",
			names: map[int]string{0: "Alice", 1: "Bob"},
			want:  "@Alice @Bob",
		},
		{
			name: "mentions without a mentions[] array stay per-tag",
			body: "<p><at id='0'>Brunno</at>&nbsp;<at id='1'>Joyran</at>&nbsp;hello</p>",
			want: "@Brunno @Joyran hello",
		},
		{
			name:  "adjacent mentions get a separator",
			body:  "<p><at id='0'>Lindeberg</at>&nbsp;<at id='1'>Pessoa</at>&nbsp;<at id='2'>Leite</at><at id='3'>Bruno</at>&nbsp;<at id='4'>Werneck</at></p>",
			names: map[int]string{0: "Lindeberg Pessoa Leite", 1: "Lindeberg Pessoa Leite", 2: "Lindeberg Pessoa Leite", 3: "Bruno Werneck", 4: "Bruno Werneck"},
			want:  "@Lindeberg Pessoa Leite @Bruno Werneck",
		},
		{name: "attachment with id", body: "<p>See the file</p><attachment id='abc123'></attachment>", want: "See the file\n\n{attachment:abc123}"},
		{name: "attachment without id", body: "<p>See the file</p><attachment></attachment>", want: "See the file\n\n{attachment}"},
		{name: "system event with no detail", body: "<systemEventMessage/>", want: ""},
		{
			name: "text that mentions the systemEventMessage tag survives",
			body: "<p>The &lt;systemEventMessage&gt; tag is used for system events</p>",
			want: "The <systemEventMessage> tag is used for system events",
		},
		{name: "image", body: "<img src='https://example.com/img.png' alt='screenshot'>", want: "![screenshot](https://example.com/img.png)"},
		{name: "realistic message", body: auditService, want: "@Alice @Bob on the audit service\n\n- **Latency**: each call triggers validation\n\n| Change | Verdict |\n|---|---|\n| Remove auth | Approved |"},

		// - - - The shapes the reference documents show - - - //

		{
			// refs/graph/api-reference/v1.0/resources/chatmessagemention.md: the
			// example body with two mentions.
			name: "chatMessageMention example body",
			body: "<div><div>Ah, <at id='0'>Megan</at>, <at id='1'>Alex</at>, I saw them in a separate folder. Thanks!</div>\n</div>",
			want: "Ah, @Megan, @Alex, I saw them in a separate folder. Thanks!",
		},
		{
			// mentions[] names a tag whose body text is empty.
			name:  "mention named by mentions[]",
			body:  "<p><at id='1'></at> ping</p>",
			names: map[int]string{1: "Alice Johnson"},
			want:  "@Alice Johnson ping",
		},
		{
			// mentions[] also wins over the body's own text.
			name:  "mentions[] wins over the tag text",
			body:  "<at id='1'>Alice</at>",
			names: map[int]string{1: "Alice Johnson", 7: "Someone Else"},
			want:  "@Alice Johnson",
		},
		{
			// Inline images arrive as hosted content, which Phase 3 cannot
			// download (PLAN.md line 232).
			name: "inline image",
			body: "<p>look</p><img src='../hostedContents/1/$value'>",
			want: "look\n\n![inline image 1](../hostedContents/1/$value)",
		},
		{
			name: "inline image with a Graph URL",
			body: "<img src='https://graph.microsoft.com/v1.0/chats/19:x/messages/1/hostedContents/2/$value'>",
			want: "![inline image 2](https://graph.microsoft.com/v1.0/chats/19:x/messages/1/hostedContents/2/$value)",
		},
		{
			name: "inline image keeps the body's alt text",
			body: "<img src='../hostedContents/3/$value' alt='build status'>",
			want: "![inline image 3: build status](../hostedContents/3/$value)",
		},
		{name: "ordinary image is left alone", body: "<img src='https://example.com/a.png' alt='x'>", want: "![x](https://example.com/a.png)"},
		{name: "image without a src", body: "<p>a</p><img alt='x'>", want: "a"},
		{name: "system event with detail text", body: "<systemEventMessage><p>Alice added Bob</p></systemEventMessage>", want: "Alice added Bob"},
		{name: "text after a system event", body: "<systemEventMessage/>Alice added Bob to the chat", want: "Alice added Bob to the chat"},
		{name: "bare systemEventMessage token", body: "systemEventMessage/", want: ""},
		{
			name: "self-closing attachment keeps the text behind it",
			body: "<p>before</p><attachment id='x'/>after text",
			want: "before\n\n{attachment:x} after text",
		},
		{
			name: "table without a header row",
			body: "<table><tr><td>Name</td><td>Value</td></tr><tr><td>A</td><td>1</td></tr></table>",
			want: "| Name | Value |\n|---|---|\n| A | 1 |",
		},
		{
			name: "table cell with a newline",
			body: "<table><tr><td>Name\nline</td><td>Value</td></tr><tr><td>A</td><td>1</td></tr></table>",
			want: "| Name line | Value |\n|---|---|\n| A | 1 |",
		},
		{name: "line break and rule", body: "line one<br>line two<hr><div>after</div>", want: "line one  \nline two\n\n---\n\nafter"},
		{name: "nested divs and spans", body: "<div><div><span><p>Deploy done</p></span><div><at id='0'>Alice</at> please review</div></div></div>", want: "Deploy done\n\n@Alice please review"},
		{name: "nested blocks keep their separation", body: "<div>outer<div>inner</div>tail</div><p>para</p>", want: "outer\n\ninner\n\ntail\n\npara"},
		{name: "mention inside emphasis", body: "<p><strong><at id='1'>Alice</at></strong></p>", names: map[int]string{1: "Alice"}, want: "**@Alice**"},
		{
			name:  "mentions separated by markup are not merged",
			body:  "<p><at id='1'>Alice</at><span> and </span><at id='2'>Alice</at></p>",
			names: map[int]string{1: "Alice", 2: "Alice"},
			want:  "@Alice and @Alice",
		},
		{name: "non-numeric mention id", body: "<at id='x'>Alice</at> <at>Bob</at> <AT ID='2'>Carol</AT>", want: "@Alice @Bob @Carol"},

		// - - - Malformed and hostile input - - - //

		{name: "unclosed tags", body: "<p>unclosed <b>bold <at id='0'>Alice</at>", want: "unclosed **bold @Alice**"},
		{name: "script and event handler", body: "<script>alert(1)</script><p onclick='x'>hi</p><!-- c -->", want: "hi"},
		{name: "escaped Teams tag stays text", body: "<p>&lt;at id='0'&gt;Alice&lt;/at&gt;</p>", want: "&lt;at id='0'>Alice&lt;/at>"},
		{name: "non-breaking space only", body: "\u00a0", want: ""},
	}
}

func TestHTMLToMarkdown(t *testing.T) {
	for _, fx := range fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			got, err := HTMLToMarkdownWithMentions(fx.body, fx.names)
			if err != nil {
				t.Fatalf("HTMLToMarkdownWithMentions(%q) returned an error: %v", fx.body, err)
			}
			if got != fx.want {
				t.Errorf("HTMLToMarkdownWithMentions(%q)\n got: %q\nwant: %q", fx.body, got, fx.want)
			}
		})
	}
}

// TestHTMLToMarkdownHTMLOnly checks the convenience wrapper: without a
// mentions[] array an <at> tag is named from its own text.
func TestHTMLToMarkdownHTMLOnly(t *testing.T) {
	got, err := HTMLToMarkdown("<p><at id='0'>Alice</at> and <at id='1'>Bob</at></p>")
	if err != nil {
		t.Fatalf("HTMLToMarkdown returned an error: %v", err)
	}
	if want := "@Alice and @Bob"; got != want {
		t.Errorf("HTMLToMarkdown = %q, want %q", got, want)
	}
}

// TestHTMLToMarkdownNeverEmitsTeamsElements is the promise other packages rely
// on when they put the result in a table or a terminal: no body, however
// malformed, leaves the start of a Teams element in the markdown, so nothing in
// the output can be mistaken for <at> or <attachment>.
func TestHTMLToMarkdownNeverEmitsTeamsElements(t *testing.T) {
	hostile := []string{
		"<at id='1'>Alice</at>",
		"<attachment id='x'>",
		"<AT ID='1'>Alice</AT>",
		"<attachment id='x'/>",
		"&lt;at id='1'&gt;Alice&lt;/at&gt;",
		"&amp;lt;attachment id='x'&amp;gt;",
		"<at id='1'>Alice</at><attachment id='y'>",
		"<at",
		"text <at id='1'>unclosed",
		"<at id='1'>Alice</at><attachment",
	}

	for _, body := range hostile {
		got, err := HTMLToMarkdownWithMentions(body, map[int]string{1: "Alice"})
		if err != nil {
			t.Fatalf("HTMLToMarkdownWithMentions(%q) returned an error: %v", body, err)
		}
		lower := strings.ToLower(got)
		if strings.Contains(lower, "<at") || strings.Contains(lower, "<attachment") {
			t.Errorf("HTMLToMarkdownWithMentions(%q) leaked a Teams element: %q", body, got)
		}
	}
}

// TestHTMLToMarkdownIsDeterministic covers the "must stay idempotent" half of
// PLAN.md's fuzz rule (line 483) in the sense that a body always converts to the
// same markdown: the converter is built per call and holds no state, so a repeat
// cannot drift.
//
// Markdown that is fed back in as HTML is a different question: it is text at
// that point, so the converter escapes the characters that are markdown to it
// ("#" becomes "\#"). That is escaping, not drift, which is why this test
// converts the same body twice rather than converting the output again;
// TestHTMLToMarkdownPlainTextIsAFixpoint covers the outputs that carry no
// markdown at all.
func TestHTMLToMarkdownIsDeterministic(t *testing.T) {
	for _, fx := range fixtures() {
		first, err := HTMLToMarkdownWithMentions(fx.body, fx.names)
		if err != nil {
			t.Fatalf("HTMLToMarkdownWithMentions(%q) returned an error: %v", fx.body, err)
		}
		second, err := HTMLToMarkdownWithMentions(fx.body, fx.names)
		if err != nil {
			t.Fatalf("HTMLToMarkdownWithMentions(%q) returned an error: %v", fx.body, err)
		}
		if first != second {
			t.Errorf("HTMLToMarkdownWithMentions(%q) is not deterministic: %q then %q", fx.body, first, second)
		}
	}
}

// TestHTMLToMarkdownIsConcurrencySafe runs the whole fixture table from several
// goroutines, which fails under -race if a conversion ever shares state with
// another one.
func TestHTMLToMarkdownIsConcurrencySafe(t *testing.T) {
	table := fixtures()
	for i := range table {
		want, err := HTMLToMarkdownWithMentions(table[i].body, table[i].names)
		if err != nil {
			t.Fatalf("HTMLToMarkdownWithMentions(%q) returned an error: %v", table[i].body, err)
		}
		table[i].want = want
	}

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, fx := range table {
				got, err := HTMLToMarkdownWithMentions(fx.body, fx.names)
				if err != nil || got != fx.want {
					t.Errorf("concurrent conversion of %q = %q (%v), want %q", fx.body, got, err, fx.want)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// TestHTMLToMarkdownPlainTextIsAFixpoint pins the cases where the output carries
// no markdown at all: reading such a body again in HTML mode cannot change it,
// which is what makes the round trip through a reader harmless.
func TestHTMLToMarkdownPlainTextIsAFixpoint(t *testing.T) {
	for _, body := range []string{
		"<p>Hello world</p>",
		"<at id='0'>John Doe</at>",
		"<p><at id='0'>Alice</at> and <at id='1'>Bob</at> are here</p>",
	} {
		first, err := HTMLToMarkdown(body)
		if err != nil {
			t.Fatalf("HTMLToMarkdown(%q) returned an error: %v", body, err)
		}
		second, err := HTMLToMarkdown(first)
		if err != nil {
			t.Fatalf("HTMLToMarkdown(%q) returned an error: %v", first, err)
		}
		if first != second {
			t.Errorf("plain-text output is not a fixpoint: %q then %q", first, second)
		}
	}
}

// TestHTMLToMarkdownEmptyBody documents the empty and whitespace-only cases,
// including a body that is only a non-breaking space.
func TestHTMLToMarkdownEmptyBody(t *testing.T) {
	for _, body := range []string{"", " ", "\t\n ", "\u00a0", "<p></p>", "<div> </div>", "<!-- nothing -->"} {
		got, err := HTMLToMarkdown(body)
		if err != nil {
			t.Fatalf("HTMLToMarkdown(%q) returned an error: %v", body, err)
		}
		if got != "" {
			t.Errorf("HTMLToMarkdown(%q) = %q, want \"\"", body, got)
		}
	}
}

func TestBodyToMarkdown(t *testing.T) {
	names := map[int]string{1: "Alice Johnson"}

	cases := []struct {
		name        string
		content     string
		contentType string
		want        string
	}{
		{name: "html body", content: "<p><at id='1'></at> shipped</p>", contentType: "html", want: "@Alice Johnson shipped"},
		{name: "html body, mixed case type", content: "<p><strong>Bold</strong></p>", contentType: "HTML", want: "**Bold**"},
		{name: "unknown type falls back to html", content: "<p><strong>Bold</strong></p>", contentType: "application/xhtml", want: "**Bold**"},
		{name: "empty type falls back to html", content: "<p>Hi</p>", contentType: "", want: "Hi"},
		{name: "text body is not markup", content: "A &amp; B < C\n", contentType: "text", want: "A &amp; B < C"},
		{name: "text body, mixed case type", content: "line one\r\nline two\r", contentType: "Text", want: "line one\nline two"},
		{name: "text body keeps a raw Teams tag out of the output", content: "<at id='1'>Alice</at>", contentType: "text", want: "&lt;at id='1'>Alice&lt;/at>"},
		{name: "empty text body", content: "", contentType: "text", want: ""},
		{name: "whitespace-only text body", content: "  \n ", contentType: "text", want: ""},
		{name: "empty html body", content: "   ", contentType: "html", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BodyToMarkdown(tc.content, tc.contentType, names)
			if err != nil {
				t.Fatalf("BodyToMarkdown(%q, %q) returned an error: %v", tc.content, tc.contentType, err)
			}
			if got != tc.want {
				t.Errorf("BodyToMarkdown(%q, %q)\n got: %q\nwant: %q", tc.content, tc.contentType, got, tc.want)
			}
		})
	}
}

func TestMentionNames(t *testing.T) {
	cases := []struct {
		name     string
		mentions []Mention
		want     map[int]string
	}{
		{name: "no mentions", mentions: nil, want: nil},
		{name: "empty slice", mentions: []Mention{}, want: nil},
		{
			name:     "indexes by id",
			mentions: []Mention{{ID: 0, DisplayName: "Alice"}, {ID: 3, DisplayName: "Bob Smith"}},
			want:     map[int]string{0: "Alice", 3: "Bob Smith"},
		},
		{
			name:     "trims and skips empty names",
			mentions: []Mention{{ID: 0, DisplayName: "  Alice  "}, {ID: 1, DisplayName: "   "}, {ID: 2, DisplayName: ""}},
			want:     map[int]string{0: "Alice"},
		},
		{name: "only empty names", mentions: []Mention{{ID: 0, DisplayName: " "}}, want: nil},
		{
			name:     "a later entry with the same id wins",
			mentions: []Mention{{ID: 1, DisplayName: "First"}, {ID: 1, DisplayName: "Second"}},
			want:     map[int]string{1: "Second"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MentionNames(tc.mentions)
			if len(got) != len(tc.want) {
				t.Fatalf("MentionNames(%v) = %v, want %v", tc.mentions, got, tc.want)
			}
			for id, want := range tc.want {
				if got[id] != want {
					t.Errorf("MentionNames(%v)[%d] = %q, want %q", tc.mentions, id, got[id], want)
				}
			}
		})
	}
}

// TestHTMLToMarkdownHostileInputDoesNotPanic is the table-driven half of the
// fuzz rule: a body can be anything Graph stored, including something a user
// pasted, and none of it may take the process down.
func TestHTMLToMarkdownHostileInputDoesNotPanic(t *testing.T) {
	deep := strings.Repeat("<div>", 2000)
	hostile := []string{
		"<",
		">",
		"</at>",
		"<at",
		"<at id=",
		"<at id=>x</at>",
		"<attachment id=></attachment>",
		"<at id='0'><at id='0'><at id='0'>Alice</at></at></at>",
		"<attachment id='" + strings.Repeat("a", 5000) + "'>",
		"<systemEventMessage" + strings.Repeat("<", 100),
		strings.Repeat("<at id='0'>Alice</at>", 500),
		strings.Repeat("&lt;at&gt;", 500),
		deep + "Alice" + strings.Repeat("</div>", 2000),
		"\x00\x01\xff",
		"<img src='" + strings.Repeat("x", 4096) + "'>",
		"<table>" + strings.Repeat("<tr><td>x</td></tr>", 500) + "</table>",
	}

	for _, body := range hostile {
		got, err := HTMLToMarkdownWithMentions(body, map[int]string{0: "Alice"})
		if err != nil {
			t.Fatalf("HTMLToMarkdownWithMentions returned an error for a hostile body: %v", err)
		}
		if again, _ := HTMLToMarkdownWithMentions(body, map[int]string{0: "Alice"}); again != got {
			t.Errorf("hostile body is not deterministic: %q then %q", got, again)
		}
		lower := strings.ToLower(got)
		if strings.Contains(lower, "<at") || strings.Contains(lower, "<attachment") {
			t.Errorf("hostile body leaked a Teams element: %q", got)
		}
	}
}

// FuzzHTMLToMarkdown is the Go-native fuzzing entry point PLAN.md line 483 asks
// for. Its properties are the ones a reader of this package depends on:
//
//   - no input panics, however malformed ("must never panic");
//   - the same input always converts to the same markdown ("must stay
//     idempotent");
//   - an empty or whitespace-only body is "" and no error;
//   - the output never contains "<at" or "<attachment".
func FuzzHTMLToMarkdown(f *testing.F) {
	for _, fx := range fixtures() {
		f.Add(fx.body)
	}
	f.Add("&lt;at id='0'&gt;Alice&lt;/at&gt;")
	f.Add("<attachment id='x'/>")
	f.Add("<AT ID='1'>Alice</AT>")

	f.Fuzz(func(t *testing.T, body string) {
		got, err := HTMLToMarkdown(body)
		if err != nil {
			t.Fatalf("HTMLToMarkdown(%q) returned an error: %v", body, err)
		}

		if strings.TrimSpace(body) == "" && got != "" {
			t.Fatalf("HTMLToMarkdown(%q) = %q, want an empty result", body, got)
		}

		again, err := HTMLToMarkdown(body)
		if err != nil {
			t.Fatalf("HTMLToMarkdown(%q) returned an error on the second call: %v", body, err)
		}
		if again != got {
			t.Fatalf("HTMLToMarkdown(%q) is not deterministic: %q then %q", body, got, again)
		}

		lower := strings.ToLower(got)
		if strings.Contains(lower, "<at") || strings.Contains(lower, "<attachment") {
			t.Fatalf("HTMLToMarkdown(%q) leaked a Teams element: %q", body, got)
		}
	})
}
