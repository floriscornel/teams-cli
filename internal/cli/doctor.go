package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
)

// clockSkewTolerance is how far the local clock may differ from Graph's before
// doctor complains. Token lifetimes and `--since` windows are both clock-based,
// and the device-code window is only 15 minutes.
const clockSkewTolerance = 5 * time.Minute

// doctorCheck is one line of the report. Status is "ok", "warn" or "fail";
// everything except a fail keeps exit code 0, because a warning is information,
// not an error.
type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
	Code   int    `json:"-"`
}

const (
	statusOK   = "ok"
	statusWarn = "warn"
	statusFail = "fail"
)

func (a *App) newDoctorCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the config, token store, scopes, clock and Graph reachability",
		Long: "Diagnose the local setup and print the fix for every problem.\n\n" +
			"doctor checks the config file, the token store (including keychain\n" +
			"reachability), the scopes the current token carries against the feature\n" +
			"matrix, the clock skew against Graph's own clock, and Graph reachability.\n" +
			"It never makes a write call; --offline skips the network checks entirely.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			checks := a.runDoctor(ctx, offline)
			if a.Printer.JSONMode() {
				if err := a.Printer.JSON(map[string]any{"checks": checks, "offline": offline}); err != nil {
					return err
				}
			} else {
				rows := make([][]string, 0, len(checks))
				for _, c := range checks {
					rows = append(rows, []string{c.Status, c.Name, c.Detail})
				}
				a.Printer.Table([]string{"STATUS", "CHECK", "DETAIL"}, rows)
				for _, c := range checks {
					if c.Fix != "" && c.Status != statusOK {
						a.Printer.Statusf("fix (%s): %s", c.Name, c.Fix)
					}
				}
			}
			// The exit code is the worst failure, so a script can gate on doctor.
			worst := 0
			for _, c := range checks {
				if c.Status == statusFail && c.Code > worst {
					worst = c.Code
				}
			}
			if worst != 0 {
				return output.WithCode(output.Errorf("doctor found problems"), worst)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "skip the checks that need the network")
	return cmd
}

