package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/auth"
	"github.com/floriscornel/teams-cli/internal/cloud"
	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
)

// newAuthCmd implements `teams auth login|status|logout`.
//
// `auth refresh` and `auth export` are Phase 5 (the bot/headless work): refresh
// only makes sense once a scheduled job feeds it, and export needs a second
// store to export to.
func (a *App) newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Sign in, inspect the session and sign out",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newAuthLoginCmd(), a.newAuthStatusCmd(), a.newAuthLogoutCmd())
	return cmd
}

func (a *App) newAuthLoginCmd() *cobra.Command {
	var (
		device bool
		scopes string
	)
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Microsoft Teams with a browser or a device code",
		Long: "Sign in with the authorization code + PKCE flow in your browser, or with a\n" +
			"device code (`--device`) on a headless machine. When no terminal is available\n" +
			"the device-code flow is chosen automatically.\n\n" +
			"The MSAL token cache is written to the profile's token store: encrypted with a\n" +
			"key from your OS keychain by default, or a 0600 plaintext file where no\n" +
			"keychain is reachable.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			client, err := a.Auth(ctx)
			if err != nil {
				return err
			}
			eff, err := a.Effective()
			if err != nil {
				return err
			}
			requested, err := config.Scopes.For(scopes)
			if err != nil {
				return output.WithHint(output.Usagef("%v", err), "use a preset (chats, read-only, full) or a space separated scope list")
			}
			if scopes == "" {
				requested = eff.Scopes
			}
			a.Printer.Statusf("signing in to %s as profile %s", eff.Tenant, eff.Name)
			a.Printer.Statusf("requesting scopes: %s", strings.Join(requested, " "))

			result, err := client.Login(ctx, auth.LoginOptions{
				Device:      device,
				Scopes:      requested,
				Interactive: a.Printer.Interactive(),
				OnDeviceCode: func(dc auth.DeviceCode) {
					// MSAL prints the whole message including the URL; keep it
					// verbatim and add the documented 15-minute window.
					if dc.Message != "" {
						a.Printer.Statusf("%s", dc.Message)
					} else {
						a.Printer.Statusf("open %s and enter the code %s", dc.VerificationURL, dc.UserCode)
					}
					if !dc.ExpiresOn.IsZero() {
						a.Printer.Statusf("the code is valid for %s (until %s)",
							output.HumanDurationSeconds(dc.ExpiresOn.Sub(a.Clock.Now())),
							dc.ExpiresOn.Local().Format("15:04:05"))
					}
				},
				OnFallback: func(cause error) {
					a.Printer.Warnf("the browser sign-in failed (%v)", cause)
					a.Printer.Statusf("falling back to a device code; ask your Entra admin to register http://localhost as a Mobile and desktop redirect URI to fix this permanently")
				},
			})
			if err != nil {
				return err
			}
			if err := a.rememberAccount(result); err != nil {
				a.Printer.Warnf("could not record the account in the config file: %v", err)
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]any{
					"profile":         eff.Name,
					"account":         result.Account,
					"home_account_id": result.HomeAccountID,
					"tenant_id":       result.TenantID,
					"scopes":          result.Scopes,
					"expires_on":      result.ExpiresOn.UTC().Format("2006-01-02T15:04:05Z07:00"),
				})
			}
			a.Printer.Successf("signed in as %s", result.Account)
			a.Printer.Definitions([][2]string{
				{"profile", eff.Name},
				{"tenant", eff.Tenant},
				{"expires", output.HumanAge(a.Clock.Now(), result.ExpiresOn) + " (" + result.ExpiresOn.Local().Format("15:04") + ")"},
				{"store", storeDescription(client)},
			})
			a.Printer.Println(a.grantedScopeSummary(result.Scopes))
			return nil
		},
	}
	cmd.Flags().BoolVar(&device, "device", false, "use the device-code flow instead of the browser")
	cmd.Flags().StringVar(&scopes, "scopes", "", "override the profile's scopes (a preset name or a space separated list)")
	return cmd
}

