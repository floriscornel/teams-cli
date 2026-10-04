// Package fakeidp is an in-process fake Microsoft identity platform (Microsoft Entra ID) that
// MSAL Go can talk to: a TLS httptest server that serves the OIDC metadata document, the
// device-code endpoint, the token endpoint (device code, auth code + PKCE and refresh grants) and
// the JWKS document, plus helpers that drive the interactive auth-code flow headlessly.
//
// Wire it into MSAL like this:
//
//	srv := fakeidp.New(t, fakeidp.Options{Scopes: []string{"User.Read"}})
//	client, err := public.New("client-id",
//		public.WithAuthority(srv.Authority()),
//		public.WithHTTPClient(srv.HTTPClient()),
//		public.WithInstanceDiscovery(false))
//
// Four MSAL-shaped requirements drive the server's shape:
//
//   - The authority must be an https URL with a tenant path segment, otherwise MSAL refuses it
//     (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:529-536). [Server.Authority]
//     therefore returns https://127.0.0.1:<port>/<tenant>.
//   - MSAL resolves the endpoints by GETting {authority}/v2.0/.well-known/openid-configuration
//     (refs/msal-go/apps/internal/oauth/resolvers.go:177) and requires authorization_endpoint,
//     token_endpoint and an issuer whose host matches the authority
//     (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:104-162).
//   - MSAL derives the device-code endpoint from the token endpoint with
//     strings.ReplaceAll(tokenEndpoint, "token", "devicecode")
//     (refs/msal-go/apps/internal/oauth/ops/accesstokens/accesstokens.go:390), which is why the
//     token endpoint is /<tenant>/oauth2/v2.0/token and its device-code sibling is
//     /<tenant>/oauth2/v2.0/devicecode. Because that substitution runs over the whole URL,
//     [Options.Tenant] must not contain the substring "token"; [NewServer] panics when it does.
//   - MSAL reads expires_in (a response with neither expires_in nor expires_on is invalid,
//     refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:238-245) and passes the
//     device-code message through verbatim
//     (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:378-395).
//
// The tokens are unsigned JWTs whose header is {"typ":"JWT","alg":"none"}, which is what the
// device-code sample in the Entra docs returns
// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:109). None of the flows here needs
// a signature: MSAL decodes the ID token payload and the client_info field but never verifies a
// signature (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:48-81,120-148). The JWKS
// document is served for completeness and publishes no keys, because nothing is signed.
package fakeidp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Defaults for [Options]. They are exported so tests can assert against them.
const (
	// DefaultTenant is the tenant path segment of the authority URL.
	DefaultTenant = "contoso.onmicrosoft.com"
	// DefaultTenantID is the tid claim and the utid half of client_info.
	DefaultTenantID = "11111111-1111-1111-1111-111111111111"
	// DefaultAudience is the Microsoft Graph app ID, the aud claim the teams CLI validates.
	DefaultAudience = "00000003-0000-0000-c000-000000000000"
	// DefaultScope is the consented scope set when Options.Scopes is empty.
	DefaultScope = "User.Read"
	// DefaultDeviceCodePollCount is the number of authorization_pending polls before success.
	DefaultDeviceCodePollCount = 2
	// DefaultDeviceCodeExpiresIn is the device code's lifetime in seconds (the real service
	// returns 900; refs/entra/docs/identity-platform/v2-oauth2-device-code.md:58).
	DefaultDeviceCodeExpiresIn = 900
	// DefaultDeviceCodeInterval is the polling interval the real service advertises. MSAL ignores
	// it: the poll loop hard-codes 50 ms doubling to a 5 s cap
	// (refs/msal-go/apps/internal/oauth/oauth.go:277-296).
	DefaultDeviceCodeInterval = 5
	// DefaultAccountObjectID is the oid claim and the uid half of client_info.
	DefaultAccountObjectID = "22222222-2222-2222-2222-222222222222"
	// DefaultAccountSubject is the sub claim.
	DefaultAccountSubject = "33333333-3333-3333-3333-333333333333"
	// DefaultUserCode is the code a human would type at the verification URI.
	DefaultUserCode = "ABCD-EFGH"
	// DefaultVerificationURI is where the device-code message sends the user.
	DefaultVerificationURI = "https://microsoft.com/devicelogin"
	// DefaultExpiresIn is the access-token lifetime in seconds.
	DefaultExpiresIn = 3600
	// DefaultAuthCodeLifetime is how long a minted authorization code stays redeemable.
	DefaultAuthCodeLifetime = 10 * time.Minute
)

