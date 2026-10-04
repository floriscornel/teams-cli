package fakeidp

import "net/http"

// Error is an injected OAuth error response, as served by the token, device-code and
// authorization endpoints. Point an [Errors] field at one of the values returned by the
// constructors in this file, or build a custom one, to make the fake fail the way a real tenant
// would.
type Error struct {
	// Code is the OAuth "error" value: "invalid_grant", "authorization_pending", "expired_token",
	// "authorization_declined", and so on
	// (refs/entra/docs/identity-platform/reference-error-codes.md:58-70,
	// refs/entra/docs/identity-platform/v2-oauth2-device-code.md:91-96).
	Code string
	// Description is served as error_description. The real service starts it with the AADSTSxxxx
	// code and appends Trace ID, Correlation ID and Timestamp lines
	// (refs/entra/docs/identity-platform/reference-error-codes.md:37); the constructors here
	// include the AADSTSxxxx code so tests can assert on it, which is what the CLI's error
	// classifier keys off.
	Description string
	// Status is the HTTP status code. The zero value means 400, which is what MSAL's device-code
	// retry logic requires: it only retries a 400 whose body says authorization_pending or
	// slow_down (refs/msal-go/apps/internal/oauth/oauth.go:302-328).
	Status int
	// Suberror is served as suberror when set, for example "consent_required" or
	// "insufficient_claims" (refs/entra/docs/identity-platform/claims-challenge.md:45).
	Suberror string
	// Codes is served as error_codes when non-empty, matching the AADSTSxxxx numbers embedded in
	// the description (refs/entra/docs/identity-platform/reference-error-codes.md:37).
	Codes []int
}

// Error implements error so injected errors can be compared and logged directly.
func (e *Error) Error() string {
	if e.Description == "" {
		return e.Code
	}
	return e.Code + ": " + e.Description
}

// InvalidGrant returns the generic "the authentication material was unusable" error. Entra uses
// it for an expired or already redeemed refresh token and for a bad PKCE verifier
// (refs/entra/docs/identity-platform/reference-error-codes.md:63).
func InvalidGrant() *Error {
	return &Error{Code: "invalid_grant", Description: "the authorization grant is invalid"}
}

// AADSTS50020 returns the "user from another tenant" error: the signed-in account has no presence
// in the tenant and cannot access the application there
// (refs/entra/docs/identity-platform/reference-error-codes.md:116).
func AADSTS50020() *Error {
	return &Error{
		Code: "invalid_grant",
		Description: "AADSTS50020: UserUnauthorized - Users are unauthorized to call this endpoint. " +
			"User account from identity provider 'live.com' does not exist in the tenant and cannot " +
			"access the application in that tenant. This account needs to be added as an external user " +
			"in the tenant first.",
		Codes: []int{50020},
	}
}

// AADSTS65001 returns the "consent has not been granted" error, the one a silent acquisition hits
// after an admin revokes consent and the one that has to surface as "run teams auth login" with
// exit code 3 (PLAN.md, service-account bot flow).
// The wording is the documented DelegationDoesNotExist text.
func AADSTS65001() *Error {
	return &Error{
		Code: "invalid_grant",
		Description: "AADSTS65001: DelegationDoesNotExist - The user or administrator has not consented " +
			"to use the application with ID 'contoso-client-id'. Send an interactive authorization request " +
			"for this user and resource.",
		Suberror: "consent_required",
		Codes:    []int{65001},
	}
}

// ExpiredDeviceCode returns the terminal device-code error the service serves once expires_in has
// been exceeded (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:96).
func ExpiredDeviceCode() *Error {
	return expiredDeviceCode("expires_in has been exceeded and authentication is no longer possible with this device_code")
}

