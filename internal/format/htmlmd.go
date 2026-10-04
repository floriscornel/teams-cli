// Package format converts Microsoft Teams message bodies into the markdown the
// CLI renders.
//
// PLAN.md puts both directions here, but only the read half exists: Phase 3
// needs HTML -> markdown, which is what this file implements as a port of the
// MCP's htmlToMarkdown (refs/teams-mcp/src/utils/html-to-markdown.ts) on top of
// github.com/JohannesKaufmann/html-to-markdown/v2 (PLAN.md "Toolchain and local
// workflow" — it replaces Turndown). markdown -> HTML and mention *generation*
// are Phase 4 (PLAN.md line 179, "sanitize before inserting <at>") and are
// deliberately absent: nothing here writes a body for Graph.
//
// Three kinds of element need handling before or during the conversion, because
// no HTML specification knows them:
//
//   - <at id="N">mentionText</at>, the tag that carries a chatMessageMention
//     (refs/graph/api-reference/v1.0/resources/chatmessagemention.md),
//   - <attachment id="...">, which references a chatMessageAttachment
//     (refs/graph/api-reference/v1.0/resources/chatmessageattachment.md),
//   - <systemEventMessage/>, the body of a messageType "systemEventMessage"
//     (refs/graph/api-reference/v1.0/resources/chatmessage.md).
//
// Where a behaviour comes from the MCP rather than from a reference document,
// the comment says so and marks it as observed, not documented (PLAN.md line
// 222). Nothing in this package is global state: every conversion builds its own
// converter.
package format

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/JohannesKaufmann/dom"
	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/strikethrough"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"
)

const (
	// tagMention is the Teams element that wraps a chatMessageMention's
	// mentionText: "It encloses the mentionText of each mention in an HTML at
	// element, with an id attribute that corresponds to the id property of the
	// mention" (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
	tagMention = "at"

	// tagAttachment is the element that references a chatMessageAttachment, a
	// "Reference to attached objects like files, tabs, meetings etc."
	// (refs/graph/api-reference/v1.0/resources/chatmessageattachment.md).
	tagAttachment = "attachment"

	// tagSystemEvent is the body of a system event message. The HTML parser
	// lowercases element names, so the source's <systemEventMessage/> reaches us
	// as "systemeventmessage".
	tagSystemEvent = "systemeventmessage"

	// mentionMarker is what a mention renders as: the MCP's "teamsMention"
	// Turndown rule is "@" plus the tag's content
	// (refs/teams-mcp/src/utils/html-to-markdown.ts).
	mentionMarker = "@"

	// nbsp is the separator Teams puts between the <at> tags of one mention.
	// The MCP compares the raw HTML between two tags against the literal
	// "&nbsp;" (mergeConsecutiveMentions); by the time the HTML parser is done
	// with it, that is U+00A0. Non-breaking spaces are normalised to plain
	// spaces at the end of every conversion, like the MCP does.
	nbsp = "\u00a0"

	// altInlineImage says what an inline image is, because Phase 3 cannot fetch
	// it. The alternative the body gives is a bare "../hostedContents/1/$value"
	// that a reader can do nothing with.
	altInlineImage = "inline image"

	// systemEventToken is the literal text a system event body degrades to once
	// its (unknown) tag has been parsed. The MCP drops any node whose whole text
	// is this token (the "teamsSystemEvent" rule); here it is dropped only when
	// it is the entire body, so prose that mentions the tag still survives.
	systemEventToken = "systemEventMessage/"
)

// hostedContentSrc matches the src of an inline image. Graph references hosted
// content relatively ("Send inline images through hostedContents[] in the
// message POST with <img src="../hostedContents/1/$value">", PLAN.md "Fix these
// gaps found in the MCP"), and the hosted content resource is reachable from the
// message (refs/graph/api-reference/v1.0/resources/chatmessage.md,
// Relationships). Both the relative form and an absolute Graph URL are accepted,
// because a message read back from Graph may carry either.
var hostedContentSrc = regexp.MustCompile("(?i)hostedcontents/([^/?#]+)/[$]value(?:[?#].*)?$")

