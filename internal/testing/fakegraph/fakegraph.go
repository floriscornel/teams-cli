// Package fakegraph is a stateful, in-memory Microsoft Graph server for the
// teams CLI test suite. It is an [httptest.Server] plus a seeded store, so a
// test can exercise the real internal/graph client (and later the testscript
// end-to-end scripts) without a tenant.
//
//	srv := fakegraph.New(t, fakegraph.Options{
//	    Model: fakegraph.Model{
//	        Me:    "u-me",
//	        Users: []fakegraph.User{{ID: "u-me", DisplayName: "Me"}, {ID: "u-alice", DisplayName: "Alice"}},
//	        Teams: []fakegraph.Team{{
//	            ID: "t-eng", DisplayName: "Engineering",
//	            Channels: []fakegraph.Channel{{ID: "c-general", DisplayName: "General"}},
//	        }},
//	    },
//	})
//	client, _ := graph.New(graph.Options{BaseURL: srv.URL(), Token: graph.TokenSourceFunc(...)})
//
// # Fidelity
//
// The endpoints, query limits and error shapes mirror the vendored
// api-reference and concepts pages under refs/graph (cited per handler), with
// the live-observed corrections from docs/spike/phase1.md applied wherever the
// two disagree. The places where the spike overrides the docs are marked
// "spike-observed" throughout the package.
//
// In particular:
//
//   - `@odata.nextLink` paging is served on every collection that pages, with
//     the documented caps: `$top` > 50 is a 400 for channel messages, replies
//     and chat messages, and `/me/chats`
//     (refs/graph/api-reference/v1.0/api/channel-list-messages.md:35,
//     chatmessage-list-replies.md:30, chat-list-messages.md:32, chat-list.md:31;
//     docs/spike/phase1.md:52-58);
//   - team and channel member lists default to 100 and cap at 999, and they do
//     page — the spike saw `@odata.nextLink` at `$top=5` where the docs imply
//     no paging (refs/graph/api-reference/v1.0/api/team-list-members.md:30,
//     docs/spike/phase1.md:50);
//   - `$expand=replies` inlines up to 200 replies per root and offers the rest
//     through `replies@odata.nextLink`
//     (refs/graph/api-reference/v1.0/api/channel-list-messages.md:37-38);
//   - `$filter` on channel messages is rejected with 400, never silently
//     ignored (docs/spike/phase1.md:53);
//   - chat message lists implement the documented `$orderby`/`$filter` matrix,
//     including the rule that a `$filter` is ignored unless a matching
//     `$orderby` is present, while `lastModifiedDateTime gt` works either way
//     and a default listing is deliberately unsorted
//     (refs/graph/api-reference/v1.0/api/chat-list-messages.md:33-34;
//     docs/spike/phase1.md:60-65);
//   - `POST /search/query` pages by `from`/`size` with no `@odata.nextLink`,
//     reports the full match count in `total`, honours `size` up to 50 for
//     chatMessage, returns 500 for the documented-but-broken `IsRead` term and
//     never returns message bodies (docs/spike/phase1.md:75-83);
//   - `/chats/{id}/softDelete` and a standalone hostedContents POST answer 405,
//     because that is what the live service does
//     (docs/spike/phase1.md:91,99; PLAN.md "Chat quirks");
//   - `$batch` enforces the documented maximum of 20, unique ids and the rule
//     that sub-request failures travel inside a 200 outer response
//     (refs/graph/concepts/json-batching.md:17,48,119).
//
// # Authorization
//
// Every route declares the delegated scope it needs. The grant comes from
// [Options.Scopes] or, when that is nil, from the `scp` claim of the bearer
// token the request carries (the spike confirmed `scp` lists every
// admin-consented scope, docs/spike/phase1.md:17). A missing scope is a 403
// `Authorization_RequestDenied` whose message names the scope, so the CLI's
// scope pre-check and its 403 handling are both testable. Scopes that
// config.DelegatedAdminConsentScopes marks as needing admin consent are only
// granted when [Options.AdminConsentedScopes] says they were consented.
package fakegraph

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/clock"
)

// Defaults for [Options].
const (
	// DefaultBasePath is the Graph service root's version segment.
	DefaultBasePath = "/v1.0"
	// DefaultSearchPageSize is the documented default `size` of a search
	// request (refs/graph/api-reference/v1.0/resources/search-api-overview.md:63).
	DefaultSearchPageSize = 25
	// DefaultMaxBodyBytes caps how much of a request body the fake buffers.
	DefaultMaxBodyBytes = 8 << 20
)

