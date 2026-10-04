package cli

import (
	"github.com/spf13/cobra"
)

// Team commands: `teams team list` and `teams team show`. Both are read-only and
// need Team.ReadBasic.All, the documented least-privileged scope for
// GET /me/joinedTeams and GET /teams/{team-id}
// (refs/graph/api-reference/v1.0/includes/permissions/user-list-joinedteams-permissions.md:9).

func (a *App) newTeamCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "team",
		Short: "List and show the teams you belong to",
	}
	cmd.AddCommand(a.newTeamListCmd(), a.newTeamShowCmd())
	return cmd
}

func (a *App) newTeamListCmd() *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the teams you are a member of",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams team list", []string{"Team.ReadBasic.All"}); err != nil {
				return err
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			teams, err := client.ListJoinedTeams(ctx)
			if err != nil {
				return err
			}
			// ListJoinedTeams pages every result (PLAN.md:230), so --limit is a
			// client-side cut of the complete listing.
			if !flags.all && flags.limit > 0 && len(teams) > flags.limit {
				teams = teams[:flags.limit]
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(teams)
			}
			rows := make([][]string, 0, len(teams))
			for _, team := range teams {
				rows = append(rows, []string{team.DisplayName, team.ID, team.Visibility, team.Description})
			}
			a.Printer.Table([]string{"NAME", "ID", "VISIBILITY", "DESCRIPTION"}, rows)
			return nil
		},
	}
	cmd.Flags().IntVar(&flags.limit, "limit", 0, "maximum number of teams to show (0 means all)")
	cmd.Flags().BoolVar(&flags.all, "all", false, "show every team (the default; kept for symmetry)")
	return cmd
}

func (a *App) newTeamShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <team>",
		Short: "Show one team",
		Long: "Show one team. <team> is a team name, Team/Channel, a Teams link or an id.\n" +
			"Names are matched case-insensitively and resolved through the entity cache;\n" +
			"--refresh resolves them again.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams team show", []string{"Team.ReadBasic.All"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Team(ctx, args[0])
			if err != nil {
				return err
			}
			team, err := resolver.Client().GetTeam(ctx, resolved.TeamID)
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(team)
			}
			a.Printer.Definitions([][2]string{
				{"name", team.DisplayName},
				{"id", team.ID},
				{"visibility", team.Visibility},
				{"created", formatTime(team.CreatedDateTime)},
				{"description", team.Description},
				{"webUrl", team.WebURL},
			})
			return nil
		},
	}
	return cmd
}