// Routes under the tenant path segment. They are split out because [Server.ServeHTTP] routes on
// them and [Server.Endpoints] builds the advertised URLs from them, so the two cannot drift.
const (
	pathOpenIDConfiguration = "/v2.0/.well-known/openid-configuration"
	pathJWKS                = "/discovery/v2.0/keys"
	pathToken               = "/oauth2/v2.0/token"
	pathDeviceCode          = "/oauth2/v2.0/devicecode"
)

// The token endpoint's grant types.
const (
	// grantTypeDeviceCode is the spelling the protocol mandates
	// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:83).
	grantTypeDeviceCode = "urn:ietf:params:oauth:grant-type:device_code"
	// grantTypeDeviceCodeShort is the spelling MSAL Go actually sends: its grant package says
	// "device_code", not the RFC 8628 URN
	// (refs/msal-go/apps/internal/oauth/ops/internal/grant/grant.go:22). The fake accepts both,
	// because a client written against the documentation and MSAL's own client both have to work
	// against it.
	grantTypeDeviceCodeShort = "device_code"
	grantTypeAuthCode        = "authorization_code"
	grantTypeRefresh         = "refresh_token"
)

// Account is the user the fake signs in.
type Account struct {
	// PreferredUsername is the preferred_username claim of the ID token, which MSAL surfaces as
	// Account.PreferredUsername (refs/msal-go/apps/internal/base/storage/storage.go:226-238).
	PreferredUsername string
	// Name is the name claim of the ID token.
	Name string
	// ObjectID is the oid claim, MSAL's local account ID
	// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:101-107).
	ObjectID string
	// Subject is the sub claim.
	Subject string
	// UID and UTID are the client_info halves MSAL joins into a HomeAccountID as "uid.utid"
	// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:262-271).
	UID, UTID string
}

// Options configures a [Server]. The zero value is valid: [NewServer] fills in the defaults.
type Options struct {
	// Tenant is the tenant path segment of the authority URL. It must be lowercase (MSAL
	// lowercases the whole authority, refs/msal-go/.../authority/authority.go:524) and must not
	// contain the substring "token", or MSAL's device-code URL substitution corrupts the path
	// (refs/msal-go/apps/internal/oauth/ops/accesstokens/accesstokens.go:390).
	Tenant string
	// TenantID is the tid claim and the utid half of the client_info field. The issuer in the
	// metadata document is built from it, matching the shape of a real v2.0 issuer
	// "https://<host>/<tenant-id>/v2.0".
	TenantID string
	// ClientID is the application ID the server expects. When empty, any client_id is accepted.
	// It is what the access token's appid claim reports.
	ClientID string
	// Account is the user the flows sign in.
	Account Account
	// Scopes is the consented delegated scope set: it becomes the access token's scp claim, which
	// the resource should authorize against
	// (refs/entra/docs/identity-platform/access-token-claims-reference.md:59). The scope field of
	// the token response instead echoes what the client requested, so MSAL never reports declined
	// scopes (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:249-259).
	Scopes []string
	// Audience is the aud claim of the access token. Defaults to the Graph app ID.
	Audience string

	// DeviceCodePollCount is how many authorization_pending responses the device-code grant
	// returns before it succeeds (the default is 2). Keep it small: MSAL sleeps 50 ms, then
	// doubles, capped at 5 s (refs/msal-go/apps/internal/oauth/oauth.go:277-296).
	DeviceCodePollCount int
	// SlowDownPolls is how many slow_down responses follow the pending ones (the default is 0).
	// MSAL retries slow_down exactly like authorization_pending
	// (refs/msal-go/apps/internal/oauth/oauth.go:302-328).
	SlowDownPolls int
	// DeviceCodeExpiresIn is the device code's lifetime in seconds.
	DeviceCodeExpiresIn int
	// DeviceCodeInterval is the interval the device-code response advertises. MSAL ignores it.
	DeviceCodeInterval int
	// UserCode is the code shown to the user. The default message embeds it.
	UserCode string
	// DeviceCodeMessage is the message the device-code response carries. MSAL passes it through
	// verbatim (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:378-395).
	DeviceCodeMessage string
	// VerificationURI is the verification_uri of the device-code response.
	VerificationURI string

	// ExpiresIn is the access-token lifetime in seconds, served as expires_in.
	ExpiresIn int

	// Errors injects error responses.
	Errors Errors
}

