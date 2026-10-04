// Package auth wraps MSAL Go: profiles, the interactive and device-code flows,
// silent acquisition, the token stores and the friendly errors PLAN.md requires.
//
// Design points that come from the Phase 1 spike and PLAN.md:
//
//   - Silent refresh is ours to call, once per operation, through Silent/Token.
//   - The MSAL cache is exported by MSAL only after a *successful network
//     acquisition*, so we record "last refresh" ourselves for the status output
//     (refs/msal-go/apps/internal/base/base.go:545-573).
//   - The feature matrix comes from the token's `scp` claim, which lists every
//     scope consented for the app, not just the requested subset
//     (docs/spike/phase1.md:17).
//   - `TEAMS_ACCESS_TOKEN` short-circuits everything: it is a decode-only check
//     on an unsigned token, so it catches the common mistakes and nothing else.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AzureAD/microsoft-authentication-extensions-for-go/cache"
	msalerrors "github.com/AzureAD/microsoft-authentication-library-for-go/apps/errors"
	"github.com/AzureAD/microsoft-authentication-library-for-go/apps/public"

	"github.com/floriscornel/teams-cli/internal/auth/tokenstore"
	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/cloud"
	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
)

// tokenFreshness is the margin we keep when reusing an access token in-process.
// MSAL itself refuses cached tokens within 5 minutes of expiry
// (refs/msal-go/apps/internal/base/storage/items.go:138), so matching that
// number keeps one command from refreshing twice for no reason.
const tokenFreshness = 5 * time.Minute

// Options configures a Client.
type Options struct {
	// Effective is the resolved profile.
	Effective config.Effective
	// Paths is the on-disk layout for that profile.
	Paths store.Paths
	// Clock defaults to the system clock.
	Clock clock.Clock
	// Environ defaults to os.Environ(); TEAMS_ACCESS_TOKEN is read from it.
	Environ []string
	// HTTPClient is MSAL's HTTP client. Tests point it at fakeidp. A nil value
	// means "use the default client"; it must stay a concrete *http.Client so
	// that an unset field is a real nil rather than a typed-nil interface, which
	// MSAL would dereference on the first request (see msalHTTPClientFor).
	HTTPClient *http.Client
	// DisableInstanceDiscovery turns off the discovery round trip, which the
	// fake identity provider needs (its host is not a trusted authority).
	DisableInstanceDiscovery bool
	// Authority overrides the MSAL authority URL. Tests point it at fakeidp; in
	// production it is derived from the profile's cloud and tenant, and there is
	// no flag or environment variable for it.
	Authority string
	// OpenURL opens the authorization URL in a browser. It defaults to MSAL's
	// own browser opener; tests pass fakeidp's in-process browser, and a future
	// `--no-browser` flag would print the URL instead
	// (refs/msal-go/apps/public/public.go:645-658).
	OpenURL func(url string) error
	// Store overrides the token store (tests, and `auth export`).
	Store tokenstore.Store
	// Keyring overrides the OS keychain (tests use an in-memory one).
	Keyring tokenstore.Keyring
}

// Client is the profile's auth session.
type Client struct {
	eff     config.Effective
	paths   store.Paths
	clk     clock.Clock
	store   tokenstore.Store
	msal    public.Client
	hasMSAL bool
	openURL func(url string) error

	staticToken  string
	staticClaims Claims

	mu       sync.Mutex
	cached   *public.AuthResult
	metadata Metadata
}

