package format

import (
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// This file builds the `<at id="N">` tags and the matching `mentions[]` array a
// Teams message body needs, which is the write-side counterpart of the mention
// handling in htmlmd.go.
//
// It is a port of the MCP's processMentionsInHtml
// (refs/teams-mcp/src/utils/users.ts:137-173) with the three fixes PLAN.md:178
// asks for:
//
//   - each `mentions[]` entry carries `mentioned.user.displayName` and
//     `userIdentityType: "aadUser"`, which the MCP leaves out even though the
//     documented example sends them
//     (refs/graph/api-reference/v1.0/api/chatmessage-post.md:213-224,
//     refs/graph/api-reference/v1.0/resources/teamworkuseridentity.md);
//   - one `<at>` per *user*, not per typed occurrence: the MCP assigns one id
//     per mapping and then replaces the text of that mapping, so the same person
//     mentioned twice yields two mentions[] entries for one person. Here the
//     duplicate collapses into the first entry, and every occurrence of that
//     person's text gets the same id;
//   - a mention whose text does not appear in the body is an error instead of a
//     `mentions[]` entry with no matching `<at id>` tag. The document is
//     explicit that the id "Matches the {index} value in the corresponding
//     <at id="{index}"> tag in the message body"
//     (refs/graph/api-reference/v1.0/resources/chatmessagemention.md), so the
//     MCP's shape is a payload Graph has to reject or ignore.
//
// The body is parsed and re-rendered rather than rewritten with a regexp,
// because the matched text can sit next to markup: `@alice` inside
// `href="mailto:@alice"` is an attribute, not a mention, and a regexp cannot
// tell the two apart.
//
// ApplyMentions must run on the *sanitized* body: `<at>` is not in the
// allow-list, so a sanitizer pass afterwards would drop every mention
// (PLAN.md:179).

// MentionTarget is one person a body should mention.
//
// Handles are the spellings to look for, most specific first, each *without*
// the leading "@" ("alice@example.com", "Alice Example", "alice"). The first
// one that occurs in the body wins; the others are then irrelevant for that
// person. A handle is matched case-insensitively, and only as a whole token:
// `@alice` does not match inside `@alicebob` or inside an e-mail address.
type MentionTarget struct {
	// UserID is the mentioned user's id. It is required: it is the only thing
	// in the payload that identifies the person.
	UserID string
	// DisplayName is the name Teams shows for the mention and the text the
	// `<at>` tag carries.
	DisplayName string
	// TenantID is optional; the documented example omits it.
	TenantID string
	// Handles are the spellings to match, without the "@".
	Handles []string
}

// MentionEntry is one element of a message's `mentions[]` array
// (refs/graph/api-reference/v1.0/resources/chatmessagemention.md).
//
// The caller renders it as the Graph payload; the field names here describe the
// values, and the JSON shape lives in internal/graph.
type MentionEntry struct {
	// ID is the mention's id, which is also the index inside the body's
	// `<at id="N">` tag.
	ID int
	// UserID is mentioned.user.id.
	UserID string
	// DisplayName is both mentionText and mentioned.user.displayName.
	DisplayName string
	// TenantID is mentioned.user.tenantId, when known.
	TenantID string
	// Matched is the spelling found in the body. It is diagnostics only: it is
	// what the user typed, and it is not sent to Graph.
	Matched string
}

// UnmatchedMentionError reports a mention target whose text is nowhere in the
// body. It is a usage mistake (the user asked to mention someone the body does
// not name), so the CLI turns it into exit code 2.
type UnmatchedMentionError struct {
	// DisplayName names the person, for the message.
	DisplayName string
	// UserID is the person's id, for when the display name is empty.
	UserID string
	// Handles lists the spellings that were tried.
	Handles []string
}

// Error implements error.
func (e *UnmatchedMentionError) Error() string {
	name := e.DisplayName
	if name == "" {
		name = e.UserID
	}
	tried := make([]string, 0, len(e.Handles))
	for _, h := range e.Handles {
		tried = append(tried, "@"+h)
	}
	if len(tried) == 0 {
		return "no spelling of " + name + " was found in the body"
	}
	return "the body does not mention " + name + " (looked for " + strings.Join(tried, ", ") + ")"
}

// ApplyMentions replaces the targets' text with `<at id="N">` tags and returns
// the body plus the `mentions[]` entries in id order.
//
// A body that names nobody gets "", nil and no error, and the caller then sends
// no `mentions` property at all: the array is optional
// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:47).
func ApplyMentions(body string, targets []MentionTarget) (string, []MentionEntry, error) {
	if len(targets) == 0 {
		return body, nil, nil
	}
	runs, err := mentionRuns(targets)
	if err != nil {
		return "", nil, err
	}

	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return "", nil, fmt.Errorf("format: parse the message body to insert mentions: %w", err)
	}
	root := bodyNode(doc)
	if root == nil {
		return "", nil, fmt.Errorf("format: parse the message body to insert mentions: no body element")
	}

	for i := range runs {
		run := &runs[i]
		for _, handle := range run.handles {
			if !replaceHandle(root, handle, *run) {
				continue
			}
			run.matched = handle
			break
		}
		if run.matched == "" {
			return "", nil, &UnmatchedMentionError{
				DisplayName: run.displayName,
				UserID:      run.userID,
				Handles:     run.handles,
			}
		}
	}

	var out strings.Builder
	for child := root.FirstChild; child != nil; child = child.NextSibling {
		if err := html.Render(&out, child); err != nil {
			return "", nil, fmt.Errorf("format: render the message body after inserting mentions: %w", err)
		}
	}

	entries := make([]MentionEntry, 0, len(runs))
	for _, run := range runs {
		entries = append(entries, MentionEntry{
			ID:          run.id,
			UserID:      run.userID,
			DisplayName: run.displayName,
			TenantID:    run.tenantID,
			Matched:     "@" + run.matched,
		})
	}
	return out.String(), entries, nil
}