// withDefaults returns a copy of o with every zero field replaced by its default.
func (o Options) withDefaults() Options {
	if o.Tenant == "" {
		o.Tenant = DefaultTenant
	}
	o.Tenant = strings.ToLower(o.Tenant)
	if o.TenantID == "" {
		o.TenantID = DefaultTenantID
	}
	if o.Account.PreferredUsername == "" {
		o.Account.PreferredUsername = "alice@" + o.Tenant
	}
	if o.Account.Name == "" {
		o.Account.Name = "Alice Example"
	}
	if o.Account.ObjectID == "" {
		o.Account.ObjectID = DefaultAccountObjectID
	}
	if o.Account.Subject == "" {
		o.Account.Subject = DefaultAccountSubject
	}
	if o.Account.UID == "" {
		o.Account.UID = o.Account.ObjectID
	}
	if o.Account.UTID == "" {
		o.Account.UTID = o.TenantID
	}
	if len(o.Scopes) == 0 {
		o.Scopes = []string{DefaultScope}
	}
	o.Scopes = append([]string(nil), o.Scopes...)
	if o.Audience == "" {
		o.Audience = DefaultAudience
	}
	if o.DeviceCodePollCount == 0 {
		o.DeviceCodePollCount = DefaultDeviceCodePollCount
	}
	if o.DeviceCodeExpiresIn == 0 {
		o.DeviceCodeExpiresIn = DefaultDeviceCodeExpiresIn
	}
	if o.DeviceCodeInterval == 0 {
		o.DeviceCodeInterval = DefaultDeviceCodeInterval
	}
	if o.UserCode == "" {
		o.UserCode = DefaultUserCode
	}
	if o.VerificationURI == "" {
		o.VerificationURI = DefaultVerificationURI
	}
	if o.DeviceCodeMessage == "" {
		o.DeviceCodeMessage = fmt.Sprintf(
			"To sign in, use a web browser to open the page %s and enter the code %s to authenticate.",
			o.VerificationURI, o.UserCode)
	}
	if o.ExpiresIn == 0 {
		o.ExpiresIn = DefaultExpiresIn
	}
	return o
}

// Endpoints holds the URLs the server advertises.
type Endpoints struct {
	// OpenIDConfiguration is the document MSAL GETs to resolve the other endpoints
	// (refs/msal-go/apps/internal/oauth/resolvers.go:177).
	OpenIDConfiguration string
	// Authorization is the authorization_endpoint, which MSAL only strings together into the
	// browser URL; it never calls it (refs/msal-go/apps/internal/base/base.go:287-332).
	Authorization string
	// Token is the token_endpoint.
	Token string
	// DeviceCode is the device_authorization_endpoint, i.e. Token with "token" replaced by
	// "devicecode" (refs/msal-go/.../accesstokens/accesstokens.go:390).
	DeviceCode string
	// JWKS is the jwks_uri.
	JWKS string
	// Issuer is the issuer, whose host must match the authority host
	// (refs/msal-go/.../authority/authority.go:119-162).
	Issuer string
}

// Server is a running fake identity provider. Create it with [New] in a test.
type Server struct {
	opts Options

	ts *httptest.Server

	mu sync.Mutex
	// bookkeeping
	deviceCodes     map[string]*deviceCode
	authCodes       map[string]*authCode
	activeRefresh   map[string]bool
	issuedRefresh   []string
	consumedRefresh []string
	seq             int
	tokenSeq        int
	tokenRequests   int
	deviceAuthReqs  int
	deviceCodePolls int
	lastAuthRequest *AuthorizationRequest
	correlationID   string
}

// SetErrors replaces the injected error responses. Handlers read the current set
// on every request, so a test can start with a healthy flow and then break it
// (a revoked refresh token, for example) the way an admin action would.
func (s *Server) SetErrors(e Errors) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.opts.Errors = e
}

// deviceCode is a minted device code and its polling state.
type deviceCode struct {
	expiresAt time.Time
	polls     int
	redeemed  bool
}

