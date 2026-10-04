package fakegraph

import (
	"net/http"
	"net/url"
	"strings"
)

// handlerCtx is what a route handler receives.
type handlerCtx struct {
	s      *Server
	w      http.ResponseWriter
	r      *http.Request
	rel    string
	params map[string]string
	query  url.Values
	body   []byte
	rec    *RecordedRequest
}

// param returns a path parameter captured by the route pattern, for example
// the "team-id" of "/teams/{team-id}".
func (c *handlerCtx) param(name string) string { return c.params[name] }

// json writes a JSON response.
func (c *handlerCtx) json(status int, v any) { writeJSON(c.w, status, v) }

// noContent writes a 204.
func (c *handlerCtx) noContent() { writeNoContent(c.w) }

// fail writes a Graph error envelope for an apiError.
func (c *handlerCtx) fail(err *apiError) {
	writeError(c.w, c.r, err.Status, err.Code, err.Message)
}

// decodeBody decodes the request body into v, reporting a 400 and returning
// false when the body is not valid JSON. Graph answers 400 BadRequest for a
// malformed body.
func (c *handlerCtx) decodeBody(v any) bool {
	if err := decodeJSON(c.body, v); err != nil {
		c.fail(badRequestf("The request body is not valid JSON: %v", err))
		return false
	}
	return true
}

// preferUnknownEnums reports whether the caller asked for evolvable enum
// members to be rendered instead of unknownFutureValue. Without this header
// Graph collapses systemEventMessage, which is why every message read sends it
// (refs/graph/api-reference/v1.0/resources/chatmessage.md:81;
// docs/spike/phase1.md:54).
func (c *handlerCtx) preferUnknownEnums() bool {
	return strings.Contains(strings.ToLower(c.r.Header.Get("Prefer")), PreferUnknownEnumMembers)
}

// PreferUnknownEnumMembers is the Prefer value the internal/graph client sends.
const PreferUnknownEnumMembers = "include-unknown-enum-members"

// splitODataFields splits a filter expression on whitespace that is outside a
// single-quoted literal, keeping the quotes. OData escapes a quote by doubling
// it, so a doubled pair must not toggle the quoted state; without this,
// "displayName eq 'Alice Example'" would split into four fields.
func splitODataFields(s string) []string {
	var (
		out     []string
		b       strings.Builder
		inQuote bool
	)
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case r == '\'':
			if inQuote && i+1 < len(runes) && runes[i+1] == '\'' {
				b.WriteRune(r)
				b.WriteRune(runes[i+1])
				i++
				continue
			}
			inQuote = !inQuote
			b.WriteRune(r)
		case (r == ' ' || r == '\t') && !inQuote:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// routeDef is one route the fake serves.
type routeDef struct {
	method  string
	pattern string
	// scopes are the any-of delegated scopes the route needs. An empty list
	// means no scope is checked (only /$batch, whose sub-requests are checked
	// individually).
	scopes []string
	fn     func(*handlerCtx)
	// notAllowed marks a path that exists in the service but not with this
	// method, so it answers 405 rather than 404. The spike saw exactly that
	// for POST /chats/{id}/softDelete and for a standalone hostedContents POST
	// (docs/spike/phase1.md:91,99).
	notAllowed bool
}

// splitPath splits a Graph path into segments, ignoring empty ones.
func splitPath(p string) []string {
	trimmed := strings.Trim(p, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// matchPattern matches a path against a pattern whose "{name}" segments capture
// a single segment.
//
// A pattern segment may also carry a literal suffix after the placeholder
// ("{file-name}:"), which is how the colon-addressed drive routes are matched:
// "/drives/{drive-id}/items/{parent-ref}:/{file-name}:/content" captures
// "parent-ref" and "file-name" without the colons that delimit them
// (refs/graph/api-reference/v1.0/api/driveitem-put-content.md:48).
func matchPattern(pattern, path string) (map[string]string, bool) {
	ps := splitPath(pattern)
	qs := splitPath(path)
	if len(ps) != len(qs) {
		return nil, false
	}
	var params map[string]string
	for i := range ps {
		if name, suffix, ok := patternParam(ps[i]); ok {
			value := qs[i]
			if value == "" {
				return nil, false
			}
			if suffix != "" {
				if len(value) <= len(suffix) || !strings.HasSuffix(strings.ToLower(value), strings.ToLower(suffix)) {
					return nil, false
				}
				value = value[:len(value)-len(suffix)]
			}
			if params == nil {
				params = make(map[string]string, 4)
			}
			params[name] = value
			continue
		}
		if !strings.EqualFold(ps[i], qs[i]) {
			return nil, false
		}
	}
	if params == nil {
		params = map[string]string{}
	}
	return params, true
}

// patternParam reports whether a pattern segment captures a path parameter, and
// returns the parameter's name and the literal that follows it ("{file-name}:"
// has the suffix ":").
func patternParam(segment string) (name, suffix string, ok bool) {
	if !strings.HasPrefix(segment, "{") {
		return "", "", false
	}
	end := strings.IndexByte(segment, '}')
	if end <= 1 {
		return "", "", false
	}
	return segment[1:end], segment[end+1:], true
}

// matchRoutes returns the routes matching method and path, plus the captured
// parameters.
func matchRoutes(routes []routeDef, method, path string) ([]*routeDef, map[string]string) {
	var (
		matched []*routeDef
		params  map[string]string
	)
	for i := range routes {
		r := &routes[i]
		if r.method != method {
			continue
		}
		p, ok := matchPattern(r.pattern, path)
		if !ok {
			continue
		}
		matched = append(matched, r)
		params = p
	}
	return matched, params
}

// routePatternMatches reports whether any route serves the path, whatever the
// method. It is what turns a wrong method into 405 instead of 404.
func routePatternMatches(routes []routeDef, path string) bool {
	for i := range routes {
		if _, ok := matchPattern(routes[i].pattern, path); ok {
			return true
		}
	}
	return false
}
