package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// apiReferenceDir is the mirror of Microsoft's per-endpoint reference pages
// (refs/INDEX.md, "Mirror layout"). It is gitignored: refs/ only exists after
// scripts/fetch-refs.sh has run.
const apiReferenceDir = "refs/graph/api-reference/v1.0/api"

// requestLine matches "METHOD /path" inside an ```http fenced block, with or
// without the https://graph.microsoft.com/v1.0 prefix the example blocks use.
var requestLine = regexp.MustCompile(`^(GET|POST|PATCH|PUT|DELETE)[ \t]+(.+?)[ \t]*$`)

// shapePattern matches one "{...}" path parameter, whatever its name. The
// api-reference and the OpenAPI description disagree on parameter names all the
// time – "{message-id}" versus "{chatMessage-id}", "{id}" versus "{channel-id}"
// – so routes are compared by shape, never by parameter name.
var shapePattern = regexp.MustCompile(`\{[^}]*\}`)

// parenParamPattern matches the handful of pages that write a path parameter
// with parentheses instead of braces: chatmessage-update.md documents
// "/teams/(team-id)/channels/{channel-id}/messages/{message-id}". A parenthesised
// segment is only treated as a parameter when it carries no OData call syntax,
// so "range()" and "itemAt(index={index})" are left alone.
var parenParamPattern = regexp.MustCompile(`\([^/()=,]+\)`)

// httpBlockRe finds the fenced blocks that can carry a request line. The pages
// are inconsistent: "```http", "``` http" and "```msgraph-interactive" all
// appear, sometimes indented.
var httpBlockRe = regexp.MustCompile("^\\s*```\\s*(?:http|msgraph-interactive)\\s*$")

var fenceEnd = regexp.MustCompile("^\\s*```\\s*$")

// docRoute is one HTTP request line found on an api-reference page.
type docRoute struct {
	Method string
	// Path is the normalized template: parameters are replaced by "{}" so the
	// route can be compared across parameter-name spellings.
	Path string
}

// parseAPIReferencePage extracts every HTTP request line from one
// api-reference markdown page. Both the "## HTTP request" template blocks and
// the example blocks are scanned; the template blocks are the ones that matter
// (their paths are already parameterized), but example blocks are harmless
// because they normalize to the same shapes.
func parseAPIReferencePage(body string) []docRoute {
	var out []docRoute
	inBlock := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, "\r")
		if !inBlock {
			if httpBlockRe.MatchString(line) {
				inBlock = true
			}
			continue
		}
		if fenceEnd.MatchString(line) {
			inBlock = false
			continue
		}
		m := requestLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		path := normalizeDocPath(m[2])
		if path == "" {
			continue
		}
		out = append(out, docRoute{Method: m[1], Path: path})
	}
	return out
}

// httpVersionSuffix matches the "HTTP/1.1" token a few example blocks append to
// the request line. It must be matched explicitly rather than by cutting at the
// first space, because api-reference paths contain spaces inside alternation
// groups: "/users/{user-id | user-principal-name}/chats".
var httpVersionSuffix = regexp.MustCompile(`\s+HTTP/\d(?:\.\d)?\s*$`)

// normalizeDocPath turns a documented request target into a shape:
//
//	https://graph.microsoft.com/v1.0/teams/{team-id}/channels/{channel-id}/messages
//	  -> /teams/{}/channels/{}/messages
//
// Absolute URLs are reduced to their path, query strings are dropped, and the
// "HTTP/1.1" suffix some example blocks carry is removed.
func normalizeDocPath(target string) string {
	target = httpVersionSuffix.ReplaceAllString(strings.TrimSpace(target), "")
	if i := strings.Index(target, "://"); i >= 0 {
		rest := target[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			target = rest[j:]
		}
	}
	if i := strings.Index(target, "?"); i >= 0 {
		target = target[:i]
	}
	target = strings.ReplaceAll(target, " ", "")
	// Requests are relative to the service root, so strip the version segment
	// an absolute or half-absolute ("/v1.0/...") example may carry.
	target = strings.TrimPrefix(target, "/v1.0")
	if target == "" || !strings.HasPrefix(target, "/") {
		return ""
	}
	return pathShape(target)
}