// authCode is a minted authorization code, bound to the PKCE challenge and the redirect URI it
// was issued for.
type authCode struct {
	clientID    string
	redirectURI string
	challenge   string
	method      string
	expiresAt   time.Time
	redeemed    bool
}

// New starts a TLS fake identity provider for a test and closes it with t.Cleanup.
func New(t *testing.T, opts Options) *Server {
	if t == nil {
		panic("fakeidp: New requires a non-nil *testing.T; use NewServer for a server you close yourself")
	}
	s := NewServer(opts)
	t.Cleanup(s.Close)
	return s
}

// NewServer starts a TLS fake identity provider. The caller must call Close.
func NewServer(opts Options) *Server {
	opts = opts.withDefaults()
	if strings.Contains(opts.Tenant, "token") {
		// MSAL replaces "token" with "devicecode" in the whole token endpoint URL
		// (refs/msal-go/apps/internal/oauth/ops/accesstokens/accesstokens.go:390), so a tenant
		// containing "token" would rewrite the path as well.
		panic(fmt.Sprintf("fakeidp: tenant %q must not contain the substring %q", opts.Tenant, "token"))
	}
	s := &Server{
		opts:          opts,
		deviceCodes:   map[string]*deviceCode{},
		authCodes:     map[string]*authCode{},
		activeRefresh: map[string]bool{},
		correlationID: "fakeidp-correlation-id",
	}
	s.ts = httptest.NewTLSServer(s)
	return s
}

// Close shuts the server down. [New] registers it with t.Cleanup, so tests rarely call it.
func (s *Server) Close() {
	s.ts.Close()
}

// URL is the server's base URL, for example https://127.0.0.1:41234.
func (s *Server) URL() string { return s.ts.URL }

// Authority is the value to pass to public.WithAuthority: the base URL plus the tenant path
// segment, for example https://127.0.0.1:41234/contoso.onmicrosoft.com. MSAL requires an https
// URL with a tenant path segment (refs/msal-go/.../authority/authority.go:529-536).
func (s *Server) Authority() string { return s.URL() + "/" + s.opts.Tenant }

// Tenant is the tenant path segment of the authority URL.
func (s *Server) Tenant() string { return s.opts.Tenant }

// HTTPClient is the client that trusts this server's TLS certificate; pass it to
// public.WithHTTPClient.
func (s *Server) HTTPClient() *http.Client { return s.ts.Client() }

// Endpoints returns the URLs the server advertises, so tests can build raw requests without
// re-deriving them from MSAL's rules.
func (s *Server) Endpoints() Endpoints {
	base := s.URL() + "/" + s.opts.Tenant
	return Endpoints{
		OpenIDConfiguration: base + pathOpenIDConfiguration,
		Authorization:       base + "/oauth2/v2.0/authorize",
		Token:               base + pathToken,
		DeviceCode:          base + pathDeviceCode,
		JWKS:                base + pathJWKS,
		Issuer:              s.URL() + "/" + s.opts.TenantID + "/v2.0",
	}
}

// TenantID is the tid claim value.
func (s *Server) TenantID() string { return s.opts.TenantID }

// TokenRequests is how many requests the token endpoint has answered. Use it to prove a silent
// acquisition was served entirely from the cache.
func (s *Server) TokenRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tokenRequests
}

// DeviceAuthorizationRequests is how many device codes the device-code endpoint has issued.
func (s *Server) DeviceAuthorizationRequests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deviceAuthReqs
}

// DeviceCodePolls is how many device-code grant requests the token endpoint has seen.
func (s *Server) DeviceCodePolls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deviceCodePolls
}

// IssuedRefreshTokens returns every refresh token the server has minted, in issue order. A client
// may only redeem the last one: every redemption rotates
// (refs/entra/docs/identity-platform/refresh-tokens.md:28).
func (s *Server) IssuedRefreshTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.issuedRefresh...)
}

// ConsumedRefreshTokens returns the refresh tokens that were successfully redeemed, in redemption
// order, each of which is now invalid. The sequence proves a client wrote a rotated token back to
// its cache.
func (s *Server) ConsumedRefreshTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.consumedRefresh...)
}

// ActiveRefreshTokens returns the refresh tokens that are still redeemable, sorted.
func (s *Server) ActiveRefreshTokens() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	tokens := make([]string, 0, len(s.activeRefresh))
	for token := range s.activeRefresh {
		tokens = append(tokens, token)
	}
	sort.Strings(tokens)
	return tokens
}

