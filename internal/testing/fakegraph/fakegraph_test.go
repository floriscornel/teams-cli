package fakegraph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers the server lifecycle, the error envelopes for unknown
// routes and wrong methods, and the route-surface helpers the contract test
// relies on.

func TestServerLifecycle(t *testing.T) {
	srv := NewServer(Options{Model: testModel(), Clock: clock.NewFake(testNow)})
	defer srv.Close()

	if !strings.HasSuffix(srv.URL(), DefaultBasePath) {
		t.Fatalf("URL() = %q, want it to end in %q", srv.URL(), DefaultBasePath)
	}
	if srv.Handler() == nil {
		t.Fatal("Handler() returned nil")
	}
	if srv.BaseURL() != srv.URL() {
		t.Fatalf("BaseURL() = %q, URL() = %q", srv.BaseURL(), srv.URL())
	}
	if srv.Me() != "u-me" {
		t.Fatalf("Me() = %q, want u-me", srv.Me())
	}
	if srv.Clock() == nil {
		t.Fatal("Clock() returned nil")
	}
}

func TestNewRequiresTestingT(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New(nil, …) did not panic")
		}
	}()
	New(nil, Options{})
}

func TestNewPanicsOnInvalidSeed(t *testing.T) {
	cases := []struct {
		name  string
		model Model
		want  string
	}{
		{"duplicate user", Model{Users: []User{{ID: "a"}, {ID: "a"}}}, "duplicate user id"},
		{"unknown member", Model{Users: []User{{ID: "a"}}, Chats: []Chat{{ID: "c", Members: []Member{{UserID: "ghost"}}}}}, "unknown user"},
		{"duplicate team", Model{Teams: []Team{{ID: "t"}, {ID: "t"}}}, "duplicate team id"},
		{"unknown author", Model{Teams: []Team{{ID: "t", Channels: []Channel{{ID: "c", Messages: []Message{{ID: "m", AuthorID: "ghost"}}}}}}}, "unknown author"},
		{"duplicate message", Model{Teams: []Team{{ID: "t", Channels: []Channel{{ID: "c", Messages: []Message{{ID: "m"}, {ID: "m"}}}}}}}, "duplicate message id"},
		{"unknown drive parent", Model{Drives: []Drive{{ID: "d", Items: []DriveItem{{ID: "x", ParentID: "nope"}}}}}, "unknown parent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("New did not panic for %s", tc.name)
				}
				if !strings.Contains(strings.ToLower(toString(r)), tc.want) {
					t.Fatalf("panic = %v, want it to mention %q", r, tc.want)
				}
			}()
			NewServer(Options{Model: tc.model})
		})
	}
}

func toString(v any) string { return fmt.Sprint(v) }

func TestUnknownRouteReturnsGraphEnvelope(t *testing.T) {
	srv := newTestServer(t)
	resp, err := srv.Client().Get(srv.URL() + "/nope/at/all")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	var env errorEnvelopeWire
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		t.Fatalf("decode error envelope: %v", err)
	}
	if env.Error.Code != "itemNotFound" {
		t.Fatalf("error.code = %q, want itemNotFound", env.Error.Code)
	}
	if env.Error.Message == "" {
		t.Fatal("error.message is empty; the CLI shows it to the user")
	}
	if env.Error.InnerError == nil || env.Error.InnerError.RequestID == "" {
		t.Fatal("error.innerError.request-id is missing")
	}
}