// New prepares the auth session for a profile.
func New(_ context.Context, opts Options) (*Client, error) {
	clk := opts.Clock
	if clk == nil {
		clk = clock.New()
	}
	c := &Client{
		eff:      opts.Effective,
		paths:    opts.Paths,
		clk:      clk,
		openURL:  opts.OpenURL,
		metadata: LoadMetadata(opts.Paths.AuthMetadataFile()),
	}

	if raw := strings.TrimSpace(config.EnvironValue(opts.Environ, config.EnvAccessToken)); raw != "" {
		claims, err := ValidateAccessToken(raw, []string{
			"https://graph.microsoft.com",
			opts.Effective.GraphBaseURL,
		}, clk.Now())
		if err != nil {
			return nil, output.WithHint(output.Authf("TEAMS_ACCESS_TOKEN is not usable: %v", err),
				"unset TEAMS_ACCESS_TOKEN or paste a fresh Graph access token; note this is a decode-only check on an unsigned token, not a security control")
		}
		c.staticToken = raw
		c.staticClaims = claims
		return c, nil
	}

	st := opts.Store
	if st == nil {
		var err error
		if st, err = tokenstore.Open(opts.Paths, opts.Effective, opts.Keyring); err != nil {
			return nil, err
		}
	}
	c.store = st

	// msal-ext's second argument is a timestamp file, not a lock: the lock lives
	// at that path plus ".lockfile" (refs/msal-ext/cache/cache.go:52-58).
	cacheAccessor, err := cache.New(st, opts.Paths.SyncFile())
	if err != nil {
		return nil, fmt.Errorf("open the token cache: %w", err)
	}
	authority := c.Authority()
	if opts.Authority != "" {
		authority = opts.Authority
	}
	msalOpts := []public.Option{
		public.WithAuthority(authority),
		public.WithCache(cacheAccessor),
		// cp1 asks for a CAE-capable token: the spike's tokens then lived 24 h
		// with RefreshOn at about 12 h (docs/spike/phase1.md:18).
		public.WithClientCapabilities([]string{"cp1"}),
	}
	msalOpts = append(msalOpts, public.WithHTTPClient(msalHTTPClientFor(opts.HTTPClient)))
	if opts.DisableInstanceDiscovery {
		msalOpts = append(msalOpts, public.WithInstanceDiscovery(false))
	}
	pca, err := public.New(c.eff.ClientID, msalOpts...)
	if err != nil {
		return nil, fmt.Errorf("initialize MSAL: %w", err)
	}
	c.msal = pca
	c.hasMSAL = true
	return c, nil
}

// msalHTTPClientFor never returns a nil client.
//
// MSAL's comm.New (refs/msal-go/apps/internal/oauth/ops/internal/comm/comm.go:45-47)
// keeps whatever it is given and calls Do on it, so a typed-nil
// *http.Client — which is what an unset `Hooks.AuthHTTPClient` field becomes
// when it is stored in an interface — panics with a nil pointer dereference on
// the very first request, before any browser or device code is shown. Resolving
// the default here removes the possibility instead of guarding each call site.
func msalHTTPClientFor(hc *http.Client) *http.Client {
	if hc == nil {
		return http.DefaultClient
	}
	return hc
}

// Authority is the MSAL authority URL for the profile's tenant.
func (c *Client) Authority() string {
	endpoints, ok := cloud.Lookup(c.eff.Cloud)
	if !ok {
		endpoints = cloud.MustLookup(config.DefaultCloud)
	}
	return endpoints.Authority(c.eff.Tenant)
}

// Effective returns the resolved profile.
func (c *Client) Effective() config.Effective { return c.eff }

// Paths returns the on-disk layout.
func (c *Client) Paths() store.Paths { return c.paths }

// Store returns the token store (nil when TEAMS_ACCESS_TOKEN is in use).
func (c *Client) Store() tokenstore.Store { return c.store }

// Static reports whether the session uses the TEAMS_ACCESS_TOKEN escape hatch.
func (c *Client) Static() bool { return c.staticToken != "" }

// StaticClaims returns the decoded claims of the static token.
func (c *Client) StaticClaims() Claims { return c.staticClaims }

// Metadata returns our refresh bookkeeping.
func (c *Client) Metadata() Metadata { return c.metadata }

// Result is a successful acquisition, reduced to what the CLI shows.
type Result struct {
	Account       string
	HomeAccountID string
	TenantID      string
	Scopes        []string
	ExpiresOn     time.Time
	// FromCache reports that no network call was needed.
	FromCache bool
}

// LoginOptions configures Login.
type LoginOptions struct {
	// Device forces the device-code flow.
	Device bool
	// Scopes defaults to the profile's scopes.
	Scopes []string
	// Interactive reports whether prompting is allowed at all. When it is false
	// and Device is not set, Login fails with a usage error instead of blocking.
	Interactive bool
	// OnDeviceCode is called with the code and URL; the CLI prints it.
	OnDeviceCode func(DeviceCode)
	// OnFallback is called when the browser flow fails with AADSTS50011 and we
	// retry with device code (PLAN.md:110).
	OnFallback func(err error)
}

// DeviceCode carries what the user has to type.
type DeviceCode struct {
	UserCode        string
	VerificationURL string
	Message         string
	ExpiresOn       time.Time
}