func (s *Server) serveOpenIDConfiguration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, methodNotAllowed("GET"))
		return
	}
	eps := s.Endpoints()
	s.writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                 eps.Issuer,
		"authorization_endpoint": eps.Authorization,
		"token_endpoint":         eps.Token,
		// MSAL never reads this field (it derives the device-code endpoint from the token
		// endpoint, refs/msal-go/.../accesstokens/accesstokens.go:390), but the real document
		// carries it (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:56-60).
		"device_authorization_endpoint": eps.DeviceCode,
		"jwks_uri":                      eps.JWKS,
		"response_modes_supported":      []string{"query", "fragment", "form_post"},
		"response_types_supported":      []string{"code", "id_token", "code id_token", "token id_token"},
		"grant_types_supported": []string{
			"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code",
		},
		"subject_types_supported":               []string{"pairwise"},
		"scopes_supported":                      append([]string{"openid", "profile", "offline_access", "email"}, s.opts.Scopes...),
		"token_endpoint_auth_methods_supported": []string{"client_secret_post", "private_key_jwt", "client_secret_basic"},
		"claims_supported": []string{
			"aud", "iss", "iat", "exp", "nbf", "name", "sub", "oid", "tid", "preferred_username", "xms_cc",
		},
		// The fake mints unsigned tokens (see the package comment), so it advertises that instead
		// of claiming an algorithm it cannot produce. MSAL does not read this field.
		"id_token_signing_alg_values_supported": []string{"none"},
		"cloud_instance_name":                   "microsoftonline.com",
		"msgraph_host":                          "graph.microsoft.com",
		"tenant_region_scope":                   "NA",
	})
}

// serveJWKS answers the jwks_uri with a well-formed but empty key set: the fake's tokens are
// unsigned (see the package comment), so there is no key to publish.
func (s *Server) serveJWKS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeError(w, methodNotAllowed("GET"))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"keys": []any{}})
}

func (s *Server) serveDeviceCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, methodNotAllowed("POST"))
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeError(w, invalidRequest("the request body could not be parsed as a form"))
		return
	}
	form := r.PostForm
	if e := s.checkClient(form.Get("client_id")); e != nil {
		s.writeError(w, e)
		return
	}
	if e := s.opts.Errors.DeviceAuthorization; e != nil {
		s.writeError(w, e)
		return
	}

	s.mu.Lock()
	s.deviceAuthReqs++
	s.seq++
	code := fmt.Sprintf("dc-%d", s.seq)
	s.deviceCodes[code] = &deviceCode{expiresAt: time.Now().Add(time.Duration(s.opts.DeviceCodeExpiresIn) * time.Second)}
	s.mu.Unlock()

	// The response fields are the ones the docs list
	// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:53-60). There is deliberately
	// no verification_uri_complete: the real service does not support it either
	// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:62-63).
	s.writeJSON(w, http.StatusOK, map[string]any{
		"user_code":        s.opts.UserCode,
		"device_code":      code,
		"verification_uri": s.opts.VerificationURI,
		"expires_in":       s.opts.DeviceCodeExpiresIn,
		"interval":         s.opts.DeviceCodeInterval,
		"message":          s.opts.DeviceCodeMessage,
	})
}

func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.writeError(w, methodNotAllowed("POST"))
		return
	}
	if err := r.ParseForm(); err != nil {
		s.writeError(w, invalidRequest("the request body could not be parsed as a form"))
		return
	}
	form := r.PostForm
	s.mu.Lock()
	s.tokenRequests++
	s.mu.Unlock()

	if e := s.checkClient(form.Get("client_id")); e != nil {
		s.writeError(w, e)
		return
	}

	switch form.Get("grant_type") {
	case grantTypeDeviceCode, grantTypeDeviceCodeShort:
		s.grantDeviceCode(w, form)
	case grantTypeAuthCode:
		s.grantAuthCode(w, form)
	case grantTypeRefresh:
		s.grantRefreshToken(w, form)
	default:
		s.writeError(w, unsupportedGrantType(form.Get("grant_type")))
	}
}

