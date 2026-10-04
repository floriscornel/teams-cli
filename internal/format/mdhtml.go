package format

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// This file is the write half of the package: markdown (or user-supplied HTML)
// becomes the sanitized `html` body a Teams message carries. The read half
// lives in htmlmd.go.
//
// The pipeline is the MCP's (refs/teams-mcp/src/utils/markdown.ts:20-62), with
// its libraries swapped for the Go ones PLAN.md names:
//
//	markdown --goldmark(GFM)--> HTML --bluemonday--> sanitized HTML
//
// Two rules from PLAN.md:179 shape the API:
//
//   - sanitation happens *before* mentions are inserted. `<at>` is deliberately
//     not in the allow-list, so a body that already carries one has the tag
//     dropped (bluemonday keeps the element's text), which is exactly the
//     silent-mention-loss the MCP would suffer if it sanitized the assembled
//     body. MarkdownToHTML therefore never returns an `<at>`; ApplyMentions
//     (mentions.go) is what adds them, afterwards;
//   - the allow-list is the MCP's, because it is the list Teams is known to
//     render (refs/teams-mcp/src/utils/markdown.ts:26-58).
//
// The 30 tags and 7 attributes below are that list verbatim, and the MCP
// applies it to *both* entry points (markdownToHtml and sanitizeHtml), which is
// what SanitizeHTML does for the CLI's `--html` input.
//
// Where the MCP leaves a behaviour to the library default, this port writes the
// default down instead of inheriting one:
//
//   - ALLOWED_URI_REGEXP is DOMPurify 3.x's scheme list
//     (http, https, ftp, ftps, mailto, tel, callto, sms, cid, xmpp plus relative
//     URLs); bluemonday's own AllowStandardURLs covers only
//     http/https/mailto/tel/cid, so the rest are added explicitly;
//   - DOMPurify's FORBID_CONTENTS drops the *content* of script and style,
//     which the MCP's test pins ("<script>alert('xss')</script>" must not leave
//     the word "alert" behind, refs/teams-mcp/src/utils/__tests__/markdown.test.ts:53-59);
//     bluemonday keeps content by default, so those two are skipped explicitly;
//   - DOMPurify does not inject rel="noopener" and does not rewrite target;
//     bluemonday would, by default (RequireNoFollowOnFullyQualifiedLinks), so
//     that is turned off. The MCP test pins `target` surviving unchanged
//     (markdown.test.ts:88-92).
//
// One divergence is deliberate and pinned by the tests rather than papered
// over: when a URL attribute fails the scheme check, DOMPurify drops the
// attribute and keeps the element (`<a href="javascript:…">x</a>` becomes
// `<a>x</a>`), while bluemonday drops the element too and keeps its text. That
// is strictly narrower — a link Teams cannot follow is not worth rendering —
// and it is the only behaviour where the two sanitizers disagree on the shape
// of the output rather than on what is allowed.

// allowedTags is the MCP's ALLOWED_TAGS list
// (refs/teams-mcp/src/utils/markdown.ts:26-57). It is the list Teams renders;
// a tag outside it is dropped and its text kept.
var allowedTags = []string{
	"p", "br",
	"strong", "em", "b", "i", "u", "s", "del",
	"a",
	"ul", "ol", "li",
	"h1", "h2", "h3", "h4", "h5", "h6",
	"blockquote",
	"code", "pre",
	"hr",
	"table", "thead", "tbody", "tr", "th", "td",
	"img",
}

// allowedAttrs is the MCP's ALLOWED_ATTR list
// (refs/teams-mcp/src/utils/markdown.ts:58). DOMPurify treats it as a global
// list (an attribute may appear on any allowed element), so bluemonday's
// equivalent is .Globally() rather than a per-element list.
var allowedAttrs = []string{"href", "target", "src", "alt", "title", "width", "height"}

// forbiddenContentTags are the elements whose *content* the sanitizer drops
// along with the tag. DOMPurify's FORBID_CONTENTS default includes these two
// plus a few HTML elements that are not in the allow-list anyway; script and
// style are the ones that would otherwise leak their text into the message.
var forbiddenContentTags = []string{"script", "style"}

// allowedSchemes is DOMPurify 3.x's ALLOWED_URI_REGEXP scheme list. Relative
// URLs are allowed separately, which is what keeps the Teams-relative
// `<img src="../hostedContents/1/$value">` usable.
var allowedSchemes = []string{"http", "https", "ftp", "ftps", "mailto", "tel", "callto", "sms", "cid", "xmpp"}