// Mention is one chatMessageMention worth naming in the body.
//
// The id is what ties a mention to the body: "Matches the {index} value in the
// corresponding <at id="{index}"> tag in the message body"
// (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
type Mention struct {
	// ID is the mention's id property, which the body's <at> tag repeats.
	ID int

	// DisplayName is the name to render for that mention. Use the mentioned
	// entity's display name (mentioned.user.displayName): it identifies the
	// person, so two <at> tags that share it are one mention, and it also names
	// a tag whose body text is empty.
	DisplayName string
}

// MentionNames indexes mentions by their <at id="N"> id.
//
// The result is the map HTMLToMarkdownWithMentions expects. Mentions whose
// display name is empty are skipped, because the body's own tag text is a better
// name than nothing; a later entry with the same id wins (the id is an index
// into mentions[], so duplicates are not expected).
//
// An input without a usable name returns a nil map, which is the same "no
// mentions[] at all" case HTMLToMarkdown covers.
func MentionNames(mentions []Mention) map[int]string {
	if len(mentions) == 0 {
		return nil
	}

	names := make(map[int]string, len(mentions))
	for _, m := range mentions {
		name := strings.TrimSpace(m.DisplayName)
		if name == "" {
			continue
		}
		names[m.ID] = name
	}
	if len(names) == 0 {
		return nil
	}

	return names
}

// HTMLToMarkdown converts a Teams message body with contentType "html".
//
// It is HTMLToMarkdownWithMentions with no mentions[] array, which is all the
// body itself gives you: an <at> tag is named from its own text.
func HTMLToMarkdown(raw string) (string, error) {
	return HTMLToMarkdownWithMentions(raw, nil)
}

// HTMLToMarkdownWithMentions is HTMLToMarkdown with the message's mentions[]
// array, which names <at id="N"> tags the body text does not.
//
// names maps a mention id to a display name (see MentionNames) and is the only
// identity this direction gets. That matters for the merge port below: the MCP
// groups consecutive <at> tags by mentioned.user.id, which the body does not
// carry, so equal display names stand in for "same person".
//
// The body is expected to be the HTML form. itemBody.contentType has two
// possible values, text and html
// (refs/graph/api-reference/v1.0/resources/itembody.md), and "The content is
// always in HTML if the chat message contains a chatMessageMention"
// (refs/graph/api-reference/v1.0/resources/chatmessage.md); BodyToMarkdown is
// the entry point that makes that choice.
//
// The output is markdown, never HTML: a raw <at> or <attachment> tag cannot
// survive (both are rendered, not passed through), and malformed input is not an
// error, because an HTML parser recovers from anything. An empty or
// whitespace-only body is "".
func HTMLToMarkdownWithMentions(raw string, names map[int]string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}

	conv := newConverter(names)
	protected := protectAngleBracketEntities(raw)

	md, err := conv.ConvertString(protected)
	if err != nil {
		// The HTML parser refuses a document that opens more than 512 elements
		// ("html: open stack of elements exceeds 512 nodes"). A Teams body nests
		// a handful deep, so only a hostile or badly corrupt one gets here; the
		// words are worth more than the structure, so the body is converted as
		// text instead of failing the read.
		md, err = conv.ConvertString(escapeMarkup(protected))
	}
	if err != nil {
		return "", fmt.Errorf("format: convert Teams message HTML to markdown: %w", err)
	}

	out := restoreAngleBracketEntities(normalizeMarkdown(string(md)))

	// A system event body with nothing left to show ("<systemEventMessage/>", or
	// the literal token when a parser ate the tag) reads as an empty message,
	// which is what the MCP's rule produces. MCP-observed, not documented: Teams
	// keeps the event detail in eventDetail
	// (refs/graph/api-reference/v1.0/resources/chatmessage.md), so the body
	// itself can be empty.
	if out == systemEventToken {
		return "", nil
	}

	return out, nil
}

// BodyToMarkdown converts either itemBody shape ("html" or "text").
//
// The contentType is matched case-insensitively and tolerantly: it is an enum of
// the two values above, and a body whose type we do not recognise is converted
// as HTML, the form that carries mentions.
//
// A "text" body is not markup, so nothing in it is translated; only whitespace
// is normalised, and — like every other path out of this package — the words
// "<at" and "<attachment" are kept from looking like an element. The reference
// documents do not describe mentions in a text body, and by the rule quoted
// above they cannot be there.
func BodyToMarkdown(content, contentType string, names map[int]string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(contentType), "text") {
		// normalizeText cannot produce a Teams element, but a body that says
		// "text" and then contains "<at id="1">" would otherwise hand the raw
		// tag straight through, so the same guarantee is applied to it.
		return escapeTeamsTagLookalikes(normalizeText(content)), nil
	}

	return HTMLToMarkdownWithMentions(content, names)
}