// Options configures a fake Graph server.
type Options struct {
	// Model is the seed. It is deterministic by construction.
	Model Model
	// Clock supplies timestamps for objects created through the API
	// (messages, chats, reactions, read state). Seed data never uses it, so a
	// test is reproducible as long as it injects a frozen clock here.
	Clock clock.Clock
	// TenantID is stamped on teams, channels, members and chats.
	TenantID string
	// BasePath is the service root's version segment; it defaults to
	// [DefaultBasePath] and must start with "/".
	BasePath string
	// Scopes, when non-nil, is the authoritative scope grant. When nil, the
	// grant is read from the bearer token's `scp` claim; with no token either,
	// it defaults to every scope in every config preset plus the incremental
	// scopes, so a test that does not care about authorization needs no setup.
	Scopes []string
	// AdminConsentedScopes gates config.DelegatedAdminConsentScopes: only the
	// listed scopes are granted. When nil, every admin-consent scope is
	// treated as consented (the permissive default described in the package
	// doc); pass a list to exercise the consent failure path.
	AdminConsentedScopes []string
	// RequireToken makes a missing or undecodable bearer token a 401. The
	// default is permissive, because most tests do not exercise
	// authentication.
	RequireToken bool
	// Faults inject failures per route and call index.
	Faults []Fault
	// SearchPageSize overrides the default search page size.
	SearchPageSize int
	// ChatMembersExpandCap caps the members an `$expand=members` chat list
	// returns. The docs give 25; the spike saw 116 members with no cap, so the
	// default is 0 (no cap) and the documented behaviour is one option away
	// (refs/graph/api-reference/v1.0/api/chat-list.md:31,
	// docs/spike/phase1.md:59).
	ChatMembersExpandCap int
	// MaxBodyBytes caps request-body buffering; it defaults to
	// [DefaultMaxBodyBytes].
	MaxBodyBytes int64
	// Contract, when set, validates every request and response against the
	// Layer 6 trimmed Graph OpenAPI description. Use [WithContract] to build
	// it; see contract.go.
	Contract *ContractHook
}

func (o Options) withDefaults() Options {
	if o.TenantID == "" {
		o.TenantID = DefaultTenantID
	}
	if o.BasePath == "" {
		o.BasePath = DefaultBasePath
	}
	if o.SearchPageSize == 0 {
		o.SearchPageSize = DefaultSearchPageSize
	}
	if o.MaxBodyBytes == 0 {
		o.MaxBodyBytes = DefaultMaxBodyBytes
	}
	return o
}

func (o Options) validate() error {
	if !strings.HasPrefix(o.BasePath, "/") {
		return fmt.Errorf("fakegraph: BasePath %q must start with %q", o.BasePath, "/")
	}
	if o.SearchPageSize < 1 {
		return fmt.Errorf("fakegraph: SearchPageSize must be positive, got %d", o.SearchPageSize)
	}
	if err := o.Model.validate(); err != nil {
		return err
	}
	for i, f := range o.Faults {
		if err := f.validate(); err != nil {
			return fmt.Errorf("fakegraph: fault %d: %w", i, err)
		}
	}
	return nil
}

// Server is a running fake Graph server. Create it with [New] (which closes it
// with t.Cleanup) or [NewServer] (which the caller closes).
type Server struct {
	opts Options
	ts   *httptest.Server
	url  string
	// origin is the httptest URL without the version segment; batch
	// sub-requests are rebuilt from it.
	origin string
	clk    clock.Clock
	st     *store
	routes []routeDef

	recMu   sync.Mutex
	records []*RecordedRequest
	reqSeq  int64

	faultMu    sync.Mutex
	faultCalls []int

	searchMu     sync.Mutex
	seenSearches map[string]bool
}

// New starts a fake Graph server for a test and closes it with t.Cleanup.
func New(t *testing.T, opts Options) *Server {
	if t == nil {
		panic("fakegraph: New requires a non-nil *testing.T; use NewServer for a server you close yourself")
	}
	s := NewServer(opts)
	t.Cleanup(s.Close)
	return s
}

// NewServer starts a fake Graph server. The caller must call Close.
func NewServer(opts Options) *Server {
	opts = opts.withDefaults()
	if err := opts.validate(); err != nil {
		panic(err)
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.New()
	}
	s := &Server{
		opts:         opts,
		clk:          clk,
		st:           newStore(opts.Model, opts.TenantID, opts.ChatMembersExpandCap),
		seenSearches: map[string]bool{},
	}
	s.faultCalls = make([]int, len(opts.Faults))
	s.routes = buildRoutes()
	// Bind the listener before the server can serve anything.
	//
	// httptest.NewServer starts accepting as soon as it returns, so assigning the
	// URL fields afterwards left a window in which a handler could read them
	// while they were being written: -race caught exactly that in the testscript
	// suite, where every script builds its own server and a keep-alive
	// connection inherited from a just-closed server on the same port can arrive
	// mid-construction. A request in that window would build URLs without the
	// version prefix (or none at all). Binding first means the address is known,
	// so every field is set before the server serves its first request.
	// ListenConfig with a context, as the linter requires; the bind has no
	// deadline of its own.
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		panic(fmt.Sprintf("fakegraph: listen: %v", err))
	}
	s.origin = "http://" + ln.Addr().String()
	s.url = s.origin + opts.BasePath
	ts := httptest.NewUnstartedServer(s)
	// NewUnstartedServer opens its own listener; the one bound above replaces it,
	// so the advertised URL is the address that actually serves.
	_ = ts.Listener.Close()
	ts.Listener = ln
	s.ts = ts
	ts.Start()
	return s
}

