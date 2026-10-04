package fakeidp

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The authorization-code + PKCE flow helper.
//
// MSAL never sends an HTTP request to the authorization endpoint: it builds a URL, hands it to the
// openURL callback and waits for the response on a short-lived listener at
// http://localhost:<port> (refs/msal-go/apps/internal/base/base.go:287-332 and
// refs/msal-go/apps/public/public.go:751-778). A headless test therefore plays the browser: it
// reads the code_challenge, state and redirect_uri out of that URL, mints a code bound to the
// challenge and POSTs state and code back to the listener, which is the form_post response mode
// MSAL asks for (refs/msal-go/apps/internal/base/base.go:321-324).
//
// GET is deliberately not implemented. MSAL's listener answers a GET with 405 and fails the
// acquisition with "response was received via a GET operation, which is not supported"
// (refs/msal-go/apps/internal/local/server.go:155-163), so there is nothing for the fake to do;
// [ReturnAuthorizationCode] always POSTs. TestRejectsGETCallback in msal_test.go pins that
// behaviour down.

// AuthorizationRequest is the authorization URL MSAL built, captured by [Server.ReturnAuthorizationCode].
type AuthorizationRequest struct {
	// ClientID is the client_id MSAL sent.
	ClientID string
	// RedirectURI is where MSAL is listening, for example http://localhost:53123.
	RedirectURI string
	// Scopes is the requested scope set, split on spaces. MSAL appends openid, profile and
	// offline_access (refs/msal-go/apps/internal/oauth/ops/accesstokens/accesstokens.go:490-501).
	Scopes []string
	// State is the CSRF state MSAL generates for the round trip
	// (refs/msal-go/apps/public/public.go:706).
	State string
	// CodeChallenge is the PKCE challenge.
	CodeChallenge string
	// CodeChallengeMethod is the PKCE method; MSAL sends S256
	// (refs/msal-go/apps/public/public.go:695-701).
	CodeChallengeMethod string
	// ResponseMode is the response_mode; MSAL sends form_post for interactive auth.
	ResponseMode string
	// Prompt, LoginHint and Claims are the request's optional parameters.
	Prompt    string
	LoginHint string
	Claims    string
	// Raw is the whole query string, for parameters this struct does not name.
	Raw url.Values
}

// FakeBrowser returns a browser hook for public.WithOpenURL
// (refs/msal-go/apps/public/public.go:645-658) that completes the auth-code + PKCE flow in
// process, so AcquireTokenInteractive can be driven without a browser:
//
//	ar, err := client.AcquireTokenInteractive(ctx, scopes, public.WithOpenURL(srv.FakeBrowser()))
func (s *Server) FakeBrowser() func(authURL string) error {
	return func(authURL string) error { return s.ReturnAuthorizationCode(authURL) }
}