// markdownConverter is the goldmark instance. It is built once and never
// mutated, which is what makes it safe to share (goldmark's own documentation
// says a Markdown value is immutable and safe for concurrent use).
//
// The options mirror the MCP's marked configuration: gfm:true is
// extension.GFM (tables, strikethrough, autolinks, task lists) and breaks:true
// is WithHardWraps, which turns a single newline into <br>
// (refs/teams-mcp/src/utils/markdown.ts:10-13).
//
// WithUnsafe lets raw HTML in the markdown reach the sanitizer instead of being
// escaped, which is what marked does by default; the sanitizer is what makes
// that safe. Everything the allow-list does not name is dropped by
// sanitizeTeamsHTML below, so this option does not widen the output.
var markdownConverter = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(gmhtml.WithHardWraps(), gmhtml.WithUnsafe()),
)

// sanitizer is the bluemonday policy built from the lists above. A Policy is
// read-only once configured, so one shared value is safe.
var sanitizer = newSanitizer()

// newSanitizer builds the allow-list policy. See the file comment for why each
// departure from bluemonday's defaults exists.
func newSanitizer() *bluemonday.Policy {
	p := bluemonday.NewPolicy()
	p.AllowElements(allowedTags...)
	p.AllowAttrs(allowedAttrs...).Globally()
	// A URL attribute must parse and must use an allowed scheme; relative URLs
	// stay allowed, because a Teams body references hosted content relatively.
	p.RequireParseableURLs(true)
	p.AllowRelativeURLs(true)
	p.AllowStandardURLs()
	p.AllowURLSchemes(allowedSchemes...)
	// DOMPurify adds no rel and no target; bluemonday's default adds
	// rel="nofollow" to fully qualified links.
	p.RequireNoFollowOnFullyQualifiedLinks(false)
	p.RequireNoFollowOnLinks(false)
	p.RequireNoReferrerOnFullyQualifiedLinks(false)
	p.RequireNoReferrerOnLinks(false)
	p.AddTargetBlankToFullyQualifiedLinks(false)
	// Script and style lose their content, not just their tag (DOMPurify's
	// FORBID_CONTENTS).
	p.SkipElementsContent(forbiddenContentTags...)
	return p
}

// MarkdownToHTML converts markdown to the sanitized HTML a Teams message body
// carries, with contentType "html".
//
// The result has no `<at>` tags: mentions are applied afterwards, to the
// sanitized body, because the sanitizer would drop them (PLAN.md:179).
//
// A markdown string that is empty or only whitespace returns "", which the
// caller turns into the documented minimum of an empty body (Graph requires the
// `body` property to be present, but not its content to be non-empty).
func MarkdownToHTML(markdown string) (string, error) {
	if strings.TrimSpace(markdown) == "" {
		return "", nil
	}
	var buf bytes.Buffer
	if err := markdownConverter.Convert([]byte(markdown), &buf); err != nil {
		return "", fmt.Errorf("format: convert markdown to HTML: %w", err)
	}
	// goldmark ends every document with a newline, which Teams would store. The
	// body is trimmed instead, so the same message always has the same bytes and
	// a --dry-run payload reads cleanly.
	return strings.TrimSpace(SanitizeHTML(buf.String())), nil
}

// SanitizeHTML applies the Teams allow-list to an HTML body.
//
// It is the path `--html` input takes, and it is also the step MarkdownToHTML
// ends with, so both spellings of a body go through exactly one policy. The
// result keeps every allowed element, drops the rest while keeping their text,
// and drops the content of script and style outright.
//
// The output is not guaranteed to be well-formed for every input (bluemonday
// sanitizes a parsed tree and re-serializes it, so in practice it is), and the
// caller that needs well-formed HTML is ApplyMentions, which parses and
// re-renders the body anyway.
func SanitizeHTML(html string) string {
	return sanitizer.Sanitize(html)
}

// EscapeText escapes plain text so it can be embedded in an HTML body, the port
// of the MCP's escapeHtml (refs/teams-mcp/src/utils/file-upload.ts:337-344).
//
// It is used for the `text` half of a message that is being posted with
// attachments: a file post always sends contentType "html", so the words the
// user typed as plain text have to be escaped into it, and `--text` plus
// `--file` is therefore not "no HTML conversion at all" but "the text, escaped
// and wrapped".
func EscapeText(text string) string {
	return textEscaper.Replace(text)
}

// textEscaper is EscapeText's replacement table. The five characters are the
// MCP's, in the MCP's order: & must be replaced first or the other escapes
// would be escaped again, which strings.Replacer guarantees by matching the
// input once.
var textEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
	`"`, "&quot;",
	"'", "&#39;",
)

// ParagraphHTML wraps already-escaped text in a paragraph, so a plain-text
// message that carries an attachment reference still reads as prose.
func ParagraphHTML(text string) string {
	return "<p>" + text + "</p>"
}