func (a *App) runDoctor(ctx context.Context, offline bool) []doctorCheck {
	var checks []doctorCheck

	// 1. Config file.
	configCheck := doctorCheck{Name: "config file", Status: statusOK, Detail: a.ConfigPath()}
	if _, err := a.Config(); err != nil {
		configCheck.Status = statusFail
		configCheck.Detail = err.Error()
		configCheck.Fix = output.HintOf(err)
		configCheck.Code = output.CodeOf(err)
		checks = append(checks, configCheck)
		return checks // nothing else can be resolved without a readable config
	}
	if !a.ConfigFileExists() {
		configCheck.Status = statusWarn
		configCheck.Detail += " (not created yet; defaults are in use)"
		configCheck.Fix = "run `teams auth login` or `teams config set ...` to create it"
	}
	checks = append(checks, configCheck)

	// 2. Profile resolution.
	eff, err := a.Effective()
	profileCheck := doctorCheck{Name: "profile", Status: statusOK}
	if err != nil {
		profileCheck.Status = statusFail
		profileCheck.Detail = err.Error()
		profileCheck.Fix = output.HintOf(err)
		profileCheck.Code = output.CodeOf(err)
		checks = append(checks, profileCheck)
		return checks
	}
	profileCheck.Detail = eff.Name + " -> tenant " + eff.Tenant + ", cloud " + eff.Cloud + ", mode " + eff.Mode()
	if eff.ClientID == config.DefaultClientID {
		profileCheck.Status = statusWarn
		profileCheck.Fix = "the Microsoft Graph CLI Tools client is often blocked by tenant policy; register your own public client and set `client_id`"
	}
	checks = append(checks, profileCheck)

	// 3. Token store and keychain reachability.
	storeCheck := doctorCheck{Name: "token store", Status: statusOK}
	if eff.TokenStore == "keyvault://" || strings.HasPrefix(eff.TokenStore, "keyvault://") {
		storeCheck.Status = statusWarn
		storeCheck.Detail = eff.TokenStore
		storeCheck.Fix = "the Key Vault backend arrives in Phase 5; use auto or file for now"
		checks = append(checks, storeCheck)
	} else {
		client, err := a.Auth(ctx)
		switch {
		case err != nil:
			storeCheck.Status = statusFail
			storeCheck.Detail = err.Error()
			storeCheck.Fix = output.HintOf(err)
			storeCheck.Code = output.CodeOf(err)
		default:
			if st := client.Store(); st != nil {
				if prober, ok := st.(interface{ Probe() error }); ok {
					// doctor is exactly the command that should pay for a
					// keychain round trip: it reports keychain reachability.
					if err := prober.Probe(); err != nil {
						storeCheck.Status = statusFail
						storeCheck.Detail = err.Error()
						storeCheck.Fix = "the ciphertext exists but its key does not; run `teams auth login` again"
						storeCheck.Code = output.CodeOf(err)
					}
				}
				storeCheck.Detail = st.Kind() + " (" + st.Description() + ")"
				if st.Kind() != "envelope" && storeCheck.Status == statusOK {
					storeCheck.Status = statusWarn
					storeCheck.Fix = "the token cache is not encrypted here; install/unlock a keychain (Secret Service on Linux) to get the envelope store"
				}
				if st.Warning() != "" && storeCheck.Status == statusOK {
					storeCheck.Status = statusWarn
					storeCheck.Fix = st.Warning()
				}
			} else {
				storeCheck.Detail = "TEAMS_ACCESS_TOKEN is set; the token store is unused"
			}
		}
		checks = append(checks, storeCheck)
	}

	// 4. Sign-in and granted scopes.
	authCheck := doctorCheck{Name: "sign-in", Status: statusOK}
	var granted []string
	client, authErr := a.Auth(ctx)
	switch {
	case authErr != nil:
		// The token store could not even be opened (a Key Vault profile, a
		// broken keychain): the sign-in row has to say so rather than look fine.
		authCheck.Status = statusFail
		authCheck.Detail = authErr.Error()
		authCheck.Fix = output.HintOf(authErr)
		authCheck.Code = output.CodeOf(authErr)
	default:
		status, statusErr := client.Status(ctx)
		switch {
		case statusErr != nil:
			authCheck.Status = statusFail
			authCheck.Detail = statusErr.Error()
			authCheck.Fix = output.HintOf(statusErr)
			authCheck.Code = output.CodeOf(statusErr)
		case status.AccessTokenFromEnv:
			authCheck.Detail = "TEAMS_ACCESS_TOKEN is set for " + eff.Name
			authCheck.Status = statusWarn
			authCheck.Fix = "unset TEAMS_ACCESS_TOKEN to use the cached account and the scope pre-checks"
		default:
			authCheck.Detail = "signed in as " + status.Account + " (token store " + status.StoreKind + ")"
			if warning := client.Metadata().RefreshWarning(a.Clock.Now()); warning != "" {
				authCheck.Status = statusWarn
				authCheck.Fix = warning
			}
		}
		granted = status.Scopes
	}
	checks = append(checks, authCheck)

	// 5. Feature matrix against the token's scopes.
	checks = append(checks, a.featureChecks(granted, a.verboseFlag > 0)...)

	// 6. Graph reachability and clock skew (read-only calls).
	if offline {
		checks = append(checks, doctorCheck{Name: "graph", Status: statusWarn, Detail: "skipped (--offline)"})
		return checks
	}
	if _, err := a.Graph(ctx); err != nil {
		checks = append(checks, doctorCheck{
			Name: "graph", Status: statusFail, Detail: err.Error(), Fix: output.HintOf(err), Code: output.CodeOf(err),
		})
		return checks
	}
	reachCheck, skewCheck := a.probeGraph(ctx)
	checks = append(checks, reachCheck, skewCheck)
	return checks
}

