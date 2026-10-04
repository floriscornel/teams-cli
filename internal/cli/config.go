package cli

import (
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/store"
)

// newConfigCmd implements `teams config get|set|list`. Only the TOML file is
// touched; secrets never live here (PLAN.md "Local data"), and `cache clear`
// never removes it.
func (a *App) newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and write the config file",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.newConfigListCmd(), a.newConfigGetCmd(), a.newConfigSetCmd())
	return cmd
}

func (a *App) newConfigListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show the config file location and its contents",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := a.Config()
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				eff, err := a.Effective()
				if err != nil {
					return err
				}
				return a.Printer.JSON(map[string]any{
					"path":    a.ConfigPath(),
					"config":  cfg,
					"profile": eff,
				})
			}
			a.Printer.Definitions([][2]string{
				{"file", a.ConfigPath()},
				{"exists", yesNo(store.Scan(a.ConfigPath(), false).Exists)},
				{"default_profile", cfg.DefaultProfile},
			})
			rendered, err := toml.Marshal(cfg)
			if err != nil {
				return output.Errorf("encode the config: %v", err)
			}
			a.Printer.Println()
			a.Printer.Print(string(rendered))
			return nil
		},
	}
}

func (a *App) newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Print one config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := a.Config()
			if err != nil {
				return err
			}
			value, err := getConfigValue(cfg, args[0])
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]string{"key": args[0], "value": value})
			}
			a.Printer.Println(value)
			return nil
		},
	}
}

func (a *App) newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one config value",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := a.Config()
			if err != nil {
				return err
			}
			if err := setConfigValue(cfg, args[0], args[1]); err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return output.Usagef("%v", err)
			}
			if err := cfg.Save(a.ConfigPath()); err != nil {
				return output.Errorf("%v", err)
			}
			a.Printer.Successf("%s = %s in %s", args[0], args[1], a.ConfigPath())
			return nil
		},
	}
}

// getConfigValue resolves a dotted key:
//
//	default_profile
//	profiles.<name>.<field>
//	ai.<provider|model|language>
//	update_check
func getConfigValue(cfg *config.Config, key string) (string, error) {
	parts := strings.Split(key, ".")
	switch {
	case key == "default_profile":
		return cfg.DefaultProfile, nil
	case key == "update_check":
		if cfg.UpdateCheck == nil {
			return "", nil
		}
		return fmt.Sprint(*cfg.UpdateCheck), nil
	case strings.HasPrefix(key, "ai.") && len(parts) == 2:
		switch parts[1] {
		case "provider":
			return cfg.AI.Provider, nil
		case "model":
			return cfg.AI.Model, nil
		case "language":
			return cfg.AI.Language, nil
		}
		return "", unknownKey(key)
	case strings.HasPrefix(key, "profiles.") && len(parts) == 3:
		profile, ok := cfg.Profiles[parts[1]]
		if !ok {
			return "", output.Usagef("profile %q is not configured", parts[1])
		}
		return profileField(profile, parts[2], key)
	default:
		return "", unknownKey(key)
	}
}

func profileField(profile config.Profile, field, key string) (string, error) {
	switch field {
	case "tenant":
		return profile.Tenant, nil
	case "client_id":
		return profile.ClientID, nil
	case "mode":
		return profile.Mode, nil
	case "cloud":
		return profile.Cloud, nil
	case "token_store":
		return profile.TokenStore, nil
	case "scopes":
		return profile.Scopes, nil
	case "graph_base_url":
		return profile.GraphBaseURL, nil
	case "home_account_id":
		return profile.HomeAccountID, nil
	default:
		return "", unknownKey(key)
	}
}

// setConfigValue applies a dotted key, creating a profile when it does not exist
// yet so `teams config set profiles.bot.tenant contoso.com` works from nothing.
func setConfigValue(cfg *config.Config, key, value string) error {
	parts := strings.Split(key, ".")
	switch {
	case key == "default_profile":
		cfg.DefaultProfile = value
		return nil
	case key == "update_check":
		cfg.UpdateCheck = config.BoolPointer(strings.EqualFold(value, "true") || value == "1")
		return nil
	case strings.HasPrefix(key, "ai.") && len(parts) == 2:
		switch parts[1] {
		case "provider":
			cfg.AI.Provider = value
		case "model":
			cfg.AI.Model = value
		case "language":
			cfg.AI.Language = value
		default:
			return unknownKey(key)
		}
		return nil
	case strings.HasPrefix(key, "profiles.") && len(parts) == 3:
		name, field := parts[1], parts[2]
		if !store.ValidProfileName(name) {
			return output.Usagef("invalid profile name %q", name)
		}
		if cfg.Profiles == nil {
			cfg.Profiles = map[string]config.Profile{}
		}
		profile := cfg.Profiles[name]
		switch field {
		case "tenant":
			profile.Tenant = value
		case "client_id":
			profile.ClientID = value
		case "mode":
			profile.Mode = value
		case "cloud":
			profile.Cloud = value
		case "token_store":
			profile.TokenStore = value
		case "scopes":
			profile.Scopes = value
		case "graph_base_url":
			profile.GraphBaseURL = value
		case "home_account_id":
			profile.HomeAccountID = value
		default:
			return unknownKey(key)
		}
		cfg.Profiles[name] = profile
		return nil
	default:
		return unknownKey(key)
	}
}

func unknownKey(key string) error {
	return output.WithHint(output.Usagef("unknown config key %q", key),
		"known keys: default_profile, update_check, ai.provider, ai.model, ai.language, profiles.<name>.{tenant,client_id,mode,cloud,token_store,scopes,graph_base_url,home_account_id}")
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}