// newConverter builds the converter for one conversion.
//
// It is built per call rather than kept in a package-level variable: the mention
// names are closed over by the <at> pre-renderer, and a shared converter would
// be mutable package state.
//
// The plugin set is the MCP's Turndown configuration, which enables GitHub
// Flavored Markdown (turndown-plugin-gfm): tables and strikethrough, plus
// CommonMark for everything else. The table plugin's own default drops any table
// whose cells contain newlines, which is most of the tables Teams sends, so
// newlines are preserved and a table degrades to a readable pipe table instead
// of to loose text.
func newConverter(names map[int]string) *converter.Converter {
	conv := converter.NewConverter(converter.WithPlugins(
		base.NewBasePlugin(),
		// The MCP's Turndown options are otherwise the library's defaults
		// (headingStyle atx, bulletListMarker "-", codeBlockStyle fenced,
		// emDelimiter "*", strongDelimiter "**"), but the horizontal rule is not:
		// the MCP writes "---" and the library defaults to "* * *".
		commonmark.NewCommonmarkPlugin(commonmark.WithHorizontalRule("---")),
		strikethrough.NewStrikethroughPlugin(),
		table.NewTablePlugin(
			table.WithNewlineBehavior(table.NewlineBehaviorPreserve),
			// Minimal padding keeps the MCP's table shape ("| A | 1 |") instead
			// of padding every cell to the widest one in its column.
			table.WithCellPaddingBehavior(table.CellPaddingBehaviorMinimal),
			// Teams sends tables without a <thead> as often as with one; promoting
			// the first row keeps the header row of a GFM table meaningful
			// instead of leaving it empty.
			table.WithHeaderPromotion(true),
		),
	))

	// <at> and <attachment> get renderers, not removal: the fallback renderer
	// for an unknown element only emits its children, which would silently drop
	// a mention's "@" and an attachment's reference. The sanitizer problem from
	// PLAN.md line 179 (a bluemonday pass over an assembled body strips the
	// unknown element and drops every mention) is what a pass-through would
	// recreate here.
	conv.Register.RendererFor(tagMention, converter.TagTypeInline, renderMention, converter.PriorityEarly)
	// An attachment is a card or a file that Teams shows on its own line, so it
	// is a block: glued to the text before it, "{attachment:x}" would read as
	// part of the sentence.
	conv.Register.RendererFor(tagAttachment, converter.TagTypeBlock, renderAttachment, converter.PriorityEarly)

	// Pre-renderers run before the plugins' own passes. The three below touch
	// disjoint elements, so their relative order does not matter, but distinct
	// priorities keep the order fixed anyway.
	conv.Register.PreRenderer(preRenderMentions(names), converter.PriorityEarly+1)
	conv.Register.PreRenderer(preRenderHostedContentImages, converter.PriorityEarly+2)
	conv.Register.PreRenderer(preRenderSystemEvents, converter.PriorityEarly+3)

	return conv
}

// renderMention renders a Teams <at> element as "@name".
//
// The MCP's rule is "@" plus the element's content
// (refs/teams-mcp/src/utils/html-to-markdown.ts, the "teamsMention" rule), and
// that is all that is needed here: preRenderMentions has already replaced the
// content with the display name from mentions[] where there is one, and the
// content of a real tag is the mentionText, which the docs describe as "a user's
// display name" (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
func renderMention(ctx converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
	writeString(w, mentionMarker)
	ctx.RenderChildNodes(ctx, w, n)

	return converter.RenderSuccess
}