// featureChecks reports which Phase 3/4 features the granted scopes unlock. It
// is the "feature matrix from the token" rule from PLAN.md: `scp` lists every
// consented scope, so no probing is needed.
func (a *App) featureChecks(granted []string, verbose bool) []doctorCheck {
	if len(granted) == 0 {
		return []doctorCheck{{
			Name: "scopes", Status: statusWarn,
			Detail: "no granted scopes to compare (not signed in, or TEAMS_ACCESS_TOKEN is set)",
			Fix:    "run `teams auth login` to get a token whose `scp` claim lists the consented scopes",
		}}
	}
	statuses := config.Evaluate(granted)
	var available, missing []string
	for _, st := range statuses {
		if st.Granted {
			available = append(available, st.Command)
			continue
		}
		// one per line: this row is the actionable one, and a comma-joined blob
		// of ten commands is impossible to read even when it wraps.
		missing = append(missing, "• "+st.Command+" — needs "+strings.Join(st.Missing, " or "))
	}
	// The actionable row comes first and the long list of commands is only
	// printed with -v (or --json, where the caller asked for everything): a
	// twelve-line cell in the middle of a diagnostic report buries the warning.
	checks := []doctorCheck{{
		Name:   "features available",
		Status: statusOK,
		Detail: fmt.Sprintf("%d of %d commands (run `teams doctor -v` to list them)", len(available), len(statuses)),
	}}
	if verbose && len(available) > 0 {
		checks = append(checks, doctorCheck{
			Name:   "commands",
			Status: statusOK,
			Detail: output.JoinNonEmpty(", ", available...),
		})
	}
	if len(missing) > 0 {
		checks = append(checks, doctorCheck{
			Name:   "features not granted",
			Status: statusWarn,
			Detail: strings.Join(missing, "\n"),
			Fix:    "consent for the missing scopes is what closes these gaps; run `teams auth status --admin-request` for the ticket",
		})
	}
	return checks
}

// probeGraph does one read-only call to /me and uses its Date header for the
// clock-skew check. Graph's own docs do not describe the Date header, so a
// missing value is reported as "unknown" rather than as a failure.
func (a *App) probeGraph(ctx context.Context) (doctorCheck, doctorCheck) {
	reach := doctorCheck{Name: "graph", Status: statusOK}
	skew := doctorCheck{Name: "clock skew", Status: statusOK}

	client, err := a.Graph(ctx)
	if err != nil {
		reach.Status = statusFail
		reach.Detail = err.Error()
		reach.Fix = output.HintOf(err)
		reach.Code = output.CodeOf(err)
		skew.Status = statusWarn
		skew.Detail = "not checked"
		return reach, skew
	}
	resp, err := client.Do(ctx, graph.Request{
		Method: "GET",
		Path:   "/me",
		Query:  url.Values{"$select": {"id"}},
	})
	if err != nil {
		reach.Status = statusFail
		reach.Detail = err.Error()
		reach.Fix = output.HintOf(err)
		if reach.Fix == "" {
			// A transport failure (no route, DNS, TLS) has no hint of its own.
			reach.Fix = "check network access, a proxy, and the profile's cloud setting (global, usgov, china)"
		}
		reach.Code = output.CodeOf(err)
		skew.Status = statusWarn
		skew.Detail = "not checked (Graph unreachable)"
		return reach, skew
	}
	reach.Detail = "reachable (" + client.BaseURL() + ")"
	serverDate := resp.Header.Get("Date")
	if serverDate == "" {
		skew.Status = statusWarn
		skew.Detail = "Graph did not send a Date header; local clock " + a.Clock.Now().UTC().Format(time.RFC3339)
		return reach, skew
	}
	// The Date header is an HTTP-date (RFC 1123), not RFC 3339.
	parsed, parseErr := http.ParseTime(serverDate)
	if parseErr != nil {
		if parsed, parseErr = time.Parse(time.RFC3339, serverDate); parseErr != nil {
			skew.Status = statusWarn
			skew.Detail = "unparseable Date header " + serverDate
			return reach, skew
		}
	}
	skewValue := a.Clock.Now().Sub(parsed)
	skew.Detail = "local minus server: " + skewValue.Round(time.Second).String()
	if skewValue.Abs() > clockSkewTolerance {
		skew.Status = statusWarn
		skew.Fix = "fix the system clock; token expiry and --since windows are clock-based"
	}
	return reach, skew
}

// ConfigFileExists reports whether the config file is on disk.
func (a *App) ConfigFileExists() bool {
	return store.Scan(a.ConfigPath(), false).Exists
}