func (s *Server) grantDeviceCode(w http.ResponseWriter, form url.Values) {
	s.mu.Lock()
	s.deviceCodePolls++
	dc := s.deviceCodes[form.Get("device_code")]
	var (
		failure *Error
		polls   int
	)
	switch {
	case dc == nil:
		failure = badVerificationCode()
	case dc.redeemed:
		failure = expiredDeviceCode("the device code has already been redeemed")
	case !time.Now().Before(dc.expiresAt):
		failure = expiredDeviceCode("expires_in has been exceeded")
	default:
		dc.polls++
		polls = dc.polls
		switch {
		case s.opts.Errors.DeviceCode != nil && polls > s.opts.Errors.DeviceCodeAfterPolls:
			failure = s.opts.Errors.DeviceCode
		case polls <= s.opts.DeviceCodePollCount:
			// MSAL retries only authorization_pending and slow_down, and only with HTTP 400
			// (refs/msal-go/apps/internal/oauth/oauth.go:302-328).
			failure = authorizationPending()
		case polls <= s.opts.DeviceCodePollCount+s.opts.SlowDownPolls:
			failure = slowDown()
		default:
			dc.redeemed = true
		}
	}
	var refreshToken string
	if failure == nil {
		refreshToken = s.mintRefreshTokenLocked()
	}
	s.mu.Unlock()

	if failure != nil {
		s.writeError(w, failure)
		return
	}
	s.writeTokenResponse(w, form, refreshToken)
}

func (s *Server) grantAuthCode(w http.ResponseWriter, form url.Values) {
	s.mu.Lock()
	var (
		failure      *Error
		refreshToken string
	)
	if injected := s.opts.Errors.AuthCode; injected != nil {
		failure = injected
	} else if failure = s.checkAuthCodeLocked(form); failure == nil {
		s.authCodes[form.Get("code")].redeemed = true
		refreshToken = s.mintRefreshTokenLocked()
	}
	s.mu.Unlock()

	if failure != nil {
		s.writeError(w, failure)
		return
	}
	s.writeTokenResponse(w, form, refreshToken)
}

// checkAuthCodeLocked validates an authorization_code request and returns the error to serve, or
// nil when the code may be redeemed. The caller holds s.mu.
func (s *Server) checkAuthCodeLocked(form url.Values) *Error {
	ac := s.authCodes[form.Get("code")]
	switch {
	case ac == nil:
		return invalidGrant("the authorization code is invalid")
	case ac.redeemed:
		// Authorization codes are single use
		// (refs/entra/docs/identity-platform/reference-breaking-changes.md:410).
		return invalidGrant("AADSTS54005: OAuth2 Authorization code was already redeemed, please retry with a new valid code or use an existing refresh token.")
	case !time.Now().Before(ac.expiresAt):
		return invalidGrant("the authorization code has expired")
	}

	// The PKCE code_verifier is required and must hash back to the challenge captured with the
	// code. Entra answers invalid_grant when it does not
	// (refs/entra/docs/identity-platform/v2-oauth2-auth-code-flow.md:319).
	verifier := form.Get("code_verifier")
	if verifier == "" {
		return invalidGrant("code_verifier is required: this authorization code was issued with a code_challenge")
	}
	if !verifyPKCE(ac.method, ac.challenge, verifier) {
		return invalidGrant("the authorization code or PKCE code verifier is invalid or has expired")
	}
	if form.Get("redirect_uri") != "" && form.Get("redirect_uri") != ac.redirectURI {
		return &Error{
			Code:        "invalid_grant",
			Description: "AADSTS50011: The redirect URI specified in the request does not match the redirect URI used to obtain the authorization code.",
			Codes:       []int{50011},
		}
	}
	return nil
}

func (s *Server) grantRefreshToken(w http.ResponseWriter, form url.Values) {
	presented := form.Get("refresh_token")

	s.mu.Lock()
	var (
		failure *Error
		rotated string
	)
	switch {
	case s.opts.Errors.Refresh != nil:
		failure = s.opts.Errors.Refresh
	case presented == "":
		failure = invalidGrant("refresh_token is required")
	case !s.activeRefresh[presented]:
		failure = invalidGrant("AADSTS70008: ExpiredOrRevokedGrant - The refresh token has expired due to inactivity or has already been redeemed.")
	default:
		// Rotation: every redemption mints a new token and invalidates the presented one. The
		// real service does not revoke the old token on use
		// (refs/entra/docs/identity-platform/refresh-tokens.md:28), but the fake does, because
		// that is the only way a test can prove a client wrote the rotated token back to its
		// cache.
		delete(s.activeRefresh, presented)
		s.consumedRefresh = append(s.consumedRefresh, presented)
		rotated = s.mintRefreshTokenLocked()
	}
	s.mu.Unlock()

	if failure != nil {
		s.writeError(w, failure)
		return
	}
	s.writeTokenResponse(w, form, rotated)
}