// mentionRun is one person's pending substitution: the id assigned to them and
// the handles still to try.
type mentionRun struct {
	id          int
	userID      string
	displayName string
	tenantID    string
	handles     []string
	// matched is the handle that was found, empty until then.
	matched string
}

// mentionRuns turns the requested targets into runs, in the order they were
// asked for, collapsing duplicates by user id and merging their handles.
//
// The order matters: it is the order of `mentions[]` and therefore of the ids
// in the body, and Graph matches one to the other (PLAN.md:178: "one <at> per
// user", which is what makes the order stable).
func mentionRuns(targets []MentionTarget) ([]mentionRun, error) {
	runs := make([]mentionRun, 0, len(targets))
	index := map[string]int{}
	for _, target := range targets {
		userID := strings.TrimSpace(target.UserID)
		if userID == "" {
			return nil, fmt.Errorf("format: a mention needs a user id (the body said %q)", target.DisplayName)
		}
		if at, ok := index[userID]; ok {
			runs[at].handles = mergeHandles(runs[at].handles, target.Handles)
			if runs[at].displayName == "" {
				runs[at].displayName = firstNonEmptyTrimmed(target.DisplayName)
			}
			continue
		}
		index[userID] = len(runs)
		runs = append(runs, mentionRun{
			id:          len(runs),
			userID:      userID,
			displayName: firstNonEmptyTrimmed(target.DisplayName),
			tenantID:    strings.TrimSpace(target.TenantID),
			handles:     cleanHandles(target.Handles),
		})
	}
	for _, run := range runs {
		if len(run.handles) == 0 {
			return nil, fmt.Errorf("format: a mention needs at least one spelling to look for (user %s)", run.userID)
		}
	}
	return runs, nil
}

// mergeHandles appends the handles of a duplicate target, keeping the first
// occurrence of each and leaving the order alone.
func mergeHandles(existing, more []string) []string {
	return cleanHandles(append(append([]string(nil), existing...), more...))
}

// cleanHandles trims, de-duplicates (case-insensitively) and drops empty
// handles.
func cleanHandles(handles []string) []string {
	out := make([]string, 0, len(handles))
	seen := make(map[string]bool, len(handles))
	for _, handle := range handles {
		handle = strings.TrimSpace(strings.Trim(handle, `"`))
		handle = strings.TrimPrefix(handle, "@")
		handle = strings.TrimSpace(handle)
		if handle == "" {
			continue
		}
		key := strings.ToLower(handle)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, handle)
	}
	return out
}

// replaceHandle replaces every whole-token occurrence of "@handle" (quoted or
// not) under root with an `<at>` element carrying run's id. It reports whether
// the body contained the handle at all.
func replaceHandle(root *html.Node, handle string, run mentionRun) bool {
	var (
		nodes    []*html.Node
		anything bool
	)
	collectTextNodes(root, &nodes)
	for _, node := range nodes {
		locs := matchHandle(node.Data, handle)
		if len(locs) == 0 {
			continue
		}
		anything = true
		splitTextNode(node, locs, func() *html.Node { return atNode(run) })
	}
	return anything
}

