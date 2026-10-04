package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// makeToken builds an unsigned JWT with the given claims. MSAL does not verify
// signatures and neither do we, so tests need no key material.
func makeToken(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func TestParseClaimsReadsTheDocumentedClaims(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	token := makeToken(t, map[string]any{
		"aud":                GraphAppIDAudience,
		"iss":                "https://login.microsoftonline.com/tenant/v2.0",
		"tid":                "tenant-id",
		"appid":              "client-id",
		"sub":                "subject",
		"name":               "Alice Example",
		"preferred_username": "alice@example.com",
		"scp":                "User.Read Chat.ReadWrite",
		"xms_cc":             map[string]any{"values": []any{"cp1"}},
		"exp":                float64(exp.Unix()),
		"nbf":                float64(exp.Add(-time.Hour).Unix()),
		"iat":                float64(exp.Add(-time.Hour).Unix()),
	})
	claims, err := ParseClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Audience != GraphAppIDAudience || claims.TenantID != "tenant-id" || claims.AppID != "client-id" {
		t.Errorf("claims = %+v", claims)
	}
	if claims.PreferredUsername != "alice@example.com" || claims.Name != "Alice Example" {
		t.Errorf("identity claims = %+v", claims)
	}
	if strings.Join(claims.Scopes, " ") != "User.Read Chat.ReadWrite" {
		t.Errorf("scopes = %v", claims.Scopes)
	}
	if len(claims.Capabilities) != 1 || claims.Capabilities[0] != "cp1" {
		t.Errorf("capabilities = %v", claims.Capabilities)
	}
	if !claims.ExpiresOn.Equal(exp) || claims.IssuedAt.IsZero() || claims.NotBefore.IsZero() {
		t.Errorf("times = %+v", claims)
	}
	if !claims.HasScope("chat.readwrite") {
		t.Error("HasScope is not case-insensitive")
	}
	if claims.HasScope("Files.Read.All") {
		t.Error("HasScope matched a scope that is not in the token")
	}
	if claims.Expired(time.Now()) {
		t.Error("a token with an hour left is reported expired")
	}
}

func TestParseClaimsHandlesAudArrayAndRoles(t *testing.T) {
	token := makeToken(t, map[string]any{
		"aud":   []any{"https://graph.microsoft.com"},
		"roles": []any{"ChannelMessage.Read.All"},
		"upn":   "fallback@example.com",
	})
	claims, err := ParseClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Audience != "https://graph.microsoft.com" {
		t.Errorf("Audience = %q", claims.Audience)
	}
	if strings.Join(claims.Scopes, " ") != "ChannelMessage.Read.All" {
		t.Errorf("Scopes = %v", claims.Scopes)
	}
	if claims.PreferredUsername != "fallback@example.com" {
		t.Errorf("PreferredUsername = %q", claims.PreferredUsername)
	}
	if claims.Capabilities != nil {
		t.Errorf("Capabilities = %v", claims.Capabilities)
	}
}

func TestParseClaimsReadsArrayCapabilities(t *testing.T) {
	token := makeToken(t, map[string]any{"xms_cc": []any{"cp1", "cp2"}})
	claims, err := ParseClaims(token)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(claims.Capabilities, ",") != "cp1,cp2" {
		t.Errorf("Capabilities = %v", claims.Capabilities)
	}
}

func TestParseClaimsRejectsMalformedTokens(t *testing.T) {
	cases := map[string]string{
		"empty":      "",
		"one part":   "abc",
		"bad base64": "a.!!!.c",
		"not json":   "a." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".c",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseClaims(token); err == nil {
				t.Fatalf("ParseClaims(%q) succeeded", token)
			}
		})
	}
}

func TestValidateAccessTokenChecksAudienceAndLifetime(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	valid := makeToken(t, map[string]any{
		"aud": GraphAppIDAudience,
		"exp": float64(now.Add(time.Hour).Unix()),
	})
	claims, err := ValidateAccessToken(valid, []string{"https://graph.microsoft.com"}, now)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if claims.Audience != GraphAppIDAudience {
		t.Errorf("claims = %+v", claims)
	}

	urlAud := makeToken(t, map[string]any{
		"aud": "https://graph.microsoft.com",
		"exp": float64(now.Add(time.Hour).Unix()),
	})
	if _, err := ValidateAccessToken(urlAud, []string{"https://graph.microsoft.com"}, now); err != nil {
		t.Errorf("URL audience rejected: %v", err)
	}

	wrongAud := makeToken(t, map[string]any{
		"aud": "https://outlook.office.com",
		"exp": float64(now.Add(time.Hour).Unix()),
	})
	_, err = ValidateAccessToken(wrongAud, []string{"https://graph.microsoft.com"}, now)
	if err == nil || !strings.Contains(err.Error(), "aud") {
		t.Fatalf("wrong audience = %v", err)
	}

	expired := makeToken(t, map[string]any{
		"aud": GraphAppIDAudience,
		"exp": float64(now.Add(-2 * time.Minute).Unix()),
	})
	if _, err := ValidateAccessToken(expired, nil, now); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired token = %v", err)
	}

	noExp := makeToken(t, map[string]any{"aud": GraphAppIDAudience})
	if _, err := ValidateAccessToken(noExp, nil, now); err == nil || !strings.Contains(err.Error(), "exp") {
		t.Fatalf("token without exp = %v", err)
	}

	notYet := makeToken(t, map[string]any{
		"aud": GraphAppIDAudience,
		"exp": float64(now.Add(time.Hour).Unix()),
		"nbf": float64(now.Add(time.Hour).Unix()),
	})
	if _, err := ValidateAccessToken(notYet, nil, now); err == nil || !strings.Contains(err.Error(), "not valid until") {
		t.Fatalf("future nbf token = %v", err)
	}
}
