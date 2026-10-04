package auth

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/floriscornel/teams-cli/internal/auth/tokenstore"
	"github.com/floriscornel/teams-cli/internal/output"
)

func TestAADSTSCodeExtraction(t *testing.T) {
	err := fmt.Errorf(`http call error: reply status code was 400: {"error":"invalid_grant","error_description":"AADSTS65001: The user or administrator has not consented"}`)
	if got := AADSTSCode(err); got != "AADSTS65001" {
		t.Errorf("AADSTSCode = %q", got)
	}
	if AADSTSCode(nil) != "" || AADSTSCode(errors.New("plain")) != "" {
		t.Error("AADSTSCode found a code where there is none")
	}
}

func TestClassifyGivesExitCodeAndHint(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantHint string
	}{
		{
			"consent missing",
			errors.New(`{"error":"invalid_grant","error_description":"AADSTS65001: The user or administrator has not consented to use the application"}`),
			output.CodeAuth,
			"user or admin consent",
		},
		{
			"wrong tenant",
			errors.New("AADSTS50020: User account from identity provider does not exist in tenant"),
			output.CodeAuth,
			"tenant and client_id",
		},
		{
			"redirect uri",
			errors.New("AADSTS50011: The redirect URI specified in the request does not match"),
			output.CodeAuth,
			"http://localhost",
		},
		{
			"mfa required",
			errors.New("AADSTS50076: Due to a configuration change made by your administrator, you must use multi-factor authentication"),
			output.CodeAuth,
			"MFA",
		},
		{
			"conditional access",
			errors.New("AADSTS53003: Access has been blocked by Conditional Access policies"),
			output.CodeAuth,
			"runbook",
		},
		{
			"refresh token expired",
			errors.New("AADSTS700082: The refresh token has expired due to inactivity"),
			output.CodeAuth,
			"90 days",
		},
		{
			"no account",
			errors.New("no account was specified with public.WithSilentAccount(), or the specified account is invalid"),
			output.CodeAuth,
			"auth login",
		},
		{
			"no token found",
			errors.New("no token found"),
			output.CodeAuth,
			"auth login",
		},
		{
			"invalid grant",
			errors.New(`{"error":"invalid_grant"}`),
			output.CodeAuth,
			"revoked",
		},
		{
			"key missing",
			tokenstore.ErrKeyMissing,
			output.CodeAuth,
			"keychain",
		},
		{
			"something else",
			errors.New("dial tcp: connection refused"),
			output.CodeError,
			"MSAL error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err, "me")
			if got == nil {
				t.Fatal("Classify returned nil")
			}
			if code := output.CodeOf(got); code != tc.wantCode {
				t.Errorf("exit code = %d, want %d (%v)", code, tc.wantCode, got)
			}
			hint := output.HintOf(got)
			if !strings.Contains(hint, tc.wantHint) {
				t.Errorf("hint = %q, want it to contain %q", hint, tc.wantHint)
			}
			if !strings.Contains(got.Error(), "profile me") && tc.name != "invalid grant" {
				t.Errorf("message = %q, want it to name the profile", got.Error())
			}
		})
	}
}

func TestClassifyKeepsAnExistingHint(t *testing.T) {
	original := output.WithHint(output.Authf("already classified"), "first hint")
	got := Classify(original, "me")
	if output.HintOf(got) != "first hint" {
		t.Errorf("hint = %q, want the original", output.HintOf(got))
	}
	if output.CodeOf(got) != output.CodeAuth {
		t.Errorf("exit code = %d", output.CodeOf(got))
	}
}

func TestClassifyNilIsNil(t *testing.T) {
	if Classify(nil, "me") != nil {
		t.Error("Classify(nil) returned an error")
	}
}

func TestIsSignedOut(t *testing.T) {
	signedOut := []error{
		errors.New("no account was specified with public.WithSilentAccount()"),
		errors.New("no token found"),
		errors.New("interaction_required"),
		errors.New("AADSTS65001: consent"),
		errors.New("AADSTS50173: token revoked"),
	}
	for _, err := range signedOut {
		if !IsSignedOut(err) {
			t.Errorf("IsSignedOut(%v) = false", err)
		}
	}
	if IsSignedOut(nil) {
		t.Error("IsSignedOut(nil) = true")
	}
	if IsSignedOut(errors.New("dial tcp: connection refused")) {
		t.Error("a network error is not a sign-out")
	}
	if !IsConsentError(errors.New("AADSTS65001")) || !IsRedirectURIError(errors.New("AADSTS50011")) {
		t.Error("code predicates failed")
	}
	if IsConsentError(errors.New("AADSTS50011")) {
		t.Error("IsConsentError matched the wrong code")
	}
}

func TestMSALVerboseIsEmptyForNil(t *testing.T) {
	if MSALVerbose(nil) != "" {
		t.Error("MSALVerbose(nil) is not empty")
	}
	if MSALVerbose(errors.New("boom")) == "" {
		t.Error("MSALVerbose dropped a plain error")
	}
}