// Close shuts the server down. [New] registers it with t.Cleanup.
func (s *Server) Close() { s.ts.Close() }

// URL is the Graph service root including the version segment, for example
// http://127.0.0.1:41234/v1.0. Pass it to graph.Options.BaseURL.
func (s *Server) URL() string { return s.url }

// BaseURL is an alias for URL.
func (s *Server) BaseURL() string { return s.url }

// Handler returns the underlying http.Handler so the dev server and the
// testscript parent process can mount it.
func (s *Server) Handler() http.Handler { return s }

// Client returns the HTTP client the underlying httptest server suggests. It
// is the default client; it exists so callers do not need to know the fake is
// an httptest.Server.
func (s *Server) Client() *http.Client { return s.ts.Client() }

// Clock returns the injected clock.
func (s *Server) Clock() clock.Clock { return s.clk }

// Me returns the signed-in user's id.
func (s *Server) Me() string {
	s.st.mu.RLock()
	defer s.st.mu.RUnlock()
	return s.st.me
}

// Seed merges more state into the running server. It panics on an invalid
// model, because a broken seed is a test bug.
func (s *Server) Seed(m Model) {
	s.st.mu.RLock()
	known := s.st.userIDsLocked()
	s.st.mu.RUnlock()
	if err := m.validateWith(known); err != nil {
		panic(err)
	}
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	s.st.seedLocked(m)
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.serve(w, r, false)
}

// serve handles one request. sub marks a $batch sub-request, which is recorded
// as such but otherwise follows the same path.
func (s *Server) serve(w http.ResponseWriter, r *http.Request, sub bool) {
	rel := strings.TrimPrefix(r.URL.Path, s.opts.BasePath)
	if rel == "" {
		rel = "/"
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, s.opts.MaxBodyBytes))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "BadRequest", "The request body could not be read.")
		return
	}
	rec := s.recordStart(r, rel, body, sub)
	sw := newStatusWriter(w)
	defer func() {
		rec.Status = sw.statusCode()
		rec.Response = sw.bodyBytes()
		s.validateContract(r.Method, rel, r.URL.Query(), body, rec.Status, rec.Response)
	}()

	if handled := s.applyFault(sw, r, rel); handled {
		return
	}

	routes, params := matchRoutes(s.routes, r.Method, rel)
	switch len(routes) {
	case 0:
		if routePatternMatches(s.routes, rel) {
			// The path is served, but not with this method. Graph answers 405
			// with Request_BadRequest and this exact message
			// (refs/graph/concepts/json-batching.md:238).
			writeError(sw, r, http.StatusMethodNotAllowed, "Request_BadRequest",
				"Specified HTTP method is not allowed for the request target.")
			return
		}
		writeError(sw, r, http.StatusNotFound, "itemNotFound", "The resource could not be found.")
		return
	case 1:
	default:
		// Two patterns can only match when the route table is ambiguous; that
		// is a bug in this package, so fail loudly rather than guessing.
		panic(fmt.Sprintf("fakegraph: ambiguous route for %s %s (%d matches)", r.Method, rel, len(routes)))
	}
	route := routes[0]
	if route.notAllowed {
		writeError(sw, r, http.StatusMethodNotAllowed, "Request_BadRequest",
			"Specified HTTP method is not allowed for the request target.")
		return
	}

	granted, authenticated := s.grantedScopes(r)
	if s.opts.RequireToken && !authenticated {
		writeError(sw, r, http.StatusUnauthorized, "InvalidAuthenticationToken", "Access token is empty.")
		return
	}
	if err := rejectUndeclaredQueryOptions(*route, r.URL.Query()); err != nil {
		writeError(sw, r, err.Status, err.Code, err.Message)
		return
	}
	if missing := missingScope(route.scopes, granted); missing != "" {
		writeScopeError(sw, r, missing, s.scopeIsAdminOnly(missing))
		return
	}

	ctx := &handlerCtx{
		s:      s,
		w:      sw,
		r:      r,
		rel:    rel,
		params: params,
		query:  r.URL.Query(),
		body:   body,
		rec:    rec,
	}
	route.fn(ctx)
}