// atNode builds the `<at id="N">DisplayName</at>` element.
//
// The display name is written as a text node, so the HTML renderer escapes it:
// a name containing "&" or "<" cannot become markup.
func atNode(run mentionRun) *html.Node {
	node := &html.Node{
		Type: html.ElementNode,
		Data: "at",
		Attr: []html.Attribute{{Key: "id", Val: strconv.Itoa(run.id)}},
	}
	node.AppendChild(&html.Node{Type: html.TextNode, Data: run.displayName})
	return node
}

// collectTextNodes gathers every text node under root, in document order.
func collectTextNodes(node *html.Node, out *[]*html.Node) {
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.TextNode {
			*out = append(*out, child)
		}
		collectTextNodes(child, out)
	}
}

// splitTextNode replaces the matched ranges of a text node with the nodes the
// factory returns, keeping the surrounding text.
//
// The offsets are byte offsets into node.Data, ascending and non-overlapping.
func splitTextNode(node *html.Node, locs [][2]int, build func() *html.Node) {
	text := node.Data
	parent := node.Parent
	if parent == nil {
		return
	}
	last := 0
	for _, loc := range locs {
		if loc[0] > last {
			parent.InsertBefore(&html.Node{Type: html.TextNode, Data: text[last:loc[0]]}, node)
		}
		parent.InsertBefore(build(), node)
		last = loc[1]
	}
	if last < len(text) {
		parent.InsertBefore(&html.Node{Type: html.TextNode, Data: text[last:]}, node)
	}
	parent.RemoveChild(node)
}

// matchHandle returns the byte ranges of every whole-token occurrence of
// "@handle" in text, either bare or quoted (@"handle").
//
// The two boundaries are what keep a mention from matching inside another word:
// the "@" must not follow a word character (so an e-mail address like
// "bob@example.com" is not a mention of "example"), and the handle must be
// followed by a non-word character (so "@alice" does not match "@alicebob").
// A dot ends a match ("@alice.smith" is not a mention of "alice") because it is
// how a typed handle is normally continued.
func matchHandle(text, handle string) [][2]int {
	runes := []rune(text)
	want := []rune(handle)
	if len(want) == 0 {
		return nil
	}

	// byteOffset converts a rune index into a byte offset in text.
	byteOffset := func(i int) int {
		off := 0
		for _, r := range runes[:i] {
			off += len(string(r))
		}
		return off
	}

	var out [][2]int
	for i := 0; i < len(runes); i++ {
		if runes[i] != '@' {
			continue
		}
		if i > 0 && isWordRune(runes[i-1]) {
			continue
		}
		rest := runes[i+1:]
		if len(rest) > 0 && rest[0] == '"' {
			// The quoted form is the only form accepted after a quote, so a
			// handle that is a prefix of the quoted name cannot match inside it.
			if len(rest) < len(want)+2 {
				continue
			}
			if !equalFoldRunes(rest[1:1+len(want)], want) || rest[1+len(want)] != '"' {
				continue
			}
			end := i + 1 + 1 + len(want) + 1
			if end < len(runes) && isWordRune(runes[end]) {
				continue
			}
			out = append(out, [2]int{byteOffset(i), byteOffset(end)})
			i = end - 1
			continue
		}
		if len(rest) < len(want) {
			continue
		}
		if !equalFoldRunes(rest[:len(want)], want) {
			continue
		}
		end := i + 1 + len(want)
		if end < len(runes) && isWordRune(runes[end]) {
			continue
		}
		out = append(out, [2]int{byteOffset(i), byteOffset(end)})
		i = end - 1
	}
	return out
}

// isWordRune reports whether r continues a token: a letter, a digit, or one of
// the characters a name, an address or a path continues with.
func isWordRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	switch r {
	case '_', '-', '.', '@', '/', '\\', '\'':
		return true
	default:
		return false
	}
}

// equalFoldRunes compares two rune slices case-insensitively.
func equalFoldRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] && !strings.EqualFold(string(a[i]), string(b[i])) {
			return false
		}
	}
	return true
}

// firstNonEmptyTrimmed returns the first non-blank value, trimmed.
func firstNonEmptyTrimmed(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// bodyNode finds the <body> element of a parsed document.
func bodyNode(doc *html.Node) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == "body" {
			found = n
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return found
}