// ReturnAuthorizationCode plays the browser for the authorization URL MSAL just built: it mints an
// authorization code bound to the URL's code_challenge and POSTs state and code to the URL's
// redirect_uri — MSAL's local listener. It returns an error only when the fake could not complete
// the round trip (a malformed URL, a redirect URI that answers with a failure); when
// [Errors.Authorization] is set it posts the authorization *error* response instead, which MSAL
// turns into a failed acquisition.
func (s *Server) ReturnAuthorizationCode(authURL string) error {
	parsed, err := url.Parse(authURL)
	if err != nil {
		return fmt.Errorf("fakeidp: parsing the authorization URL: %w", err)
	}
	query := parsed.Query()
	req := AuthorizationRequest{
		ClientID:            query.Get("client_id"),
		RedirectURI:         query.Get("redirect_uri"),
		Scopes:              strings.Fields(query.Get("scope")),
		State:               query.Get("state"),
		CodeChallenge:       query.Get("code_challenge"),
		CodeChallengeMethod: query.Get("code_challenge_method"),
		ResponseMode:        query.Get("response_mode"),
		Prompt:              query.Get("prompt"),
		LoginHint:           query.Get("login_hint"),
		Claims:              query.Get("claims"),
		Raw:                 query,
	}
	s.mu.Lock()
	s.lastAuthRequest = &req
	s.mu.Unlock()

	if req.RedirectURI == "" {
		return fmt.Errorf("fakeidp: the authorization URL has no redirect_uri: %s", authURL)
	}
	if req.ResponseMode != "form_post" {
		return fmt.Errorf(
			"fakeidp: response_mode is %q, want %q: MSAL requests form_post for interactive auth (refs/msal-go/apps/internal/base/base.go:321-324) and the fake only implements that",
			req.ResponseMode, "form_post")
	}

	form := url.Values{"state": {req.State}}
	if injected := s.opts.Errors.Authorization; injected != nil {
		form.Set("error", injected.Code)
		form.Set("error_description", injected.Description)
		if injected.Suberror != "" {
			form.Set("error_subcode", injected.Suberror)
		}
	} else {
		code, err := s.mintAuthorizationCode(req)
		if err != nil {
			return err
		}
		form.Set("code", code)
	}

	resp, err := s.HTTPClient().PostForm(req.RedirectURI, form)
	if err != nil {
		return fmt.Errorf("fakeidp: posting the authorization response to %s: %w", req.RedirectURI, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fakeidp: the redirect URI %s answered HTTP %d: %s",
			req.RedirectURI, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// LastAuthorizationRequest returns the authorization URL the flow helper most recently played the
// browser for, which lets a test assert the PKCE and prompt parameters MSAL sent.
func (s *Server) LastAuthorizationRequest() (AuthorizationRequest, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastAuthRequest == nil {
		return AuthorizationRequest{}, false
	}
	return *s.lastAuthRequest, true
}

// mintAuthorizationCode mints an authorization code bound to the request's PKCE challenge.
func (s *Server) mintAuthorizationCode(req AuthorizationRequest) (string, error) {
	if req.CodeChallenge == "" {
		// PKCE is what makes this flow worth faking, and MSAL always sends a challenge
		// (refs/msal-go/apps/public/public.go:687-696), so a URL without one is a test bug rather
		// than a case to model.
		return "", errors.New("fakeidp: the authorization URL has no code_challenge; the fake only issues PKCE authorization codes")
	}
	method := strings.ToLower(req.CodeChallengeMethod)
	switch method {
	case "s256", "plain":
	case "":
		// "If excluded, code_challenge is assumed to be plaintext if code_challenge is included.
		// The Microsoft identity platform supports both plain and S256"
		// (refs/entra/docs/identity-platform/v2-oauth2-auth-code-flow.md:89).
		method = "plain"
	default:
		return "", fmt.Errorf("fakeidp: unsupported code_challenge_method %q", req.CodeChallengeMethod)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	code := fmt.Sprintf("ac-%d", s.seq)
	s.authCodes[code] = &authCode{
		clientID:    req.ClientID,
		redirectURI: req.RedirectURI,
		challenge:   req.CodeChallenge,
		method:      method,
		expiresAt:   time.Now().Add(DefaultAuthCodeLifetime),
	}
	return code, nil
}

// verifyPKCE checks a code_verifier against the challenge captured with the code:
// BASE64URL(SHA256(verifier)) for S256, or the verifier itself for plain
// (refs/entra/docs/identity-platform/v2-oauth2-auth-code-flow.md:88-89). MSAL hashes with
// base64.RawURLEncoding without padding (refs/msal-go/apps/public/public.go:786-796), the same
// encoding [codeChallengeFor] uses.
func verifyPKCE(method, challenge, verifier string) bool {
	switch strings.ToLower(method) {
	case "", "plain":
		return subtle.ConstantTimeCompare([]byte(challenge), []byte(verifier)) == 1
	case "s256":
		return subtle.ConstantTimeCompare([]byte(challenge), []byte(codeChallengeFor(verifier))) == 1
	default:
		return false
	}
}

// codeChallengeFor computes the S256 challenge a client sends for a verifier. Tests that drive the
// token endpoint by hand use it instead of hard-coding a challenge.
func codeChallengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
