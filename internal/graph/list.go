package graph

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// This file holds the request-building rules every endpoint wrapper shares:
// paging, the documented $top ceilings, OData escaping and timestamp
// formatting. The ceilings come from the api-reference pages cited in PLAN.md
// "Layer 1" and are enforced by fakegraph as well, so a regression fails a test
// rather than a tenant.

// Documented pagination limits. Channel messages and replies cap $top at 50,
// chat listings at 50, member listings at 999, and a chatMessage search page at
// 50 for the live service (the docs cap mail and events at 25).
//
// Refs: refs/graph/api-reference/v1.0/api/channel-list-messages.md:33,
// chatmessage-list-replies.md:41, chat-list-messages.md:47, chat-list.md:63,
// team-list-members.md:45, docs/spike/phase1.md:75.
const (
	DefaultTopMessages = 20
	MaxTopMessages     = 50
	MaxTopChats        = 50
	MaxTopMembers      = 999
	DefaultTopMembers  = 100
	MaxTopSearch       = 50
	DefaultTopSearch   = 25
	MaxTopPeople       = 999
	DefaultTopPeople   = 25
)

// ListAll pages a collection and returns at most limit items, following
// @odata.nextLink as the docs require ("Follow pagination everywhere",
// refs/graph/concepts/paging.md:91). A limit of 0 means every item.
//
// The result keeps whatever order Graph returned; callers that render a listing
// sort it themselves, because PLAN.md records that the default order of message
// listings is neither created- nor modified-sorted.
func ListAll[T any](ctx context.Context, c *Client, path string, q url.Values, limit int, opts ...RequestOption) ([]T, error) {
	var out []T
	err := EachPage[T](ctx, c, path, q, func(page Page[T]) (bool, error) {
		out = append(out, page.Value...)
		if limit > 0 && len(out) >= limit {
			out = out[:limit]
			return false, nil
		}
		return true, nil
	}, opts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// topValue clamps a requested $top into the documented range. A zero request
// takes the documented default.
func topValue(want, def, ceiling int) int {
	if want <= 0 {
		want = def
	}
	if want > ceiling {
		return ceiling
	}
	return want
}

// segment escapes one path segment. Graph ids are URL-safe apart from the
// separators a chat or channel id carries (19:...@thread.tacv2), which
// url.PathEscape leaves alone but never mistakes for a path separator.
func segment(s string) string { return url.PathEscape(s) }

// ODataQuote quotes a string literal for an OData $filter. The docs require a
// single quote inside a literal to be doubled, which the MCP never does
// (refs/graph/concepts/query-parameters.md, "Filter parameter"; PLAN.md
// "Escape ' in OData $filter user search by doubling it").
func ODataQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// odataTime renders a timestamp the way a $filter comparison expects.
func odataTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// intParam renders an int for a query value.
func intParam(n int) string { return strconv.Itoa(n) }
