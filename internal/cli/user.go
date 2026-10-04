package cli

import (
	"github.com/spf13/cobra"
)

// User commands: search the directory and show one user. Both need
// User.ReadBasic.All, which is what makes GET /users reach anybody rather than
// only /me (PLAN.md:59).

func (a *App) newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Search the directory and show users",
	}
	cmd.AddCommand(a.newUserSearchCmd(), a.newUserShowCmd())
	return cmd
}

func (a *App) newUserSearchCmd() *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search the directory for a person",
		Long: "Search the directory with a startswith filter on displayName, falling back\n" +
			"to userPrincipalName and mail when nothing matches. A single quote in the\n" +
			"query is escaped by doubling it, which OData requires and the MCP never does\n" +
			"(PLAN.md:231).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams user search", []string{"User.ReadBasic.All"}); err != nil {
				return err
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			users, err := client.SearchUsers(ctx, args[0], flags.limitOf(defaultUserSearchLimit))
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(users)
			}
			if len(users) == 0 {
				a.Printer.Statusf("no user matches %q", args[0])
				return nil
			}
			rows := make([][]string, 0, len(users))
			for _, user := range users {
				rows = append(rows, []string{user.DisplayName, user.Address(), user.ID, user.JobTitle})
			}
			a.Printer.Table([]string{"NAME", "ADDRESS", "ID", "TITLE"}, rows)
			return nil
		},
	}
	cmd.Flags().IntVar(&flags.limit, "limit", defaultUserSearchLimit, "maximum number of users")
	cmd.Flags().BoolVar(&flags.all, "all", false, "fetch every page")
	return cmd
}

func (a *App) newUserShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <user>",
		Short: "Show one user",
		Long: "Show one user. <user> is an e-mail address, a user principal name, an id,\n" +
			"@name or a display name; a name is resolved through the entity cache, the\n" +
			"members of your chats and teams, /me/people and finally the directory\n" +
			"(PLAN.md:167).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams user show", []string{"User.ReadBasic.All", "User.Read"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Person(ctx, args[0])
			if err != nil {
				return err
			}
			user, err := resolver.Client().GetUser(ctx, resolved.UserID)
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(user)
			}
			a.Printer.Definitions([][2]string{
				{"name", user.DisplayName},
				{"upn", user.UserPrincipalName},
				{"mail", user.Mail},
				{"id", user.ID},
				{"title", user.JobTitle},
				{"department", user.Department},
				{"office", user.OfficeLocation},
			})
			return nil
		},
	}
	return cmd
}

// defaultUserSearchLimit bounds a directory search, which is a paged listing.
const defaultUserSearchLimit = 25