// renderAttachment renders an <attachment> element as a one-line reference.
//
// The shape is the MCP's ({attachment:<id>}, or {attachment} without an id), so
// a reader of the markdown sees that a card or file was there and which
// attachment id it was (refs/teams-mcp/src/utils/html-to-markdown.ts). Nothing
// is invented for it: the attachments[] array is not part of this call, so an
// attachment the body does not mention is not described.
//
// The marker is a block, because Teams shows an attachment on its own line.
// Any content inside the element is rendered after the marker. The MCP's
// pre-processing only rewrites the empty form, but an HTML parser ignores the
// self-closing slash on an unknown element, so everything behind an
// "<attachment id=.../>" ends up as that element's children; dropping them would
// lose the message text that follows.
func renderAttachment(ctx converter.Context, w converter.Writer, n *html.Node) converter.RenderStatus {
	id := strings.TrimSpace(dom.GetAttributeOr(n, "id", ""))
	if id == "" {
		writeString(w, "{attachment}")
	} else {
		writeString(w, "{attachment:"+id+"}")
	}

	if n.FirstChild != nil {
		// Keep the marker readable when the parser moved text inside the
		// element (see above): "{attachment:x}after" would read as one word.
		writeString(w, " ")
	}
	ctx.RenderChildNodes(ctx, w, n)

	return converter.RenderSuccess
}

// angleBracketEntity matches the named references for the two characters the
// converter escapes on the way out: "<" and ">". The match is case-insensitive
// because HTML also knows &LT; and friends, which the parser accepts as the same
// characters.
var angleBracketEntity = regexp.MustCompile("(?i)&(lt|gt);")

// ltSentinel and gtSentinel stand in for the characters "&lt;" and "&gt;" meant.
// They live in the Unicode private use area, so a message cannot contain one by
// accident, and they are neither whitespace nor markdown, which keeps them
// intact through collapsing, escaping and trimming.
const (
	ltSentinel = "\uE000"
	gtSentinel = "\uE001"
)

// protectAngleBracketEntities replaces "&lt;" and "&gt;" with a sentinel before
// the conversion.
//
// The HTML parser would decode those entities to "<" and ">" and the converter
// would then escape them straight back to "&lt;" and "&gt;", so the MCP's output
// ("A & B < C > D" for "A &amp; B &lt; C &gt; D",
// refs/teams-mcp/src/utils/__tests__/html-to-markdown.test.ts, "should decode
// HTML entities") would be unreachable. A sentinel is the only form the
// converter leaves alone, and it keeps the distinction between a body that wrote
// the entity and one whose literal text is "&lt;".
func protectAngleBracketEntities(raw string) string {
	return angleBracketEntity.ReplaceAllStringFunc(raw, func(match string) string {
		if match[1] == 'l' || match[1] == 'L' {
			return ltSentinel
		}

		return gtSentinel
	})
}

// markupEscaper turns the two characters the HTML parser reads as markup into
// their entity form.
var markupEscaper = strings.NewReplacer("<", "&lt;", ">", "&gt;")

// escapeMarkup is the fallback for a body the parser will not take: with no
// angle brackets left the document is plain text, and every entity the body
// wrote is still decoded.
func escapeMarkup(raw string) string {
	return markupEscaper.Replace(raw)
}

// restoreAngleBracketEntities is protectAngleBracketEntities' other half: the
// sentinels become the characters the entities stood for, and a span that now
// reads as one of the two Teams elements keeps its entities instead.
func restoreAngleBracketEntities(md string) string {
	md = strings.ReplaceAll(md, ltSentinel, "<")
	md = strings.ReplaceAll(md, gtSentinel, ">")

	return escapeTeamsTagLookalikes(md)
}

// teamsTagLookalike matches the start of one of the two Teams elements, with or
// without its closing slash, wherever it appears in text. It deliberately does
// not require a ">": text can be cut in half, and the promise is about the
// characters "<at" and "<attachment" themselves.
var teamsTagLookalike = regexp.MustCompile("(?i)<(/?)(at|attachment)")

// escapeTeamsTagLookalikes keeps its output free of "<at" and "<attachment".
//
// The two requirements it reconciles are otherwise in conflict: a body whose own
// *text* spells a tag out (the eight characters "&lt;at id='0'&gt;" are words,
// not markup) would decode to a string that reads as a tag once entities are
// decoded. Everywhere else entities are decoded; here the entities stay, so the
// package can promise that its markdown never contains the start of a Teams
// element. The MCP makes no such promise — it reads an already-decoded DOM and
// passes that text through — and the reference documents describe bodies with
// real tags, not escaped ones.
func escapeTeamsTagLookalikes(md string) string {
	return teamsTagLookalike.ReplaceAllString(md, "&lt;$1$2")
}

// writeString writes to the converter's writer.
//
// The writer the converter hands a renderer is a bytes.Buffer, so writing cannot
// fail; the error is dropped deliberately rather than propagated through a
// renderer that has no way to report one.
func writeString(w converter.Writer, s string) {
	_, _ = w.WriteString(s)
}