// rememberAccount persists the account key in the profile, so silent acquisition
// has an account without a full cache read (PLAN.md:92).
func (a *App) rememberAccount(result auth.Result) error {
	if result.HomeAccountID == "" {
		return nil
	}
	cfg, err := a.Config()
	if err != nil {
		return err
	}
	eff, err := a.Effective()
	if err != nil {
		return err
	}
	profile := cfg.Profiles[eff.Name]
	if profile.HomeAccountID == result.HomeAccountID {
		return nil
	}
	profile.HomeAccountID = result.HomeAccountID
	if profile.Tenant == "" {
		profile.Tenant = eff.Profile.Tenant
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]config.Profile{}
	}
	cfg.Profiles[eff.Name] = profile
	return cfg.Save(a.ConfigPath())
}

func storeDescription(client *auth.Client) string {
	if st := client.Store(); st != nil {
		return st.Kind() + " (" + st.Description() + ")"
	}
	return "TEAMS_ACCESS_TOKEN"
}

// grantedScopeSummary lists the granted scopes and points out which presets'
// scopes are still missing, because the `scp` claim is the whole feature matrix.
func (a *App) grantedScopeSummary(granted []string) string {
	parts := []string{"granted scopes: " + strings.Join(sortedCopy(granted), " ")}
	for _, preset := range config.Scopes.Names() {
		missing := config.Scopes.Missing(granted, config.Scopes.Preset(preset))
		if len(missing) == 0 {
			parts = append(parts, fmt.Sprintf("preset %q: complete", preset))
			continue
		}
		parts = append(parts, fmt.Sprintf("preset %q: missing %s", preset, strings.Join(missing, ", ")))
	}
	return strings.Join(parts, "\n")
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

type statusPayload struct {
	Profile         string   `json:"profile"`
	SignedIn        bool     `json:"signed_in"`
	Account         string   `json:"account,omitempty"`
	HomeAccountID   string   `json:"home_account_id,omitempty"`
	Tenant          string   `json:"tenant"`
	TenantID        string   `json:"tenant_id,omitempty"`
	Cloud           string   `json:"cloud"`
	ClientID        string   `json:"client_id"`
	Mode            string   `json:"mode"`
	ScopesSpec      string   `json:"scopes_spec,omitempty"`
	RequestedScopes []string `json:"requested_scopes"`
	GrantedScopes   []string `json:"granted_scopes,omitempty"`
	ExpiresOn       string   `json:"expires_on,omitempty"`
	LastRefresh     string   `json:"last_refresh,omitempty"`
	RefreshAge      string   `json:"refresh_age,omitempty"`
	TokenFromEnv    bool     `json:"access_token_from_env"`
	TokenStore      string   `json:"token_store"`
	TokenStorePath  string   `json:"token_store_path,omitempty"`
	TokenStoreWarn  string   `json:"token_store_warning,omitempty"`
	AdminConsent    []string `json:"scopes_needing_admin_consent,omitempty"`
	RefreshWarning  string   `json:"refresh_warning,omitempty"`
	GraphBaseURL    string   `json:"graph_base_url"`
}

func (a *App) newAuthStatusCmd() *cobra.Command {
	var adminRequest bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the signed-in account, its scopes and the token store",
		Long: "Show who you are signed in as, which scopes the current token carries (the\n" +
			"`scp` claim lists every consented scope), and where the token cache lives.\n\n" +
			"Exits 3 when the profile is not signed in or the refresh token is dead.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			client, err := a.Auth(ctx)
			if err != nil {
				return err
			}
			status, statusErr := client.Status(ctx)
			payload := a.statusPayload(client, status)
			if a.Printer.JSONMode() {
				if err := a.Printer.JSON(payload); err != nil {
					return err
				}
				return statusErr
			}
			a.printStatus(payload, status)
			if adminRequest {
				a.Printer.Println()
				adminReq, err := a.adminRequestText(status)
				if err != nil {
					return err
				}
				a.Printer.Println(adminReq)
			}
			return statusErr
		},
	}
	cmd.Flags().BoolVar(&adminRequest, "admin-request", false, "print the admin-consent and redirect-URI changes to request")
	return cmd
}

