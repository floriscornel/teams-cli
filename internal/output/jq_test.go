package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Tests for the --jq filter and the markdown renderer (PLAN.md's output
// conventions: --jq filters the JSON, and a message body is markdown on a
// terminal and plain text when piped).

func TestApplyJQ(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"messages": []map[string]string{{"body": "hi"}, {"body": "there"}},
		"total":    2,
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		expr string
		want string
	}{
		{"field", ".total", "2\n"},
		{"nested field", ".messages[0].body", "hi\n"},
		{"array iteration", ".messages[].body", "hi\nthere\n"},
		{"a string stays unquoted", ".messages[1].body", "there\n"},
		{"a number result", ".messages | length", "2\n"},
		{"an object result is JSON", "{n: .total}", "{\"n\":2}\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := ApplyJQ(&buf, tc.expr, data); err != nil {
				t.Fatalf("ApplyJQ(%q): %v", tc.expr, err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("ApplyJQ(%q) = %q, want %q", tc.expr, got, tc.want)
			}
		})
	}
}

func TestApplyJQErrors(t *testing.T) {
	t.Run("an invalid expression is a usage error", func(t *testing.T) {
		var buf bytes.Buffer
		err := ApplyJQ(&buf, ".[", []byte("{}"))
		var coded *Error
		if !errors.As(err, &coded) || coded.Code != CodeUsage {
			t.Fatalf("err = %v, want a usage error", err)
		}
	})
	t.Run("non-JSON input is an error", func(t *testing.T) {
		var buf bytes.Buffer
		if err := ApplyJQ(&buf, ".", []byte("not json")); err == nil {
			t.Fatal("want an error for a non-JSON document")
		}
	})
	t.Run("empty input is not an error", func(t *testing.T) {
		var buf bytes.Buffer
		if err := ApplyJQ(&buf, ".", nil); err != nil {
			t.Fatalf("ApplyJQ on empty input: %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("output = %q, want empty", buf.String())
		}
	})
}

func TestPrinterJSONWithJQ(t *testing.T) {
	var buf bytes.Buffer
	p := New(Options{Out: &buf, JSON: true, JQ: ".items[].id"})
	if err := p.JSON(map[string]any{"items": []map[string]string{{"id": "a"}, {"id": "b"}}}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if got, want := buf.String(), "a\nb\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if !p.JSONMode() {
		t.Error("JSONMode() = false with --jq set; --jq implies --json")
	}
}

func TestPrinterJSONWithoutJQ(t *testing.T) {
	var buf bytes.Buffer
	p := New(Options{Out: &buf, JSON: true})
	if err := p.JSON(map[string]string{"id": "a"}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	want := "{\n  \"id\": \"a\"\n}\n"
	if got := buf.String(); got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
	if got := p.JQ(); got != "" {
		t.Errorf("JQ() = %q, want empty", got)
	}
}

func TestMarkdownPiped(t *testing.T) {
	var buf bytes.Buffer
	p := New(Options{Out: &buf})
	p.Markdown("first\n\nsecond", "  ")
	want := "  first\n\n  second\n"
	if got := buf.String(); got != want {
		t.Errorf("Markdown = %q, want %q", got, want)
	}
}

func TestMarkdownEmpty(t *testing.T) {
	var buf bytes.Buffer
	p := New(Options{Out: &buf})
	p.Markdown("   \n\t\n", "  ")
	if buf.Len() != 0 {
		t.Errorf("Markdown of a blank body wrote %q", buf.String())
	}
}

func TestMarkdownStyledOnForcedColor(t *testing.T) {
	// CLICOLOR_FORCE keeps colour on a pipe, which is how the styled branch is
	// reachable without a pty.
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	var buf bytes.Buffer
	p := New(Options{Out: &buf, Err: &buf})
	if !p.Color() {
		t.Fatal("CLICOLOR_FORCE did not enable colour")
	}
	p.Markdown("# Title\n\nbody text", "")
	out := buf.String()
	if !strings.Contains(out, "Title") || !strings.Contains(stripANSI(out), "body text") {
		t.Errorf("styled markdown lost its text: %q", out)
	}
	if !strings.Contains(out, "\x1b[") {
		t.Errorf("styled markdown carries no styling: %q", out)
	}
}

func TestPrinterStylesAreNoOpsWithoutColor(t *testing.T) {
	var buf bytes.Buffer
	p := New(Options{Out: &buf, Err: &buf, NoColor: true})
	if got := p.Bold("x"); got != "x" {
		t.Errorf("Bold = %q, want x", got)
	}
	if got := p.Dim("x"); got != "x" {
		t.Errorf("Dim = %q, want x", got)
	}
}

func TestPrinterJSONIsValid(t *testing.T) {
	var buf bytes.Buffer
	p := New(Options{Out: &buf, JSON: true})
	if err := p.JSON(map[string]any{"n": 1, "s": "<b>"}); err != nil {
		t.Fatalf("JSON: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, buf.String())
	}
	if strings.Contains(buf.String(), "\\u003c") {
		t.Error("HTML in the JSON was escaped; SetEscapeHTML(false) keeps it readable")
	}
}

// stripANSI removes the styling glamour adds, so an assertion can look at the
// text a terminal would show.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && (s[i] < 'a' || s[i] > 'z') && (s[i] < 'A' || s[i] > 'Z') {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