// preRenderMentions names and merges the body's <at> tags.
//
// Naming: a tag whose id has an entry in names is rewritten to that display name
// before conversion. mentions[] is the authoritative name — and MCP-observed,
// not documented, the body does not always carry the full one, which is the same
// premise the split-tag merge below rests on.
//
// Merging is the port of mergeConsecutiveMentions
// (refs/teams-mcp/src/utils/html-to-markdown.ts). The MCP premise is that Teams
// splits a multi-word display name into one <at> tag per word separated by
// "&nbsp;" — that has no counterpart in the reference docs, which describe one
// tag per mention (refs/graph/api-reference/v1.0/resources/chatmessagemention.md:
// the tag's content is the mentionText of one mention), so it is MCP-observed,
// not documented. Where the MCP groups consecutive tags by mentioned.user.id,
// this port groups them by the resolved display name, since names is the only
// identity the exported API carries.
func preRenderMentions(names map[int]string) converter.HandlePreRenderFunc {
	return func(_ converter.Context, doc *html.Node) {
		mentions := dom.FindAllNodes(doc, isMention)
		for _, n := range mentions {
			if name, ok := mentionName(n, names); ok {
				setTextContent(n, name)
			}
		}

		// Merging happens after naming, so that a merged tag keeps the full
		// display name instead of the pieces the body had.
		for _, n := range mentions {
			mergeMentionsIn(n.Parent, names)
		}
	}
}

// preRenderHostedContentImages labels the inline images of a message.
//
// Phase 3 cannot download hosted content: that needs the message's
// hostedContents[] collection
// (refs/graph/api-reference/v1.0/resources/chatmessage.md, Relationships) and a
// separate GET. The image therefore stays a markdown image whose alt text says
// what it is and keeps the hosted-content id visible, so a reader knows an image
// was there and which id to fetch.
func preRenderHostedContentImages(_ converter.Context, doc *html.Node) {
	for _, n := range dom.FindAllNodes(doc, isImage) {
		src, ok := dom.GetAttribute(n, "src")
		if !ok {
			continue
		}

		match := hostedContentSrc.FindStringSubmatch(src)
		if match == nil {
			continue
		}

		setAttribute(n, "alt", hostedContentAlt(match[1], dom.GetAttributeOr(n, "alt", "")))
	}
}

// preRenderSystemEvents degrades the <systemEventMessage> wrapper to one line of
// text.
//
// A system event keeps its detail in eventDetail and gets messageType
// "systemEventMessage" (refs/graph/api-reference/v1.0/resources/chatmessage.md),
// an evolvable-enum member that "never appears in the parsed messageType" unless
// the request asks for Prefer: include-unknown-enum-members (PLAN.md line 245);
// the body is read here as it arrives.
//
// Teams returns the wrapper without content ("<systemEventMessage/>"), and the
// MCP deletes such an element together with everything inside it. Dropping the
// content is what this port does not do: because an HTML parser ignores the
// self-closing slash on an unknown element, text behind the tag is inside it,
// and the MCP's own fixtures keep text that merely mentions the tag. An empty
// wrapper is removed; a wrapper with text becomes that text, on a single line.
func preRenderSystemEvents(_ converter.Context, doc *html.Node) {
	for _, n := range dom.FindAllNodes(doc, isSystemEvent) {
		if n.Parent == nil {
			// An enclosing node was already replaced or removed.
			continue
		}

		line := strings.Join(strings.Fields(dom.CollectText(n)), " ")
		if line == "" {
			dom.RemoveNode(n)
			continue
		}

		dom.ReplaceNode(n, textNode(line))
	}
}