func (a *App) statusPayload(client *auth.Client, status auth.Status) statusPayload {
	now := a.Clock.Now()
	payload := statusPayload{
		Profile:         status.Profile,
		SignedIn:        status.SignedIn,
		Account:         status.Account,
		HomeAccountID:   status.HomeAccountID,
		Tenant:          status.Tenant,
		TenantID:        status.TenantID,
		Cloud:           status.Cloud,
		ClientID:        status.ClientID,
		Mode:            status.Mode,
		ScopesSpec:      status.ScopesSpec,
		RequestedScopes: status.RequestedScopes,
		GrantedScopes:   sortedCopy(status.Scopes),
		TokenFromEnv:    status.AccessTokenFromEnv,
		TokenStore:      status.StoreKind,
		TokenStorePath:  status.StoreDescription,
		TokenStoreWarn:  status.StoreWarning,
		GraphBaseURL:    status.GraphBaseURL,
	}
	if !status.ExpiresOn.IsZero() {
		payload.ExpiresOn = output.HumanAge(now, status.ExpiresOn)
	}
	if !status.LastRefresh.IsZero() {
		payload.LastRefresh = status.LastRefresh.UTC().Format("2006-01-02T15:04:05Z07:00")
		payload.RefreshAge = output.HumanDuration(client.Metadata().RefreshAge(now))
		payload.RefreshWarning = client.Metadata().RefreshWarning(now)
	}
	seen := map[string]bool{}
	for _, scope := range append(append([]string(nil), status.Scopes...), status.RequestedScopes...) {
		if config.Scopes.RequiresAdminConsent(scope) && !seen[scope] {
			seen[scope] = true
			payload.AdminConsent = append(payload.AdminConsent, scope)
		}
	}
	sort.Strings(payload.AdminConsent)
	return payload
}

func (a *App) printStatus(payload statusPayload, status auth.Status) {
	signedIn := "no"
	if payload.SignedIn {
		signedIn = "yes"
	}
	pairs := [][2]string{
		{"profile", payload.Profile},
		{"signed in", signedIn},
	}
	if payload.Account != "" {
		pairs = append(pairs, [2]string{"account", payload.Account})
	}
	pairs = append(pairs,
		[2]string{"tenant", payload.Tenant},
		[2]string{"client id", payload.ClientID},
		[2]string{"cloud", payload.Cloud},
		[2]string{"mode", payload.Mode},
	)
	if payload.TokenFromEnv {
		pairs = append(pairs, [2]string{"token", "TEAMS_ACCESS_TOKEN (decode-only check; scopes are not verified)"})
	} else {
		pairs = append(pairs, [2]string{"token store", payload.TokenStore + " (" + payload.TokenStorePath + ")"})
	}
	if payload.ExpiresOn != "" {
		pairs = append(pairs, [2]string{"access token expires", payload.ExpiresOn})
	}
	if payload.RefreshAge != "" {
		pairs = append(pairs, [2]string{"refresh token renewed", payload.RefreshAge + " ago"})
	}
	pairs = append(pairs,
		[2]string{"requested scopes", strings.Join(payload.RequestedScopes, " ")},
		[2]string{"granted scopes", strings.Join(payload.GrantedScopes, " ")},
	)
	if payload.TokenStoreWarn != "" {
		pairs = append(pairs, [2]string{"token store warning", payload.TokenStoreWarn})
	}
	a.Printer.Definitions(pairs)
	if payload.RefreshWarning != "" {
		a.Printer.Warnf("%s", payload.RefreshWarning)
	}
	if len(payload.GrantedScopes) > 0 {
		a.Printer.Println()
		a.Printer.Println(a.grantedScopeSummary(payload.GrantedScopes))
	}
	if !status.SignedIn {
		a.Printer.Statusf("not signed in: run `teams auth login`")
	}
}