// scopeIsAdminOnly reports whether a scope needs admin consent and was not
// consented for this server.
func (s *Server) scopeIsAdminOnly(scope string) bool {
	if !needsAdminConsent(scope) {
		return false
	}
	return !containsFold(s.consentedAdminScopes(), scope)
}

// consentedAdminScopes returns the admin-consented set; a nil option means the
// permissive default (everything consented).
func (s *Server) consentedAdminScopes() []string {
	if s.opts.AdminConsentedScopes != nil {
		return s.opts.AdminConsentedScopes
	}
	return adminConsentScopes()
}

// grantedScopes decides the token's grant. Precedence is the configured set,
// then the token's scp claim, then the permissive default; scopes that need
// admin consent and were not consented are then withheld.
func (s *Server) grantedScopes(r *http.Request) ([]string, bool) {
	var (
		granted       []string
		authenticated bool
	)
	switch {
	case s.opts.Scopes != nil:
		granted = s.opts.Scopes
		authenticated = true
	case bearerToken(r) != "":
		if scopes, ok := scopesFromToken(bearerToken(r)); ok {
			granted = scopes
			authenticated = true
		} else {
			granted = defaultGrantedScopes()
		}
	default:
		granted = defaultGrantedScopes()
	}
	return s.withholdUnconsented(granted), authenticated
}

func (s *Server) withholdUnconsented(granted []string) []string {
	if s.opts.AdminConsentedScopes == nil {
		return granted
	}
	consented := s.opts.AdminConsentedScopes
	out := make([]string, 0, len(granted))
	for _, g := range granted {
		if needsAdminConsent(g) && !containsFold(consented, g) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// bearerToken extracts the Authorization bearer value, or "".
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

// scopesFromToken decodes the scp claim of a JWT-ish token. The spike
// confirmed that scp carries every admin-consented scope, whichever subset the
// client asked for (docs/spike/phase1.md:17).
func scopesFromToken(token string) ([]string, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Tolerate standard base64 padding.
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, false
		}
	}
	var claims struct {
		Scp string `json:"scp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	if strings.TrimSpace(claims.Scp) == "" {
		return nil, false
	}
	return strings.Fields(claims.Scp), true
}

// Token mints a JWT-ish access token carrying the given scopes in scp. It
// needs no signature: the fake decodes the payload and nothing verifies it,
// which is also how MSAL treats the tokens from fakeidp.
func Token(scopes ...string) string { return TokenFor(DefaultMe, scopes...) }

// TokenFor mints a token for a user id. The id is informational; the fake
// treats the request as the configured signed-in user.
func TokenFor(userID string, scopes ...string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"typ":"JWT","alg":"none"}`))
	claims, _ := json.Marshal(map[string]any{
		"sub": userID,
		"scp": strings.Join(scopes, " "),
	})
	payload := base64.RawURLEncoding.EncodeToString(claims)
	return header + "." + payload + "."
}

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("OData-Version", "4.0")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// writeNoContent writes the 204 Graph returns from softDelete, PATCH and the
// reaction actions.
func writeNoContent(w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) }

// writeRaw writes a body without touching its bytes (malformed-JSON faults).
func writeRaw(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// writeError writes a Graph error envelope. requestID echoes the
// client-request-id header the internal/graph client sends, because Graph does
// (refs/openapi/openapi/v1.0/openapi.yaml:941262-941276).
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	w.Header().Set("request-id", newRequestID())
	writeJSON(w, status, errorEnvelopeWire{Error: errorBodyWire{
		Code:    code,
		Message: message,
		InnerError: &innerErrorWire{
			Date:            time.Now().UTC().Format(graphTimeFormat),
			RequestID:       newRequestID(),
			ClientRequestID: r.Header.Get("client-request-id"),
			Status:          fmt.Sprint(status),
		},
	}})
}

// writeScopeError writes the 403 the scope model produces. The message names
// the required scope and keeps Graph's "API requires one of '…'" wording, so
// graph.APIError.Hint can extract it (internal/graph/errors.go:117).
func writeScopeError(w http.ResponseWriter, r *http.Request, scope string, adminOnly bool) {
	alternatives := scopeAlternativesOf(scope)
	message := fmt.Sprintf("API requires one of '%s'.", strings.Join(alternatives, ", "))
	if adminOnly {
		message += " The scope requires admin consent, and no admin has consented for this app."
	}
	writeError(w, r, http.StatusForbidden, "Authorization_RequestDenied", message)
}

// errNotImplemented is returned by helpers that must never be reached.
var errNotImplemented = errors.New("fakegraph: not implemented")

// newRequestID returns a per-request correlation id. It is not random: tests
// assert on recorded traffic, and a deterministic id keeps output stable.
func newRequestID() string { return "fakegraph-request" }