// AuthorizationDeclined returns the terminal device-code error for a user who denied the request.
// MSAL stops polling on it, because it is neither authorization_pending nor slow_down
// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:94,
// refs/msal-go/apps/internal/oauth/oauth.go:302-328).
func AuthorizationDeclined() *Error {
	return &Error{
		Code:        "authorization_declined",
		Description: "The end user denied the authorization request.",
	}
}

// NewError builds an injected error with a custom code and description.
func NewError(code, description string) *Error {
	return &Error{Code: code, Description: description}
}

// Errors injects error responses. Every field is optional; [Options.Errors] is the zero value
// unless a test wants a failure.
type Errors struct {
	// DeviceAuthorization replaces the device-code response, as an authorization server that
	// refuses to start the flow would.
	DeviceAuthorization *Error
	// DeviceCode is the response to the device-code grant. It is served once the grant has
	// answered authorization_pending DeviceCodeAfterPolls times, so a test can observe polling
	// followed by a terminal error. DeviceCodeAfterPolls 0 (the default) fails the first poll.
	DeviceCode           *Error
	DeviceCodeAfterPolls int
	// AuthCode replaces the response to every authorization_code request.
	AuthCode *Error
	// Refresh replaces the response to every refresh_token request.
	Refresh *Error
	// Authorization replaces the code in the authorization response the flow helper posts back,
	// which is how the authorize endpoint itself refuses a request (for example access_denied).
	Authorization *Error
}

// The errors the fake serves on its own, with the documented wording.

// authorizationPending is the poll-again response. AADSTS70016 is the documented code
// (refs/entra/docs/identity-platform/reference-error-codes.md:273) and HTTP 400 is required for
// MSAL to retry it (refs/msal-go/apps/internal/oauth/oauth.go:302-328).
func authorizationPending() *Error {
	return &Error{
		Code: "authorization_pending",
		Description: "AADSTS70016: OAuth 2.0 device flow error. Authorization is pending. " +
			"The device will retry polling the request.",
		Codes: []int{70016},
	}
}

// slowDown is the poll-slower response. MSAL retries it exactly like authorization_pending
// (refs/msal-go/apps/internal/oauth/oauth.go:302-328), even though the Entra table documents only
// authorization_pending, authorization_declined, bad_verification_code and expired_token
// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:91-96).
func slowDown() *Error {
	return &Error{
		Code:        "slow_down",
		Description: "Polling too frequently; increase the polling interval (RFC 8628 section 3.5).",
	}
}

// badVerificationCode is served for a device_code the server does not know
// (refs/entra/docs/identity-platform/v2-oauth2-device-code.md:95).
func badVerificationCode() *Error {
	return &Error{
		Code:        "bad_verification_code",
		Description: "The device_code sent to the /token endpoint wasn't recognized.",
	}
}

func expiredDeviceCode(reason string) *Error {
	return &Error{
		Code:        "expired_token",
		Description: "The device code is no longer usable: " + reason + ". Start a new sign-in request.",
	}
}

// invalidRequest is served for a malformed request.
func invalidRequest(description string) *Error {
	return &Error{Code: "invalid_request", Description: description}
}

// unsupportedGrantType is served for a grant_type the token endpoint does not implement
// (refs/entra/docs/identity-platform/reference-error-codes.md:65).
func unsupportedGrantType(grantType string) *Error {
	return &Error{
		Code:        "unsupported_grant_type",
		Description: "the authorization server does not support the authorization grant type " + grantType,
	}
}

// invalidGrant builds an invalid_grant response with a specific description.
func invalidGrant(description string) *Error {
	return &Error{Code: "invalid_grant", Description: description}
}

// methodNotAllowed is served when a route is called with the wrong verb. MSAL's authorization
// endpoint is the exception: it is only ever string-built, never called
// (refs/msal-go/apps/internal/base/base.go:287-332).
func methodNotAllowed(method string) *Error {
	return &Error{
		Code:        "invalid_request",
		Description: "this endpoint requires " + method,
		Status:      http.StatusMethodNotAllowed,
	}
}