// Login signs in. It uses the browser (auth code + PKCE) unless Device is set,
// and it falls back to device code when Entra rejects the redirect URI, which is
// the AADSTS50011 the spike hit while the app registration lacked
// http://localhost.
func (c *Client) Login(ctx context.Context, opts LoginOptions) (Result, error) {
	if c.Static() {
		return Result{}, output.Usagef("TEAMS_ACCESS_TOKEN is set, so there is nothing to sign in to (unset it first)")
	}
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = c.eff.Scopes
	}
	if opts.Device {
		res, err := c.loginDevice(ctx, scopes, opts)
		if err != nil {
			return Result{}, Classify(err, c.eff.Name)
		}
		return res, nil
	}
	if !opts.Interactive {
		return Result{}, output.WithHint(output.Usagef("no terminal for an interactive sign-in"),
			"run `teams auth login --device` (or set TEAMS_NO_INPUT only when you really want no prompt)")
	}
	res, err := c.loginInteractive(ctx, scopes)
	if err == nil {
		return res, nil
	}
	if !IsRedirectURIError(err) {
		return Result{}, Classify(err, c.eff.Name)
	}
	// Fall back automatically, with the fix in the message.
	if opts.OnFallback != nil {
		opts.OnFallback(err)
	}
	res, devErr := c.loginDevice(ctx, scopes, opts)
	if devErr != nil {
		return Result{}, Classify(devErr, c.eff.Name)
	}
	return res, nil
}

func (c *Client) loginInteractive(ctx context.Context, scopes []string) (Result, error) {
	interactiveOpts := []public.AcquireInteractiveOption{}
	if c.openURL != nil {
		interactiveOpts = append(interactiveOpts, public.WithOpenURL(c.openURL))
	}
	res, err := c.msal.AcquireTokenInteractive(ctx, scopes, interactiveOpts...)
	if err != nil {
		return Result{}, err
	}
	return c.finish(res)
}

func (c *Client) loginDevice(ctx context.Context, scopes []string, opts LoginOptions) (Result, error) {
	dc, err := c.msal.AcquireTokenByDeviceCode(ctx, scopes)
	if err != nil {
		return Result{}, err
	}
	if opts.OnDeviceCode != nil {
		opts.OnDeviceCode(DeviceCode{
			UserCode:        dc.Result.UserCode,
			VerificationURL: dc.Result.VerificationURL,
			Message:         dc.Result.Message,
			ExpiresOn:       dc.Result.ExpiresOn,
		})
	}
	res, err := dc.AuthenticationResult(ctx)
	if err != nil {
		return Result{}, err
	}
	return c.finish(res)
}

// finish records the acquisition in our metadata and caches it for this run.
func (c *Client) finish(res public.AuthResult) (Result, error) {
	out := toResult(res)
	c.mu.Lock()
	c.cached = &res
	if res.Metadata.TokenSource == public.TokenSourceIdentityProvider {
		// Only a real network acquisition renews the refresh token, so this is
		// the only place the age we report can move.
		c.metadata.LastRefresh = c.clk.Now()
	}
	c.metadata.Profile = c.eff.Name
	c.metadata.HomeAccountID = firstNonEmpty(res.Account.HomeAccountID, c.metadata.HomeAccountID)
	c.metadata.Account = firstNonEmpty(res.Account.PreferredUsername, c.metadata.Account)
	c.metadata.TenantID = firstNonEmpty(res.IDToken.TenantID, c.metadata.TenantID)
	c.metadata.Scopes = out.Scopes
	m := c.metadata
	c.mu.Unlock()
	if err := SaveMetadata(c.paths.AuthMetadataFile(), m); err != nil {
		return out, err
	}
	return out, nil
}

func toResult(res public.AuthResult) Result {
	return Result{
		Account:       res.Account.PreferredUsername,
		HomeAccountID: res.Account.HomeAccountID,
		TenantID:      res.IDToken.TenantID,
		Scopes:        res.GrantedScopes,
		ExpiresOn:     res.ExpiresOn,
		FromCache:     res.Metadata.TokenSource == public.TokenSourceCache,
	}
}

