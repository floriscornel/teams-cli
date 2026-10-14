package graph

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

const caeClaims = `{"access_token":{"nbf":{"essential":true,"value":"1791960000"}}}`

func challengeHeader(claims string) string {
	return `Bearer realm="", authorization_uri="https://login.microsoftonline.com/common/oauth2/authorize", ` +
		`client_id="00000003-0000-0000-c000-000000000000", error="insufficient_claims", claims="` +
		base64.StdEncoding.EncodeToString([]byte(claims)) + `"`
}

func TestClaimsChallenge(t *testing.T) {
	if got := claimsChallenge([]string{challengeHeader(caeClaims)}); got != caeClaims {
		t.Fatalf("%q", got)
	}
	raw := `Bearer error="insufficient_claims", claims="` + base64.RawStdEncoding.EncodeToString([]byte(caeClaims)) + `"`
	if got := claimsChallenge([]string{raw}); got != caeClaims {
		t.Fatalf("unpadded: %q", got)
	}
	for _, h := range []string{
		`Bearer error="invalid_token"`,
		`Bearer error="insufficient_claims", claims="!!!"`,
		`Bearer error="insufficient_claims", claims="` + base64.StdEncoding.EncodeToString([]byte("not json")) + `"`,
		`Bearer error="insufficient_claims"`,
	} {
		if got := claimsChallenge([]string{h}); got != "" {
			t.Errorf("%s: %q", h, got)
		}
	}
}

// claimsSource hands out "old" until TokenWithClaims is called, then "new".
type claimsSource struct {
	mu     sync.Mutex
	tok    string
	claims []string
}

func (s *claimsSource) Token(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tok, nil
}

func (s *claimsSource) TokenWithClaims(_ context.Context, claims string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims = append(s.claims, claims)
	s.tok = "new"
	return s.tok, nil
}

// revoking answers 401 with a claims challenge to every token but "new" (or to every token, when always).
func revoking(t *testing.T, always bool, calls *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*calls = append(*calls, r.Header.Get("Authorization"))
		mu.Unlock()
		if always || r.Header.Get("Authorization") != "Bearer new" {
			w.Header().Set("WWW-Authenticate", challengeHeader(caeClaims))
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"InvalidAuthenticationToken","message":"Continuous access evaluation resulted in challenge"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"displayName":"DevBot"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDoAnswersAClaimsChallengeOnce(t *testing.T) {
	var calls []string
	srv := revoking(t, false, &calls)
	src := &claimsSource{tok: "old"}
	c, err := New(Options{BaseURL: srv.URL, Token: src, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/me"})
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%v", err)
	}
	if len(calls) != 2 || calls[0] != "Bearer old" || calls[1] != "Bearer new" {
		t.Fatalf("calls: %v", calls)
	}
	if len(src.claims) != 1 || src.claims[0] != caeClaims {
		t.Fatalf("claims passed: %v", src.claims)
	}
}

func TestDoGivesUpWhenTheNewTokenIsChallengedToo(t *testing.T) {
	var calls []string
	srv := revoking(t, true, &calls)
	src := &claimsSource{tok: "old"}
	c, err := New(Options{BaseURL: srv.URL, Token: src, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/me"}); !IsUnauthorized(err) {
		t.Fatalf("err = %v", err)
	}
	if len(src.claims) != 1 || len(calls) != 2 {
		t.Fatalf("challenge answered %d times, %d calls", len(src.claims), len(calls))
	}
}

func TestDoWithoutClaimsSupportKeepsThe401(t *testing.T) {
	var calls []string
	srv := revoking(t, false, &calls)
	c, err := New(Options{
		BaseURL: srv.URL, Token: TokenSourceFunc(func(context.Context) (string, error) { return "old", nil }),
		HTTPClient: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Do(context.Background(), Request{Method: http.MethodGet, Path: "/me"})
	if !IsUnauthorized(err) || len(calls) != 1 {
		t.Fatalf("err = %v, %d calls", err, len(calls))
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Claims != caeClaims {
		t.Fatalf("claims not on the error: %+v", apiErr)
	}
}
