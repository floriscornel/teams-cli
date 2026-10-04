package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
)

// newProfileCmd implements `teams profile list|use`. Both read and write only
// the config file: no network, no token store.
func (a *App) newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "List or select profiles",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(a.newProfileListCmd(), a.newProfileUseCmd())
	return cmd
}

type profileRow struct {
	Name         string   `json:"name"`
	Default      bool     `json:"default"`
	Tenant       string   `json:"tenant"`
	ClientID     string   `json:"client_id"`
	Mode         string   `json:"mode"`
	Cloud        string   `json:"cloud"`
	Scopes       string   `json:"scopes"`
	TokenStore   string   `json:"token_store"`
	GraphBaseURL string   `json:"graph_base_url"`
	ScopesList   []string `json:"scope_list"`
	SignedIn     bool     `json:"signed_in"`
}

func (a *App) profileRows() ([]profileRow, error) {
	cfg, err := a.Config()
	if err != nil {
		return nil, err
	}
	names := cfg.ProfileNames()
	// The built-in `me` profile works without a config entry, so it must show up
	// in the list even before the file exists.
	def := cfg.DefaultProfile
	if def == "" {
		def = config.DefaultProfileName
	}
	if !containsName(names, def) {
		names = append(names, def)
		sort.Strings(names)
	}
	rows := make([]profileRow, 0, len(names))
	for _, name := range names {
		eff, err := cfg.Resolve(config.ResolveInput{ProfileFlag: name, Environ: []string{}})
		if err != nil {
			return nil, err
		}
		// Signed-in is a local question: does the profile have a token cache?
		// It never calls Graph, so `profile list` works offline.
		paths, err := store.Resolve(name)
		if err != nil {
			return nil, err
		}
		rows = append(rows, profileRow{
			Name:         name,
			Default:      name == cfg.DefaultProfile,
			Tenant:       eff.Tenant,
			ClientID:     eff.ClientID,
			Mode:         eff.Mode(),
			Cloud:        eff.Cloud,
			Scopes:       eff.ScopesSpec,
			TokenStore:   eff.TokenStore,
			GraphBaseURL: eff.GraphBaseURL,
			ScopesList:   eff.Scopes,
			SignedIn:     tokenCacheExists(paths),
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Default != rows[j].Default {
			return rows[i].Default
		}
		return rows[i].Name < rows[j].Name
	})
	return rows, nil
}

func (a *App) newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the configured profiles",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			rows, err := a.profileRows()
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]any{
					"profiles": rows,
					"config":   a.ConfigPath(),
				})
			}
			table := make([][]string, 0, len(rows))
			for _, row := range rows {
				name := row.Name
				if row.Default {
					name += " *"
				}
				table = append(table, []string{
					name, row.Tenant, row.Mode, row.Cloud, row.Scopes, signInLabel(row.SignedIn),
				})
			}
			a.Printer.Table([]string{"PROFILE", "TENANT", "MODE", "CLOUD", "SCOPES", "TOKENS"}, table)
			if !a.Printer.JSONMode() {
				a.Printer.Statusf("config: %s", a.ConfigPath())
			}
			return nil
		},
	}
}

func (a *App) newProfileUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use <name>",
		Short: "Set the default profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			name := args[0]
			cfg, err := a.Config()
			if err != nil {
				return err
			}
			if _, ok := cfg.Profiles[name]; !ok && name != config.DefaultProfileName {
				return output.WithHint(output.Usagef("profile %q is not configured", name),
					fmt.Sprintf("known profiles: %s", strings.Join(cfg.ProfileNames(), ", ")))
			}
			cfg.DefaultProfile = name
			if err := cfg.Save(a.ConfigPath()); err != nil {
				return output.Errorf("%v", err)
			}
			a.Printer.Successf("default profile is now %s", name)
			return nil
		},
	}
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

func signInLabel(signedIn bool) string {
	if signedIn {
		return "cached"
	}
	return "-"
}

// tokenCacheExists reports whether any of the cache files a profile can use is
// present. It is a filesystem check, never a network call.
func tokenCacheExists(paths store.Paths) bool {
	for _, loc := range paths.Locations() {
		if loc.Kind != "secrets" {
			continue
		}
		if st := store.Scan(loc.Path, loc.Dir); st.Exists && st.Files > 0 {
			return true
		}
	}
	return false
}