// Accounts lists the cached accounts for this profile.
//
// An empty token store short-circuits: reading it would mean a keychain round
// trip (about a second on macOS) to learn what a stat(2) already told us, and a
// profile that has never signed in is the most common case for the first
// commands a user runs.
func (c *Client) Accounts(ctx context.Context) ([]public.Account, error) {
	if !c.hasMSAL || c.store == nil || c.store.Empty() {
		return nil, nil
	}
	accounts, err := c.msal.Accounts(ctx)
	if err != nil {
		return nil, Classify(err, c.eff.Name)
	}
	return accounts, nil
}

// pickAccount prefers the account we recorded (config or metadata), so a cache
// holding several accounts still resolves deterministically.
func (c *Client) pickAccount(accounts []public.Account) (public.Account, error) {
	if len(accounts) == 0 {
		return public.Account{}, output.WithHint(output.Authf("profile %s is not signed in", c.eff.Name), "run `teams auth login`")
	}
	want := firstNonEmpty(c.eff.Profile.HomeAccountID, c.metadata.HomeAccountID)
	if want == "" {
		return accounts[0], nil
	}
	for _, a := range accounts {
		if strings.EqualFold(a.HomeAccountID, want) {
			return a, nil
		}
	}
	// The recorded account is gone: fall back to the only one, otherwise make
	// the user choose rather than guessing between identities.
	if len(accounts) == 1 {
		return accounts[0], nil
	}
	return public.Account{}, output.WithHint(
		output.Authf("profile %s has %d cached accounts and none matches %s", c.eff.Name, len(accounts), want),
		"run `teams auth login` to re-record the account, or `teams auth logout` to clear the cache",
	)
}

// Silent acquires a token without prompting, refreshing only when MSAL decides
// the cached one is too close to expiry.
func (c *Client) Silent(ctx context.Context, scopes []string) (public.AuthResult, error) {
	if c.Static() {
		return public.AuthResult{}, output.WithHint(
			output.Authf("TEAMS_ACCESS_TOKEN is in use, so no silent refresh is available"),
			"unset TEAMS_ACCESS_TOKEN to use the cached account",
		)
	}
	if len(scopes) == 0 {
		scopes = c.eff.Scopes
	}
	c.mu.Lock()
	cached := c.cached
	c.mu.Unlock()
	if cached != nil && c.clk.Now().Add(tokenFreshness).Before(cached.ExpiresOn) {
		return *cached, nil
	}

	accounts, err := c.Accounts(ctx)
	if err != nil {
		return public.AuthResult{}, err
	}
	account, err := c.pickAccount(accounts)
	if err != nil {
		return public.AuthResult{}, err
	}
	res, err := c.msal.AcquireTokenSilent(ctx, scopes, public.WithSilentAccount(account))
	if err != nil {
		return public.AuthResult{}, Classify(err, c.eff.Name)
	}
	if _, err := c.finish(res); err != nil {
		return res, err
	}
	return res, nil
}

// Token implements graph.TokenSource.
func (c *Client) Token(ctx context.Context) (string, error) {
	if c.Static() {
		return c.staticToken, nil
	}
	res, err := c.Silent(ctx, c.eff.Scopes)
	if err != nil {
		return "", err
	}
	return res.AccessToken, nil
}

// GrantedScopes returns the scopes the current token carries. The `scp` claim
// lists every consented scope, so this is the feature matrix with no extra
// probing (PLAN.md:66).
func (c *Client) GrantedScopes(ctx context.Context) ([]string, error) {
	if c.Static() {
		return c.staticClaims.Scopes, nil
	}
	res, err := c.Silent(ctx, c.eff.Scopes)
	if err != nil {
		return nil, err
	}
	claims, err := ParseClaims(res.AccessToken)
	if err != nil {
		return nil, err
	}
	return claims.Scopes, nil
}

