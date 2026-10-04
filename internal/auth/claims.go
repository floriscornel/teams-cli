package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GraphAppIDAudience is the audience a delegated Graph token carries. The spike
// saw the GUID form, not the URL form PLAN.md assumed
// (docs/spike/phase1.md:16), so both are accepted where a token is inspected.
const GraphAppIDAudience = "00000003-0000-0000-c000-000000000000"

// Claims is the decoded (unverified) payload of a JWT access token. Only the
// fields the CLI actually reads are typed; Raw keeps everything else.
//
// This is a decode, not a verification: it exists to produce clear errors for
// the common mistakes (wrong audience, expired token) and it is never a security
// control. PLAN.md says so explicitly for the TEAMS_ACCESS_TOKEN escape hatch.
type Claims struct {
	Audience          string
	Issuer            string
	TenantID          string
	AppID             string
	Subject           string
	Name              string
	PreferredUsername string
	Scopes            []string
	Capabilities      []string
	ExpiresOn         time.Time
	NotBefore         time.Time
	IssuedAt          time.Time
	Raw               map[string]any
}

// ParseClaims decodes the payload of a JWT. It does not check the signature.
func ParseClaims(token string) (Claims, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("the access token is not a JWT (expected three dot separated parts)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("decode the access token payload: %w", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		return Claims{}, fmt.Errorf("parse the access token payload: %w", err)
	}
	c := Claims{
		Audience:          stringClaim(raw, "aud"),
		Issuer:            stringClaim(raw, "iss"),
		TenantID:          stringClaim(raw, "tid"),
		AppID:             stringClaim(raw, "appid"),
		Subject:           stringClaim(raw, "sub"),
		Name:              stringClaim(raw, "name"),
		PreferredUsername: firstNonEmpty(stringClaim(raw, "preferred_username"), stringClaim(raw, "upn")),
		Raw:               raw,
	}
	if c.Scopes = spaceList(stringClaim(raw, "scp")); len(c.Scopes) == 0 {
		// Application tokens use "roles"; we are delegated-only, but reading it
		// keeps a misconfigured token from looking like "no scopes at all".
		c.Scopes = spaceList(stringClaim(raw, "roles"))
	}
	c.Capabilities = capabilityClaim(raw)
	c.ExpiresOn = timeClaim(raw, "exp")
	c.NotBefore = timeClaim(raw, "nbf")
	c.IssuedAt = timeClaim(raw, "iat")
	return c, nil
}

// HasScope reports whether the token carries a scope. The `scp` claim is a
// space separated list (refs/entra/docs/identity-platform/access-token-claims-reference.md:59).
func (c Claims) HasScope(scope string) bool {
	for _, s := range c.Scopes {
		if strings.EqualFold(s, scope) {
			return true
		}
	}
	return false
}

// Expired reports whether the token is expired at now (a token with no exp claim
// counts as expired: we cannot tell, so we do not trust it).
func (c Claims) Expired(now time.Time) bool {
	return c.ExpiresOn.IsZero() || !now.Before(c.ExpiresOn)
}

// ValidateAccessToken checks the audience and lifetime of a token we did not
// obtain ourselves (TEAMS_ACCESS_TOKEN). allowedAudiences lists the audience
// values that are acceptable for the configured cloud; the Graph app GUID is
// always accepted.
func ValidateAccessToken(token string, allowedAudiences []string, now time.Time) (Claims, error) {
	claims, err := ParseClaims(token)
	if err != nil {
		return Claims{}, err
	}
	allowed := append([]string{GraphAppIDAudience}, allowedAudiences...)
	ok := false
	for _, a := range allowed {
		if a != "" && strings.EqualFold(claims.Audience, a) {
			ok = true
			break
		}
	}
	if !ok {
		return claims, fmt.Errorf("the access token's aud is %q, want the Graph app id %s or %s",
			claims.Audience, GraphAppIDAudience, strings.Join(allowedAudiences, " / "))
	}
	if claims.ExpiresOn.IsZero() {
		return claims, errors.New("the access token has no exp claim, so it cannot be trusted")
	}
	if claims.Expired(now) {
		return claims, fmt.Errorf("the access token expired %s ago", now.Sub(claims.ExpiresOn).Round(time.Second))
	}
	if claims.NotBefore.After(now.Add(time.Minute)) {
		return claims, fmt.Errorf("the access token is not valid until %s (check the machine clock)", claims.NotBefore.UTC().Format(time.RFC3339))
	}
	return claims, nil
}

func stringClaim(raw map[string]any, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case []any:
		// aud can be an array; the first entry is what we compare against.
		if len(t) > 0 {
			if s, ok := t[0].(string); ok {
				return s
			}
		}
	}
	return ""
}

func spaceList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return strings.Fields(v)
}

// capabilityClaim reads xms_cc, which is {"values":["CP1"]} for client
// capabilities (refs/msal-go/apps/internal/oauth/ops/authority/authority.go:486-499).
func capabilityClaim(raw map[string]any) []string {
	v, ok := raw["xms_cc"]
	if !ok {
		return nil
	}
	switch t := v.(type) {
	case map[string]any:
		list, _ := t["values"].([]any)
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func timeClaim(raw map[string]any, key string) time.Time {
	n, ok := raw[key].(float64)
	if !ok || n <= 0 {
		return time.Time{}
	}
	return time.Unix(int64(n), 0).UTC()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
