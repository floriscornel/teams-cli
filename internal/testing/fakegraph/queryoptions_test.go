package fakegraph

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// Tests for the query-option gate (queryoptions.go). The gate exists because
// $top=999 on /me/joinedTeams reached a live tenant and answered 400 "Query option
// 'Top' is not allowed" while every layer of the suite stayed green: the OpenAPI
// description declares $top there, and the fake policed only the options the spike
// had seen refused.

// TestJoinedTeamsRejectsTop reproduces the live 400 for the exact request the CLI
// used to send.
func TestJoinedTeamsRejectsTop(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)

	_, err := graph.GetPage[teamWire](context.Background(), c, "/me/joinedTeams", url.Values{"$top": {"999"}})
	if got := errStatus(err); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (err: %v)", got, err)
	}
	if msg := err.Error(); !strings.Contains(msg, "Query option 'Top' is not allowed") {
		t.Errorf("error = %q, want the live wording for a refused query option", msg)
	}

	// The same route without the option works, which is what the client sends now
	// (user-list-joinedteams.md:39: no OData query parameters).
	if _, err := graph.GetPage[teamWire](context.Background(), c, "/me/joinedTeams", nil); err != nil {
		t.Fatalf("GET /me/joinedTeams without options = %v, want success", err)
	}
}

// TestGateKeepsTheSpikeWording pins the second wording: the OData attribute layer
// answers "Parameter 'Filter' not supported" (docs/spike/phase1.md:53), which the
// channel-message route demonstrated, and the gate keeps it for those six options.
func TestGateKeepsTheSpikeWording(t *testing.T) {
	srv := newTestServer(t)
	c := newClient(t, srv)
	_, err := graph.GetPage[messageWire](context.Background(), c, "/teams/t-eng/channels/c-general/messages", url.Values{"$filter": {"startswith(body,'a')"}})
	if got := errStatus(err); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (err: %v)", got, err)
	}
	if msg := err.Error(); !strings.Contains(msg, "Parameter 'Filter' not supported") {
		t.Errorf("error = %q, want the spike wording", msg)
	}
}

// TestEveryRouteDeclaresItsQueryOptions is the mechanism that closes the class of
// bug: a new route must decide, in the table, which query options it documents. A
// route with no row is a test failure rather than a silent yes to everything.
func TestEveryRouteDeclaresItsQueryOptions(t *testing.T) {
	for _, route := range buildRoutes() {
		if route.notAllowed || strings.HasPrefix(route.pattern, "/_") {
			// 405 placeholders and the fake's own pre-authenticated URLs are not
			// api-reference routes.
			continue
		}
		if _, ok := queryOptionsFor(route.method, route.pattern); !ok {
			t.Errorf("route %s has no row in documentedQueryOptions: decide which query options its api-reference page documents", describeRoute(route))
		}
	}
}

// TestChatRoutesShareTheDocumentedOptions guards the normalization: the chat family
// is registered under three prefixes, and every one of them must resolve to the
// single /chats row, with the documented set rather than an empty default.
func TestChatRoutesShareTheDocumentedOptions(t *testing.T) {
	for _, prefix := range chatPrefixes {
		for _, route := range chatRoutes(prefix) {
			if route.notAllowed {
				continue
			}
			options, ok := queryOptionsFor(route.method, route.pattern)
			if !ok {
				t.Errorf("chat route %s has no documented option set", describeRoute(route))
				continue
			}
			want, ok := queryOptionsFor(route.method, strings.TrimPrefix(normalizeChatPattern(route.pattern), ""))
			if !ok || len(want) != len(options) {
				t.Errorf("chat route %s resolved to an unexpected option set %v", describeRoute(route), options)
			}
		}
	}
	// The messages row carries the three options chat-list-messages.md documents,
	// which is what the CLI relies on for --since and --until.
	options, ok := queryOptionsFor(http.MethodGet, "/me/chats/19:x@thread.v2/messages"[0:0]+"/chats/{chat-id}/messages")
	if !ok || len(options) != 3 {
		t.Fatalf("GET /chats/{chat-id}/messages options = %v, want the three documented ones", options)
	}
}