// adminRequestText prints the exact changes an Entra admin has to make, ready to
// paste into a ticket (PLAN.md: `auth status --admin-request`). The admin-consent
// endpoint accepts dynamic scopes
// (refs/entra/docs/identity-platform/v2-admin-consent.md:26-44).
func (a *App) adminRequestText(status auth.Status) (string, error) {
	eff, err := a.Effective()
	if err != nil {
		return "", err
	}
	endpoints, _ := cloud.Lookup(eff.Cloud)
	adminURL := fmt.Sprintf("%s/adminconsent?client_id=%s&redirect_uri=%s&scope=%s",
		endpoints.Authority(eff.Tenant), eff.ClientID,
		"http%3A%2F%2Flocalhost",
		strings.ReplaceAll(strings.Join(status.RequestedScopes, "+"), " ", "+"))

	missing := config.Scopes.Missing(status.Scopes, eff.Scopes)
	var b strings.Builder
	b.WriteString("Admin request for the app registration " + eff.ClientID + " (tenant " + eff.Tenant + "):\n")
	b.WriteString("  1. Add http://localhost as a \"Mobile and desktop applications\" redirect URI\n")
	b.WriteString("     (MSAL listens on an ephemeral localhost port; Entra ignores the port for localhost).\n")
	b.WriteString("  2. Enable \"Allow public client flows\" so the device-code flow works.\n")
	if len(missing) > 0 {
		b.WriteString("  3. Grant admin consent for: " + strings.Join(missing, " ") + "\n")
	} else {
		b.WriteString("  3. Grant admin consent for: " + strings.Join(status.RequestedScopes, " ") + "\n")
	}
	needingAdmin := make([]string, 0, len(status.RequestedScopes))
	for _, scope := range status.RequestedScopes {
		if config.Scopes.RequiresAdminConsent(scope) {
			needingAdmin = append(needingAdmin, scope)
		}
	}
	if len(needingAdmin) > 0 {
		b.WriteString("     Documented as needing admin consent: " + strings.Join(needingAdmin, " ") + "\n")
	}
	b.WriteString("     Consent URL: " + adminURL + "\n")
	b.WriteString("  4. Add an owner to the registration so future changes need no ticket.")
	return b.String(), nil
}

func (a *App) newAuthLogoutCmd() *cobra.Command {
	var (
		all bool
		yes bool
	)
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Sign out and delete the cached tokens",
		Long: "Remove the cached account and delete the token store for the profile.\n\n" +
			"With --all, sign out of every configured profile: this is the \"forget\n" +
			"everything\" command, and it is the only one that deletes token material.\n" +
			"`teams cache clear` never does.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if all {
				return a.logoutAllProfiles(ctx, yes)
			}
			client, err := a.Auth(ctx)
			if err != nil {
				return err
			}
			eff, err := a.Effective()
			if err != nil {
				return err
			}
			if err := a.confirm(fmt.Sprintf("sign out of profile %s and delete its cached tokens?", eff.Name), yes); err != nil {
				return err
			}
			if err := client.Logout(ctx); err != nil {
				return output.Errorf("%v", err)
			}
			a.Printer.Successf("signed out of profile %s", eff.Name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "sign out of every configured profile")
	cmd.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	return cmd
}

func (a *App) logoutAllProfiles(ctx context.Context, yes bool) error {
	cfg, err := a.Config()
	if err != nil {
		return err
	}
	names := cfg.ProfileNames()
	if len(names) == 0 {
		names = []string{config.DefaultProfileName}
	}
	if err := a.confirm(fmt.Sprintf("sign out of all %d configured profiles and delete their cached tokens?", len(names)), yes); err != nil {
		return err
	}
	var failed []string
	for _, name := range names {
		profileApp := a.forProfile(name)
		client, err := profileApp.Auth(ctx)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		if err := client.Logout(ctx); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		a.Printer.Statusf("signed out of profile %s", name)
	}
	if len(failed) > 0 {
		return output.Errorf("could not sign out of: %s", strings.Join(failed, "; "))
	}
	a.Printer.Successf("signed out of all profiles")
	return nil
}

// forProfile returns a copy of the App bound to another profile.
func (a *App) forProfile(name string) *App {
	clone := *a
	clone.profileFlag = name
	clone.effective = nil
	clone.paths = nil
	clone.authc = nil
	clone.graphc = nil
	clone.graphErr = nil
	return &clone
}
