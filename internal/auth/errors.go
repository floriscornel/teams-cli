package auth

import (
	"errors"
	"regexp"
	"strings"

	"github.com/floriscornel/teams-cli/internal/auth/tokenstore"
	"github.com/floriscornel/teams-cli/internal/output"
)

// This file turns MSAL's raw errors into the exit codes and hints PLAN.md
// requires. MSAL Go classifies nothing: every failure arrives as an
// errors.CallErr whose message embeds the JSON body, so the AADSTS code has to
// be recovered from the text (refs/msal-go/apps/errors/errors.go:57-71).

var aadstsRE = regexp.MustCompile(`AADSTS\d+`)

// AADSTSCode extracts the first AADSTS code from an error, or "".
func AADSTSCode(err error) string {
	if err == nil {
		return ""
	}
	return aadstsRE.FindString(err.Error())
}

// IsSignedOut reports whether err means "there is no usable account": MSAL's two
// distinct no-cache errors, a dead refresh token, or a consent failure.
func IsSignedOut(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	switch AADSTSCode(err) {
	case "AADSTS65001", "AADSTS50020", "AADSTS700082", "AADSTS700084", "AADSTS50173":
		return true
	}
	for _, needle := range []string{
		"no account was specified",
		"no token found",
		"invalid_grant",
		"interaction_required",
		"login_required",
		"consent_required",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// Classify maps an auth error to a coded error with a hint. It never returns a
// nil error for a non-nil input, and it leaves an already-classified *output.Error
// alone (except to add a missing hint).
func Classify(err error, profile string) error {
	if err == nil {
		return nil
	}
	// An error we already classified keeps its code and hint.
	var classified *output.Error
	if errors.As(err, &classified) {
		return err
	}
	if errors.Is(err, tokenstore.ErrKeyMissing) {
		return output.WithHint(output.Authf("the token cache cannot be decrypted (profile %s)", profile),
			"the encryption key lives in your OS keychain and is no longer there or no longer matches the cache; run `teams auth login` to start a new session, or `teams auth logout` to discard the old cache first")
	}

	msg := err.Error()
	code := AADSTSCode(err)
	switch code {
	case "AADSTS65001":
		// The doc is explicit that this is "the user OR administrator hasn't
		// consented" (refs/entra/docs/identity-platform/reference-error-codes.md:247),
		// and PLAN.md warns against assuming it is always an admin problem. The
		// code stays in the message: support tickets are searched by AADSTS code.
		return output.WithHint(output.Authf("consent is missing for profile %s (%s)", profile, code),
			"user or admin consent is missing for the requested scopes; run `teams auth login` to be prompted, or `teams auth status --admin-request` for the ticket")
	case "AADSTS50020":
		return output.WithHint(output.Authf("the account cannot sign in to this tenant (profile %s, %s)", profile, code),
			"check the profile's tenant and client_id, and make sure you are not signing in with a personal Microsoft account")
	case "AADSTS50011":
		return output.WithHint(output.Authf("the app registration rejected the redirect URI (profile %s, %s)", profile, code),
			"register http://localhost as a Mobile and desktop redirect URI on the app, or use `teams auth login --device`; see `teams auth status --admin-request`")
	case "AADSTS50076", "AADSTS50079", "AADSTS50158":
		return output.WithHint(output.Authf("a Conditional Access policy blocked the silent sign-in (profile %s, %s)", profile, code),
			"run `teams auth login` interactively to satisfy MFA, or exempt the account from the policy for headless use")
	case "AADSTS53003":
		return output.WithHint(output.Authf("Conditional Access blocked the sign-in (profile %s, %s)", profile, code),
			"an admin must allow sign-ins from this device or network; see the bot runbook in the README")
	case "AADSTS700082", "AADSTS700084", "AADSTS50173":
		return output.WithHint(output.Authf("the refresh token is no longer valid (profile %s, %s)", profile, code),
			"refresh tokens expire after 90 days of inactivity and are revoked by a password reset or an admin revocation; run `teams auth login` again")
	}
	if IsSignedOut(err) {
		hint := "run `teams auth login`"
		if strings.Contains(msg, "invalid_grant") {
			hint = "the refresh token was rejected (revoked, expired, or re-used); run `teams auth login` again"
		}
		return output.WithHint(output.Authf("not signed in for profile %s", profile), hint)
	}
	if errors.Is(err, errNoDeviceCodeSupport) {
		return output.WithHint(output.Usagef("no interactive sign-in is possible here"), "run `teams auth login --device` and enter the code on another device")
	}
	return output.WithHint(output.Errorf("authentication failed for profile %s", profile), "run `teams auth login -v` for the full MSAL error")
}

// errNoDeviceCodeSupport is returned when a flow needs a browser that the
// environment cannot provide.
var errNoDeviceCodeSupport = errors.New("this environment has no browser or terminal for an interactive sign-in")

// IsConsentError reports whether err is an AADSTS65001-style consent failure.
func IsConsentError(err error) bool { return AADSTSCode(err) == "AADSTS65001" }

// IsRedirectURIError reports whether err is the AADSTS50011 the spike hit, which
// makes `auth login` fall back to device code (PLAN.md:110).
func IsRedirectURIError(err error) bool { return AADSTSCode(err) == "AADSTS50011" }
