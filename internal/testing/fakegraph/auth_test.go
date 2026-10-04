package fakegraph

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/graph"
)

// This file covers the authorization model: the scopes a route needs, the 403
// envelope that names them, the token's `scp` claim as the grant, and the
// admin-consent gate over internal/config.DelegatedAdminConsentScopes.

// clientWithToken builds a client whose token carries the given scopes.
func clientWithToken(t *testing.T, srv *Server, scopes ...string) *graph.Client {
	t.Helper()
	token := Token(scopes...)
	return newClient(t, srv, func(o *graph.Options) {
		o.Token = graph.TokenSourceFunc(func(context.Context) (string, error) { return token, nil })
	})
}

func TestMissingScopeReturns403NamingTheScope(t *testing.T) {
	readOnly, err := config.Scopes.For(config.ScopePresetReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock(), Scopes: readOnly})
	c := newClient(t, srv)

	// read-only lacks ChannelMessage.ReadWrite, which is the scope the spike
	// found necessary for PATCH (docs/spike/phase1.md:27).
	_, err = c.Do(context.Background(), graph.Request{
		Method: http.MethodPatch,
		Path:   "/teams/t-eng/channels/c-general/messages/cm-1",
		Body:   map[string]any{"body": map[string]string{"content": "<p>edited</p>"}},
	})
	var apiErr *graph.APIError
	if !asError(err, &apiErr) {
		t.Fatalf("err = %v, want an APIError", err)
	}
	if apiErr.Status != 403 {
		t.Fatalf("status = %d, want 403", apiErr.Status)
	}
	if apiErr.Code != "Authorization_RequestDenied" {
		t.Fatalf("code = %q, want Authorization_RequestDenied", apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "requires one of") || !strings.Contains(apiErr.Message, "ChannelMessage.ReadWrite") {
		t.Fatalf("message = %q, want it to name the required scope", apiErr.Message)
	}
	if apiErr.ExitCode() != 3 {
		t.Fatalf("ExitCode = %d, want 3 (auth required)", apiErr.ExitCode())
	}
	if hint := apiErr.Hint(); !strings.Contains(hint, "ChannelMessage.ReadWrite") {
		t.Fatalf("Hint = %q, want it to name the scope", hint)
	}

	// A read the preset does cover still works.
	if _, err := graph.GetPage[messageWire](context.Background(), c, "/teams/t-eng/channels/c-general/messages", nil); err != nil {
		t.Fatalf("read-only read failed: %v", err)
	}
}

func TestTokenScopesDriveTheGrant(t *testing.T) {
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock()})
	c := clientWithToken(t, srv, "Chat.Read")

	if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats", nil); err != nil {
		t.Fatalf("GET /me/chats with Chat.Read: %v", err)
	}
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); errStatus(err) != 403 {
		t.Fatalf("GET /me with only Chat.Read: err = %v, want 403", err)
	}
}

func TestConfiguredScopesOverrideTheToken(t *testing.T) {
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock(), Scopes: []string{"User.Read"}})
	c := clientWithToken(t, srv, "Chat.Read")

	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatalf("GET /me with the configured grant: %v", err)
	}
	if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats", nil); errStatus(err) != 403 {
		t.Fatalf("GET /me/chats: err = %v, want 403 because the configured set wins", err)
	}
}

