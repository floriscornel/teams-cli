package fakegraph

import (
	"net/url"
	"strconv"
	"strings"
)

// This file implements the paging every collection shares. Graph pages with
// `@odata.nextLink`, which a client must follow verbatim
// (refs/graph/concepts/paging.md:34,91); the fake also accepts the `$skip` and
// `$skiptoken` forms on the way in, because a next link it generated carries
// $skiptoken and a client may legitimately send $skip.

// Page-size limits. The message, reply and chat caps are documented (50) and
// the spike confirmed the 400
// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:35,
// chatmessage-list-replies.md:30, chat-list-messages.md:32, chat-list.md:31;
// docs/spike/phase1.md:52,56,58). Member lists default to 100 and cap at 999
// (refs/graph/api-reference/v1.0/api/team-list-members.md:30,
// channel-list-members.md:29); the spike saw them page even at `$top=5`
// (docs/spike/phase1.md:50). The directory collections use the same 100/999
// window (refs/graph/api-reference/v1.0/api/user-list.md:36).
const (
	maxTopMessages     = 50
	defaultTopMessages = 20
	maxTopMembers      = 999
	defaultTopMembers  = 100
	maxTopChildren     = 999
	defaultTopChildren = 200
	defaultTopPeople   = 25
	// RepliesExpandedMax is how many replies `$expand=replies` inlines before
	// it hands the rest to replies@odata.nextLink
	// (refs/graph/api-reference/v1.0/api/channel-list-messages.md:37).
	RepliesExpandedMax = 200
)

// pageParams is the resolved window of one page.
type pageParams struct {
	// Top is the effective page size.
	Top int
	// Offset is the 0-based first item of the page.
	Offset int
}

// parsePage resolves $top and the paging offset. def and limit are the
// collection's documented defaults and caps. A $top over the cap is a 400, not
// a clamp: the spike saw 400 for $top=51
// (docs/spike/phase1.md:52), and silently clamping would hide a client bug.
func parsePage(q url.Values, def, limit int) (pageParams, *apiError) {
	p := pageParams{Top: def}
	if raw := q.Get("$top"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return p, badRequestf("Invalid value for '$top': %q is not an integer.", raw)
		}
		if n < 1 {
			return p, badRequestf("The '$top' value must be at least 1, got %d.", n)
		}
		if n > limit {
			return p, badRequestf("The '$top' value is greater than the maximum allowed value. The maximum allowed value is %d.", limit)
		}
		p.Top = n
	}
	for _, key := range []string{"$skiptoken", "$skip"} {
		raw := q.Get(key)
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			return p, badRequestf("Invalid value for '%s': %q is not a non-negative integer.", key, raw)
		}
		p.Offset = n
		break
	}
	return p, nil
}

// window returns the page slice and the offset of the following page (equal to
// len(items) when there is none).
func window[T any](items []T, p pageParams) ([]T, int) {
	if p.Offset >= len(items) {
		return nil, len(items)
	}
	end := p.Offset + p.Top
	if end > len(items) {
		end = len(items)
	}
	return items[p.Offset:end], end
}

// nextLink builds the absolute `@odata.nextLink` for the next page of a
// route. The original query parameters are preserved and the offset is carried
// in $skiptoken, which is the form the fake emits (Graph emits either
// $skiptoken or $skip depending on the API,
// refs/graph/concepts/paging.md:91).
func (s *Server) nextLink(rel string, q url.Values, nextOffset int) string {
	next := url.Values{}
	for k, vs := range q {
		if k == "$skiptoken" || k == "$skip" {
			continue
		}
		next[k] = vs
	}
	next.Set("$skiptoken", strconv.Itoa(nextOffset))
	return s.url + rel + "?" + next.Encode()
}

// pageOf wires one page: nextOffset is the offset of the following page and
// total the size of the whole collection. A link is added only when another
// page exists, which is exactly when Graph sends one
// (refs/graph/concepts/paging.md:34).
func pageOf[T any](s *Server, rel string, q url.Values, items []T, nextOffset, total int) collectionWire[T] {
	page := collectionWire[T]{Value: items}
	if nextOffset < total {
		page.NextLink = s.nextLink(rel, q, nextOffset)
	}
	return page
}

// pageAndLink is the common case: window a collection and wire the next link.
func pageAndLink[T any](s *Server, rel string, q url.Values, all []T, p pageParams) collectionWire[T] {
	items, next := window(all, p)
	return pageOf(s, rel, q, items, next, len(all))
}

// rejectUnsupported rejects the OData parameters a collection does not
// support, with the wording the live service uses: "Parameter 'Filter' not
// supported" (docs/spike/phase1.md:53). This is what keeps a caller from
// believing a $filter was applied when Graph ignored it.
func rejectUnsupported(q url.Values, allowed ...string) *apiError {
	ok := map[string]bool{"$skiptoken": true, "$skip": true}
	for _, a := range allowed {
		ok[a] = true
	}
	for _, name := range []string{"$filter", "$orderby", "$search", "$count", "$select", "$expand"} {
		if q.Get(name) == "" {
			continue
		}
		if ok[name] {
			continue
		}
		label := strings.TrimPrefix(name, "$")
		return badRequestf("Parameter '%s' not supported", strings.ToUpper(label[:1])+label[1:])
	}
	return nil
}
