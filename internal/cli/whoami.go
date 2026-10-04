package cli

import (
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// Me is the signed-in user, as returned by GET /me. Only the fields the CLI
// shows are modelled; the `--json` schema is exactly this struct.
//
// GET /me needs User.Read, which reaches only the signed-in user - which is all
// whoami wants (refs/graph/api-reference/v1.0/includes/permissions/user-get-permissions.md:3).
type Me struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName,omitempty"`
	Mail              string `json:"mail,omitempty"`
}

func (a *App) newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.requireScope(ctx, "teams whoami", []string{"User.Read"}); err != nil {
				return err
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			var me Me
			err = client.Get(ctx, "/me", &me,
				graph.WithQuery(url.Values{"$select": {strings.Join([]string{"id", "displayName", "userPrincipalName", "mail"}, ",")}}))
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(me)
			}
			pairs := [][2]string{{"name", me.DisplayName}}
			if me.UserPrincipalName != "" {
				pairs = append(pairs, [2]string{"upn", me.UserPrincipalName})
			}
			if me.Mail != "" && me.Mail != me.UserPrincipalName {
				pairs = append(pairs, [2]string{"mail", me.Mail})
			}
			pairs = append(pairs, [2]string{"id", me.ID})
			a.Printer.Definitions(pairs)
			return nil
		},
	}
}