func TestAdminConsentGate(t *testing.T) {
	scopes := []string{"ChannelMessage.Read.All"}

	t.Run("withheld without consent", func(t *testing.T) {
		srv := New(t, Options{
			Model:                testModel(),
			Clock:                newFakeClock(),
			Scopes:               scopes,
			AdminConsentedScopes: []string{},
		})
		c := newClient(t, srv)
		_, err := graph.GetPage[messageWire](context.Background(), c, "/teams/t-eng/channels/c-general/messages", nil)
		var apiErr *graph.APIError
		if !asError(err, &apiErr) || apiErr.Status != 403 {
			t.Fatalf("err = %v, want a 403", err)
		}
		if !strings.Contains(apiErr.Message, "ChannelMessage.Read.All") {
			t.Fatalf("message = %q, want it to name the scope", apiErr.Message)
		}
		if !strings.Contains(apiErr.Message, "admin consent") {
			t.Fatalf("message = %q, want it to explain the missing admin consent", apiErr.Message)
		}
	})

	t.Run("granted once consented", func(t *testing.T) {
		srv := New(t, Options{
			Model:                testModel(),
			Clock:                newFakeClock(),
			Scopes:               scopes,
			AdminConsentedScopes: []string{"ChannelMessage.Read.All"},
		})
		c := newClient(t, srv)
		if _, err := graph.GetPage[messageWire](context.Background(), c, "/teams/t-eng/channels/c-general/messages", nil); err != nil {
			t.Fatalf("GET channel messages after consent: %v", err)
		}
	})

	t.Run("a scope without admin consent needs none", func(t *testing.T) {
		srv := New(t, Options{
			Model:                testModel(),
			Clock:                newFakeClock(),
			Scopes:               []string{"Chat.Read"},
			AdminConsentedScopes: []string{},
		})
		c := newClient(t, srv)
		if _, err := graph.GetPage[chatWire](context.Background(), c, "/me/chats", nil); err != nil {
			t.Fatalf("Chat.Read is not an admin-consent scope, so it must work: %v", err)
		}
	})
}

func TestRequireToken(t *testing.T) {
	srv := New(t, Options{Model: testModel(), Clock: newFakeClock(), RequireToken: true})

	// No Authorization header at all.
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL()+"/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}

	// A token the server can decode.
	c := clientWithToken(t, srv, "User.Read")
	if _, err := c.Do(context.Background(), graph.Request{Method: http.MethodGet, Path: "/me"}); err != nil {
		t.Fatalf("GET /me with a token: %v", err)
	}
}

func TestNeedsAdminConsentMatchesConfig(t *testing.T) {
	for _, scope := range config.DelegatedAdminConsentScopes {
		if !needsAdminConsent(scope) {
			t.Errorf("needsAdminConsent(%q) = false, want true", scope)
		}
	}
	for _, scope := range []string{"User.Read", "Chat.Read", "ChannelMessage.Send"} {
		if needsAdminConsent(scope) {
			t.Errorf("needsAdminConsent(%q) = true, want false", scope)
		}
	}
}

func TestScopeAlternativesCoverEveryRouteScope(t *testing.T) {
	// Every scope a route names must have a documented alternatives list, or
	// the 403 body would not read like Graph's.
	for _, route := range buildRoutes() {
		for _, scope := range route.scopes {
			if _, ok := scopeAlternatives[scope]; !ok {
				t.Errorf("no alternatives documented for route scope %q", scope)
			}
		}
	}
}

func TestDefaultGrantCoversEveryRoute(t *testing.T) {
	srv := newTestServer(t)
	granted := defaultGrantedScopes()
	for _, route := range srv.routes {
		// The scope lists are any-of, so the default grant must satisfy one
		// alternative per route, not every alternative.
		if missing := missingScope(route.scopes, granted); missing != "" {
			t.Errorf("the permissive default grant does not cover %s %s (missing %q)", route.method, route.pattern, missing)
		}
	}
}

func TestTokenEncoding(t *testing.T) {
	tok := TokenFor("u-1", "User.Read", "Chat.Read")
	scopes, ok := scopesFromToken(tok)
	if !ok {
		t.Fatalf("scopesFromToken(%q) failed", tok)
	}
	if strings.Join(scopes, " ") != "User.Read Chat.Read" {
		t.Fatalf("scopes = %v", scopes)
	}
	if _, ok := scopesFromToken("not-a-jwt"); ok {
		t.Fatal("scopesFromToken accepted a token without three parts")
	}
	if _, ok := scopesFromToken("a.!!!.c"); ok {
		t.Fatal("scopesFromToken accepted an undecodable payload")
	}
	if _, ok := scopesFromToken("a." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".c"); ok {
		t.Fatal("scopesFromToken accepted a token without a scp claim")
	}
}