// pathShape normalizes a canonical route path (or a concrete request path) to
// the same "{}"-per-parameter shape used by docRoute.
func pathShape(p string) string {
	if i := strings.Index(p, "?"); i >= 0 {
		p = p[:i]
	}
	p = parenParamPattern.ReplaceAllString(p, "{}")
	return shapePattern.ReplaceAllString(p, "{}")
}

// pageHasRoute reports whether an api-reference page documents method+shape.
//
// It accepts two kinds of evidence, because the pages are inconsistent:
//
//   - a template match: the page's "HTTP request" block lists the route with
//     parameter placeholders, which is the normal case;
//   - a suffix match: some pages document the same operation only under a
//     different container (chatmessage-softdelete.md documents the chat form as
//     "/users/{userId}/chats/{chatsId}/messages/{id}/softDelete" while the CLI
//     calls "/chats/{chat-id}/messages/{id}/softDelete"). The documented shape
//     must be a strict prefix-plus-suffix of the wanted shape, so the
//     distinguishing tail is genuinely documented.
//
// Concrete example blocks are matched by shape too: a page that only shows real
// IDs is still evidence that the route exists, and shape comparison reduces
// those IDs to wildcards, so the example-only documented routes are covered by
// the equal-length comparison.
func pageHasRoute(routes []docRoute, method, shape string) bool {
	want := strings.Split(shape, "/")
	for _, r := range routes {
		if r.Method != method {
			continue
		}
		got := strings.Split(r.Path, "/")
		if len(got) == len(want) && shapeMatches(got, want) {
			return true
		}
		if len(got) > len(want) && strictSuffixMatch(got, want) {
			return true
		}
	}
	return false
}

// shapeMatches compares two shapes of equal length segment by segment, treating
// "{}" as a wildcard in either side.
func shapeMatches(got, want []string) bool {
	for i := range want {
		if got[i] == "{}" || want[i] == "{}" {
			continue
		}
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// strictSuffixMatch reports whether the literal segments of want appear, in
// order and in their relative positions, as a suffix of got.
//
// Parameters are skipped on both sides, which is what lets the page's
// "/users/{userId}/chats/{chatsId}/messages/{chatMessageId}/softDelete" be
// accepted as documentation for "/chats/{chat-id}/messages/{id}/softDelete":
// the literals "chats" and "softDelete" line up. Requiring the literals keeps
// "/me/people" from matching "/teams/{}/channels/{}/messages/{}/hostedContents".
func strictSuffixMatch(got, want []string) bool {
	gotLiterals := literalsOf(got)
	wantLiterals := literalsOf(want)
	if len(wantLiterals) == 0 || len(gotLiterals) <= len(wantLiterals) {
		return false
	}
	tail := gotLiterals[len(gotLiterals)-len(wantLiterals):]
	for i := range wantLiterals {
		if tail[i] != wantLiterals[i] {
			return false
		}
	}
	return true
}

// literalsOf returns the non-parameter segments of a shape.
func literalsOf(segments []string) []string {
	out := make([]string, 0, len(segments))
	for _, s := range segments {
		if s != "{}" && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// readAPIReferencePage reads one page from the mirror.
func readAPIReferencePage(root, page string) (string, error) {
	path := filepath.Join(root, apiReferenceDir, page)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf(
				"contract: api-reference page %s is missing; the reference mirror is gitignored, "+
					"so run scripts/fetch-refs.sh first", path)
		}
		return "", fmt.Errorf("contract: read %s: %w", path, err)
	}
	return string(b), nil
}

// verifyRoutesAgainstAPIReference checks every committed route against the
// page it claims to come from. This is the invariant that makes the route list
// trustworthy: a route that is not documented on its page fails the generator,
// and so does a page that documents a different method or shape.
func verifyRoutesAgainstAPIReference(root string) error {
	pages := map[string][]docRoute{}
	for _, r := range routes {
		if _, ok := pages[r.Source]; !ok {
			body, err := readAPIReferencePage(root, r.Source)
			if err != nil {
				return err
			}
			pages[r.Source] = parseAPIReferencePage(body)
		}
		shape := pathShape(r.Path)
		if !pageHasRoute(pages[r.Source], r.Method, shape) {
			return fmt.Errorf(
				"contract: route %s %s is not documented on %s (looked for shape %s); "+
					"the route list in internal/testing/contract/routes.go and the api-reference disagree",
				r.Method, r.Path, r.Source, shape)
		}
	}
	return nil
}