// Status summarizes the profile for `teams auth status` and `teams doctor`. A
// missing or dead account comes back as a Status plus an exit-3 error, so the
// caller can print the diagnosis before exiting.
func (c *Client) Status(ctx context.Context) (Status, error) {
	st := Status{
		Profile:         c.eff.Name,
		Tenant:          c.eff.Tenant,
		Cloud:           c.eff.Cloud,
		ClientID:        c.eff.ClientID,
		Mode:            modeOf(c.eff),
		ReadOnly:        c.eff.ReadOnly,
		RequestedScopes: c.eff.Scopes,
		ScopesSpec:      c.eff.ScopesSpec,
		GraphBaseURL:    c.eff.GraphBaseURL,
	}
	if c.store != nil {
		st.StoreKind = c.store.Kind()
		st.StoreDescription = c.store.Description()
		st.StoreWarning = c.store.Warning()
		if c.store.Empty() {
			st.StoreDescription = "not created yet"
		}
	}
	st.LastRefresh = c.metadata.LastRefresh

	if c.Static() {
		st.SignedIn = true
		st.AccessTokenFromEnv = true
		st.Account = c.staticClaims.PreferredUsername
		st.TenantID = c.staticClaims.TenantID
		st.Scopes = c.staticClaims.Scopes
		st.ExpiresOn = c.staticClaims.ExpiresOn
		return st, nil
	}

	accounts, err := c.Accounts(ctx)
	if err != nil {
		return st, err
	}
	if len(accounts) == 0 {
		return st, output.WithHint(output.Authf("profile %s is not signed in", c.eff.Name), "run `teams auth login`")
	}
	account, err := c.pickAccount(accounts)
	if err != nil {
		return st, err
	}
	st.Account = account.PreferredUsername
	st.HomeAccountID = account.HomeAccountID
	st.TenantID = account.Realm

	res, err := c.Silent(ctx, c.eff.Scopes)
	if err != nil {
		return st, err
	}
	claims, claimErr := ParseClaims(res.AccessToken)
	if claimErr != nil {
		return st, claimErr
	}
	st.SignedIn = true
	st.Scopes = claims.Scopes
	st.ExpiresOn = res.ExpiresOn
	st.FromCache = res.Metadata.TokenSource == public.TokenSourceCache
	if st.Account == "" {
		st.Account = claims.PreferredUsername
	}
	if st.TenantID == "" {
		st.TenantID = claims.TenantID
	}
	return st, nil
}

// Logout removes the account from the cache and deletes the token store
// material. MSAL only exports the cache through RemoveAccount, so signing out
// any other way would leave the refresh token on disk
// (refs/msal-go/apps/internal/base/base.go:609-622).
func (c *Client) Logout(ctx context.Context) error {
	if c.Static() {
		return output.Usagef("TEAMS_ACCESS_TOKEN is set; unset it instead of signing out")
	}
	var errs []error
	accounts, err := c.Accounts(ctx)
	if err != nil {
		errs = append(errs, err)
	}
	for _, account := range accounts {
		if err := c.msal.RemoveAccount(ctx, account); err != nil {
			errs = append(errs, fmt.Errorf("remove the cached account: %w", err))
		}
	}
	if err := store.RemoveIfExists(c.paths.AuthMetadataFile()); err != nil {
		errs = append(errs, err)
	}
	if err := store.RemoveIfExists(c.paths.SyncFile()); err != nil {
		errs = append(errs, err)
	}
	if err := store.RemoveIfExists(c.paths.SyncFile() + ".lockfile"); err != nil {
		errs = append(errs, err)
	}
	if err := c.store.Delete(ctx); err != nil {
		errs = append(errs, err)
	}
	c.mu.Lock()
	c.cached = nil
	c.metadata = Metadata{Version: metadataVersion, Profile: c.eff.Name}
	c.mu.Unlock()
	return errors.Join(errs...)
}

// Status is the profile's auth state.
type Status struct {
	Profile            string
	Account            string
	HomeAccountID      string
	Tenant             string
	TenantID           string
	Cloud              string
	ClientID           string
	Mode               string
	ReadOnly           bool
	SignedIn           bool
	AccessTokenFromEnv bool
	Scopes             []string
	RequestedScopes    []string
	ScopesSpec         string
	ExpiresOn          time.Time
	LastRefresh        time.Time
	FromCache          bool
	StoreKind          string
	StoreDescription   string
	StoreWarning       string
	GraphBaseURL       string
}

// RefreshAge is how long ago the refresh token was last renewed.
func (s Status) RefreshAge(now time.Time) time.Duration {
	if s.LastRefresh.IsZero() {
		return 0
	}
	return now.Sub(s.LastRefresh)
}

func modeOf(eff config.Effective) string { return eff.Mode() }

// MSALVerbose returns MSAL's verbose rendering of an error, which includes the
// request and response. It is only used behind -v, and its output can contain
// tokens, so it is never printed by default.
func MSALVerbose(err error) string {
	if err == nil {
		return ""
	}
	return msalerrors.Verbose(err)
}
