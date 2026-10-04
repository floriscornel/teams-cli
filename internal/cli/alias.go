package cli

import (
	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/ref"
	"github.com/floriscornel/teams-cli/internal/store"
)

// Alias commands. An alias is durable user memory that every command can use,
// not only the AI ones (PLAN.md:297): `teams alias set boss alice@contoso.com`
// makes `teams chat read @boss` work.
//
// Aliases are state, not cache: `teams cache clear` leaves them alone, and they
// are per profile so a bot alias never leaks into the personal profile.

func (a *App) newAliasCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "alias",
		Short: "Name a person, chat or channel once and reuse it everywhere",
	}
	cmd.AddCommand(a.newAliasSetCmd(), a.newAliasListCmd(), a.newAliasRemoveCmd())
	return cmd
}

func (a *App) newAliasSetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "set <name> <target>",
		Short: "Create or replace an alias",
		Long: "Create or replace an alias. The target is anything the CLI accepts as a\n" +
			"reference: a person (alice@example.com or @alice), a chat, Team/Channel, a\n" +
			"Teams link or an id. Aliases are per profile.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			name, target := args[0], args[1]
			if !store.ValidAliasName(name) {
				return output.WithHint(output.Usagef("invalid alias name %q", name),
					"use letters, digits, dot, dash or underscore")
			}
			if _, err := ref.Parse(target); err != nil {
				return output.WithHint(output.Usagef("alias target %q is not a reference: %v", target, err),
					"use @person, alice@example.com, Team/Channel, a Teams link or an id")
			}
			paths, err := a.Paths()
			if err != nil {
				return err
			}
			if err := store.SetAlias(paths.AliasesFile(), name, target); err != nil {
				return output.Errorf("%v", err)
			}
			a.Printer.Successf("%s -> %s", name, target)
			return nil
		},
	}
	return cmd
}

func (a *App) newAliasListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the aliases of this profile",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			paths, err := a.Paths()
			if err != nil {
				return err
			}
			aliases, err := store.ListAliases(paths.AliasesFile())
			if err != nil {
				return output.Errorf("%v", err)
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(aliases)
			}
			if len(aliases) == 0 {
				a.Printer.Statusf("no aliases for profile %s", paths.Profile)
				return nil
			}
			rows := make([][]string, 0, len(aliases))
			for _, alias := range aliases {
				rows = append(rows, []string{alias.Name, alias.Target})
			}
			a.Printer.Table([]string{"NAME", "TARGET"}, rows)
			return nil
		},
	}
	return cmd
}

func (a *App) newAliasRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove an alias",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			paths, err := a.Paths()
			if err != nil {
				return err
			}
			removed, err := store.RemoveAlias(paths.AliasesFile(), args[0])
			if err != nil {
				return output.Errorf("%v", err)
			}
			if !removed {
				return output.WithHint(output.NotFoundf("no alias named %q", args[0]), "run `teams alias list`")
			}
			a.Printer.Successf("removed %s", args[0])
			return nil
		},
	}
	return cmd
}