func TestWrongMethodReturns405(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct {
		method string
		path   string
	}{
		// The documented 405s from the spike: the MCP's standalone
		// hostedContents POST and the /chats form of the chat soft delete
		// (docs/spike/phase1.md:91,99).
		{"POST", "/teams/t-eng/channels/c-general/messages/cm-1/hostedContents"},
		{"POST", "/chats/chat-group/softDelete"},
		{"POST", "/chats/chat-group/messages/group-m0/hostedContents"},
		// A generic method mismatch on a route that exists.
		{"DELETE", "/me"},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			req, err := http.NewRequest(tc.method, srv.URL()+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != MethodNotAllowed {
				t.Fatalf("status = %d, want %d", resp.StatusCode, MethodNotAllowed)
			}
			var env errorEnvelopeWire
			if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if env.Error.Code != "Request_BadRequest" {
				t.Fatalf("error.code = %q, want Request_BadRequest", env.Error.Code)
			}
			if env.Error.Message != "Specified HTTP method is not allowed for the request target." {
				t.Fatalf("message = %q", env.Error.Message)
			}
		})
	}
}

func TestSeedMergesAdditionalState(t *testing.T) {
	srv := newTestServer(t)
	srv.Seed(Model{
		Users: []User{{ID: "u-new", DisplayName: "New Person"}},
		Chats: []Chat{{ID: "chat-new", ChatType: ChatTypeGroup, Members: members("u-me", "u-new")}},
	})
	c := newClient(t, srv)
	resp := mustGet(t, c, "/users/u-new", nil)
	var user struct {
		DisplayName string `json:"displayName"`
	}
	if err := resp.Decode(&user); err != nil {
		t.Fatal(err)
	}
	if user.DisplayName != "New Person" {
		t.Fatalf("displayName = %q", user.DisplayName)
	}
	if _, err := graph.GetPage[map[string]any](context.Background(), c, "/me/chats", nil); err != nil {
		t.Fatalf("chat list after Seed: %v", err)
	}
}

func TestSeedPanicsOnInvalidModel(t *testing.T) {
	srv := newTestServer(t)
	defer func() {
		if recover() == nil {
			t.Fatal("Seed did not panic on an invalid model")
		}
	}()
	srv.Seed(Model{Users: []User{{ID: "x"}, {ID: "x"}}})
}

func TestImplementsAndRoutes(t *testing.T) {
	srv := newTestServer(t)
	if !srv.Implements("GET", "/teams/{team-id}/channels/{channel-id}/messages") {
		t.Error("Implements said no for a route the fake serves")
	}
	if srv.Implements("PUT", "/teams/{team-id}/channels/{channel-id}/messages") {
		t.Error("Implements said yes for a method the fake does not serve")
	}
	if srv.Implements("POST", "/chats/{chat-id}/softDelete") {
		t.Error("Implements said yes for a 405 placeholder")
	}
	routes := srv.Routes()
	if len(routes) < 40 {
		t.Fatalf("Routes() returned %d entries, want the full surface", len(routes))
	}
	if !sort.StringsAreSorted(routes) {
		t.Error("Routes() is not sorted")
	}
	for _, r := range routes {
		if strings.HasPrefix(r, "POST /_") || strings.Contains(r, "/_download/") {
			t.Errorf("Routes() leaked a fake-only route: %s", r)
		}
	}
}

func TestSeedModelDeterministic(t *testing.T) {
	// Two servers built from the same model must serve byte-identical bodies
	// for a collection. The check is on the message list because it is the
	// collection with the most derived ordering.
	body := func() string {
		srv := newTestServer(t)
		c := newClient(t, srv)
		resp := mustGet(t, c, "/teams/t-eng/channels/c-general/messages", url.Values{"$top": {"10"}})
		return string(resp.Body)
	}
	first, second := body(), body()
	if first != second {
		t.Fatalf("seed is not deterministic:\n%s\n%s", first, second)
	}
	if !strings.Contains(first, "cm-1") {
		t.Fatalf("seeded message missing from %s", first)
	}
}

func TestDefaultGrantedScopesCoverEveryPreset(t *testing.T) {
	granted := defaultGrantedScopes()
	if len(granted) == 0 {
		t.Fatal("defaultGrantedScopes is empty")
	}
	for _, scope := range []string{"User.Read", "ChannelMessage.Read.All", "ChatMessage.Send", "Chat.ManageDeletion.All"} {
		if !containsFold(granted, scope) {
			t.Errorf("default grant is missing %s", scope)
		}
	}
}