// mergeMentionsIn merges the runs of consecutive <at> children of parent.
//
// Two mentions merge when nothing but a single non-breaking space separates them
// and both resolve to the same display name (see preRenderMentions for why the
// name stands in for the MCP's user id). The first tag of a run survives with
// the full name; the tags after it and the non-breaking spaces between them
// disappear, exactly like the MCP's rebuild step, which replaces the whole run
// with one tag.
//
// Two mentions with nothing at all between them are separated with a space. The
// MCP does the same with a regex over "</at><at id=...>", because Teams
// sometimes sends no separator; that premise is MCP-observed, not documented.
func mergeMentionsIn(parent *html.Node, names map[int]string) {
	if parent == nil {
		return
	}

	var prev *html.Node // the last <at> child seen, if any
	gap := ""           // the text between prev and the child being visited

	for child := parent.FirstChild; child != nil; {
		next := child.NextSibling

		switch {
		case isMention(child):
			if prev != nil && gap == nbsp && sameMention(prev, child, names) {
				removeBetween(prev, child)
				dom.RemoveNode(child)
			} else {
				if prev != nil && gap == "" {
					parent.InsertBefore(textNode(" "), child)
				}
				prev = child
			}
			gap = ""
		case child.Type == html.TextNode:
			gap += child.Data
		default:
			// Any other node ends the run: the MCP looks at the plain string
			// between two tags, so markup there was never part of a merge.
			prev = nil
			gap = ""
		}

		child = next
	}
}

// isMention reports whether n is one of the body's <at> elements.
func isMention(n *html.Node) bool {
	return dom.NodeName(n) == tagMention
}

// isImage reports whether n is an <img> element.
func isImage(n *html.Node) bool {
	return dom.NodeName(n) == "img"
}

// isSystemEvent reports whether n is the <systemEventMessage> wrapper.
func isSystemEvent(n *html.Node) bool {
	return dom.NodeName(n) == tagSystemEvent
}

// mentionName resolves the name to render for a mention element.
//
// The boolean is false when the mention's id is missing, is not a number, or has
// no usable name in names; the caller then keeps the tag's own text. The docs
// make the id an Int32 ("Index of an entity being mentioned in the specified
// chatMessage", refs/graph/api-reference/v1.0/resources/chatmessagemention.md),
// so anything else is not a mention id we can match.
func mentionName(n *html.Node, names map[int]string) (string, bool) {
	raw, ok := dom.GetAttribute(n, "id")
	if !ok {
		return "", false
	}

	id, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}

	name := strings.TrimSpace(names[id])
	if name == "" {
		return "", false
	}

	return name, true
}

// sameMention reports whether two mention elements are the same person, which is
// what allows their tags to be merged. Both need a name from mentions[]: without
// one there is nothing to identify them by, and the MCP likewise merges only
// tags whose user id it looked up.
func sameMention(a, b *html.Node, names map[int]string) bool {
	nameA, okA := mentionName(a, names)
	if !okA {
		return false
	}

	nameB, okB := mentionName(b, names)

	return okB && nameA == nameB
}

// setTextContent replaces an element's children with one text node.
//
// Writing a text node (rather than markdown directly) keeps the display name
// inside the converter's escaping rules, so a name that contains markdown
// characters cannot become formatting.
func setTextContent(n *html.Node, text string) {
	for child := n.FirstChild; child != nil; child = n.FirstChild {
		n.RemoveChild(child)
	}

	n.AppendChild(textNode(text))
}

// setAttribute sets or replaces an attribute on an element.
func setAttribute(n *html.Node, key, value string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = value
			return
		}
	}

	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: value})
}

// removeBetween removes the nodes between two siblings, exclusive.
func removeBetween(from, to *html.Node) {
	for n := from.NextSibling; n != nil && n != to; n = from.NextSibling {
		dom.RemoveNode(n)
	}
}

// textNode builds a text node.
func textNode(text string) *html.Node {
	return &html.Node{Type: html.TextNode, Data: text}
}

// hostedContentAlt is the alt text of an inline image: the marker, the
// hosted-content id, and — after the marker, so that the marker always reads
// first — whatever alt text the body already had.
func hostedContentAlt(id, existing string) string {
	alt := altInlineImage + " " + id

	existing = strings.TrimSpace(existing)
	if existing != "" && existing != alt {
		return alt + ": " + existing
	}

	return alt
}

// normalizeMarkdown is the MCP's last step: non-breaking spaces become plain
// spaces ("Normalize non-breaking spaces for LLM consumption") and the result is
// trimmed.
func normalizeMarkdown(md string) string {
	return strings.TrimSpace(strings.ReplaceAll(md, nbsp, " "))
}

// normalizeText cleans a plain-text body for rendering: line endings are
// uniform, non-breaking spaces become the same plain spaces the markdown path
// produces, and the body is trimmed.
func normalizeText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")

	return normalizeMarkdown(s)
}