// mintRefreshTokenLocked mints and activates a refresh token. The caller holds s.mu.
func (s *Server) mintRefreshTokenLocked() string {
	s.seq++
	token := fmt.Sprintf("rt-%d", s.seq)
	s.activeRefresh[token] = true
	s.issuedRefresh = append(s.issuedRefresh, token)
	return token
}

// checkClient validates the client_id when Options.ClientID pins one.
func (s *Server) checkClient(clientID string) *Error {
	if s.opts.ClientID == "" || clientID == s.opts.ClientID {
		return nil
	}
	return &Error{
		Code:        "invalid_client",
		Description: fmt.Sprintf("the client_id %q is not registered in this tenant", clientID),
		Status:      http.StatusBadRequest,
	}
}

// writeTokenResponse serves a successful token response for the request in form.
func (s *Server) writeTokenResponse(w http.ResponseWriter, form url.Values, refreshToken string) {
	clientID := form.Get("client_id")
	now := time.Now()

	// MSAL merges the capabilities configured with WithClientCapabilities into the "claims"
	// parameter of every request as {"access_token":{"xms_cc":{"values":["cp1"]}}}
	// (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:486-499); the real service
	// copies the value into the access token's xms_cc claim
	// (refs/entra/docs/identity-platform/claims-challenge.md:167-169).
	clientCapable := hasClientCapability(form.Get("claims"), "cp1")

	// The scope field reports what the client asked for, so MSAL's scope comparison finds no
	// declined scopes (refs/msal-go/.../accesstokens/tokens.go:249-259).
	scope := form.Get("scope")
	if scope == "" {
		scope = strings.Join(s.opts.Scopes, " ")
	}

	body := map[string]any{
		"token_type":     "Bearer",
		"scope":          scope,
		"expires_in":     s.opts.ExpiresIn,
		"ext_expires_in": s.opts.ExpiresIn,
		"access_token":   s.accessToken(clientID, now, clientCapable),
		"id_token":       s.idToken(clientID, now),
		// client_info is base64url-encoded JSON, which is what makes MSAL produce a
		// HomeAccountID (refs/msal-go/.../accesstokens/tokens.go:120-148,262-271).
		"client_info": b64(marshalJSON(map[string]any{"uid": s.opts.Account.UID, "utid": s.opts.Account.UTID})),
	}
	if refreshToken != "" {
		body["refresh_token"] = refreshToken
	}
	s.writeJSON(w, http.StatusOK, body)
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError serves an OAuth error response. The real service answers with a JSON object whose
// error_description starts with the AADSTSxxxx code
// (refs/entra/docs/identity-platform/reference-error-codes.md:37).
func (s *Server) writeError(w http.ResponseWriter, e *Error) {
	status := e.Status
	if status == 0 {
		status = http.StatusBadRequest
	}
	body := map[string]any{
		"error":             e.Code,
		"error_description": e.Description,
		"correlation_id":    s.correlationID,
		"timestamp":         time.Now().UTC().Format("2006-01-02 15:04:05Z"),
	}
	if e.Suberror != "" {
		body["suberror"] = e.Suberror
	}
	if len(e.Codes) > 0 {
		body["error_codes"] = e.Codes
	}
	s.writeJSON(w, status, body)
}

// ServeHTTP routes the requests MSAL and the flow helpers make. The routing is exact rather than
// pattern-based so a typo in a test's URL surfaces as a 404 naming the request path.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tenant := "/" + s.opts.Tenant
	switch r.URL.Path {
	case tenant + pathOpenIDConfiguration:
		s.serveOpenIDConfiguration(w, r)
	case tenant + pathJWKS:
		s.serveJWKS(w, r)
	case tenant + pathDeviceCode:
		s.serveDeviceCode(w, r)
	case tenant + pathToken:
		s.serveToken(w, r)
	default:
		s.writeError(w, &Error{
			Code:        "invalid_request",
			Description: fmt.Sprintf("fakeidp does not serve %s", r.URL.Path),
			Status:      http.StatusNotFound,
		})
	}
}
