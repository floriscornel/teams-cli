package fakeidp

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Token minting.
//
// Every token is an unsigned JWT: the header is {"typ":"JWT","alg":"none"} and the signature is
// empty, which is exactly what the device-code sample in the Entra docs returns
// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:109). MSAL decodes the ID token's
// payload without checking the algorithm or the signature
// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:48-81), so nothing here has to be
// signed, and signing would only slow every test server down. The tokens are still real JWTs, so
// tests can decode them with the same code the CLI uses to inspect a Graph token.

// reservedScopes are the OIDC scopes that never appear in an access token's scp claim: scp is the
// set of scopes the resource exposes and the client was consented for
// (refs/entra/docs/identity-platform/access-token-claims-reference.md:59).
var reservedScopes = map[string]bool{
	"openid":         true,
	"profile":        true,
	"offline_access": true,
	"email":          true,
}

// consentedScopes returns Options.Scopes without the reserved OIDC scopes, in order.
func (s *Server) consentedScopes() []string {
	scopes := make([]string, 0, len(s.opts.Scopes))
	for _, scope := range s.opts.Scopes {
		if !reservedScopes[strings.ToLower(scope)] {
			scopes = append(scopes, scope)
		}
	}
	return scopes
}

// accessToken mints the access token for a request. The claims are the Graph-shaped ones the CLI
// reads: aud (the resource), scp (the consented scopes), appid (the client), tid, sub and oid,
// plus xms_cc when the client declared the cp1 capability.
func (s *Server) accessToken(clientID string, now time.Time, clientCapable bool) string {
	claims := map[string]any{
		"aud":   s.opts.Audience,
		"iss":   s.Endpoints().Issuer,
		"iat":   now.Unix(),
		"nbf":   now.Unix(),
		"exp":   now.Add(time.Duration(s.opts.ExpiresIn) * time.Second).Unix(),
		"appid": clientID,
		"tid":   s.opts.TenantID,
		"sub":   s.opts.Account.Subject,
		"oid":   s.opts.Account.ObjectID,
		"scp":   strings.Join(s.consentedScopes(), " "),
		"uti":   s.tokenIdentifier(),
		"ver":   "2.0",
	}
	if clientCapable {
		// The client sent {"access_token":{"xms_cc":{"values":["cp1"]}}} in its claims parameter,
		// so the resource echoes cp1 back; a value of cp1 is the authoritative signal that the
		// client can handle a claims challenge
		// (refs/entra/docs/identity-platform/claims-challenge.md:167-169).
		claims["xms_cc"] = []string{"cp1"}
	}
	return unsignedJWT(claims)
}

// idToken mints the ID token. MSAL takes preferred_username and name from it for the account and
// oid or sub as the local account ID
// (refs/msal-go/apps/internal/base/storage/storage.go:216-238,
// refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:101-107).
func (s *Server) idToken(clientID string, now time.Time) string {
	return unsignedJWT(map[string]any{
		"aud":                clientID,
		"iss":                s.Endpoints().Issuer,
		"iat":                now.Unix(),
		"nbf":                now.Unix(),
		"exp":                now.Add(time.Duration(s.opts.ExpiresIn) * time.Second).Unix(),
		"name":               s.opts.Account.Name,
		"preferred_username": s.opts.Account.PreferredUsername,
		"oid":                s.opts.Account.ObjectID,
		"sub":                s.opts.Account.Subject,
		"tid":                s.opts.TenantID,
		"uti":                s.tokenIdentifier(),
		"ver":                "2.0",
	})
}

// tokenIdentifier mints the uti claim: Entra's per-token identifier, "equivalent to jti in the JWT
// specification" (refs/entra/docs/identity-platform/access-token-claims-reference.md:70). Without it
// two tokens minted within the same second would be byte-identical, and a test could not tell a
// refresh from a cache hit by looking at the access token.
func (s *Server) tokenIdentifier() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokenSeq++
	return fmt.Sprintf("uti-%d", s.tokenSeq)
}

// unsignedJWT builds a JWT with an "alg":"none" header and an empty signature. json.Marshal sorts
// map keys, so the same claims always produce the same token.
func unsignedJWT(claims map[string]any) string {
	header := b64([]byte(`{"typ":"JWT","alg":"none"}`))
	payload := b64(marshalJSON(claims))
	return header + "." + payload + "."
}

// b64 is the base64url encoding without padding that JWT and the client_info field use
// (refs/msal-go/apps/internal/oauth/ops/accesstokens/tokens.go:327-330).
func b64(data []byte) string { return base64.RawURLEncoding.EncodeToString(data) }

func marshalJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic("fakeidp: token claims are not JSON-safe: " + err.Error())
	}
	return data
}

// hasClientCapability reports whether a "claims" parameter carried capability (cp1) in its
// xms_cc claim. MSAL sends the capability as
// {"access_token":{"xms_cc":{"values":["cp1"]}}}
// (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:486-499). A claims challenge such
// as {"access_token":{"acrs":{"essential":true,"value":"c25"}}} carries no xms_cc and therefore
// yields false; the real service treats the values as case-insensitive
// (refs/entra/docs/identity-platform/claims-challenge.md:169).
func hasClientCapability(claims, capability string) bool {
	if claims == "" {
		return false
	}
	var parsed struct {
		AccessToken struct {
			XMSCC struct {
				Values []string `json:"values"`
			} `json:"xms_cc"`
		} `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(claims), &parsed); err != nil {
		return false
	}
	for _, value := range parsed.AccessToken.XMSCC.Values {
		if strings.EqualFold(value, capability) {
			return true
		}
	}
	return false
}
