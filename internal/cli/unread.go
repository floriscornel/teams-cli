package cli

import (
	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// `teams unread` is the inbox view PLAN.md:125 describes: unread chats plus the
// unread @mentions. Chats come from viewpoint.lastMessageReadDateTime, which is
// documented as the read watermark and returned by List chats
// (refs/graph/api-reference/v1.0/resources/chatviewpoint.md:23); mentions come
// from IsMentioned:true. Unread channel posts are deliberately not covered: there
// is no working read-state API for them (PLAN.md:151).

// unreadView is the documented --json shape of `teams unread`.
type unreadView struct {
	Chats    []graph.Chat      `json:"chats"`
	Mentions []graph.SearchHit `json:"mentions"`
	// Since is the window the mentions were searched in, so a script can tell
	// what "unread" meant for this run.
	Since string `json:"since,omitempty"`
}

func (a *App) newUnreadCmd() *cobra.Command {
	var (
		chatsOnly    bool
		mentionsOnly bool
		window       windowFlags
	)
	cmd := &cobra.Command{
		Use:   "unread",
		Short: "Show unread chats and the mentions you have not read",
		Long: "Show what is waiting for you: chats whose newest message is newer than\n" +
			"their read watermark, and the messages that mention you. It never marks\n" +
			"anything read.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams unread", []string{"Chat.Read", "Chat.ReadWrite"}); err != nil {
				return err
			}
			// No reference to resolve here, so the entity cache is not opened.
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			// Both halves scan everything: an unread chat or an old mention is exactly
			// the thing a newest-page-only listing would miss.
			view := unreadView{Chats: []graph.Chat{}, Mentions: []graph.SearchHit{}}
			if !mentionsOnly {
				a.Printer.Statusf("scanning chats...")
				all, err := client.ListChats(ctx, graph.ChatQuery{
					Top:                graph.MaxTopChats,
					Members:            true,
					LastMessagePreview: true,
					OrderByLastMessage: true,
				})
				if err != nil {
					return err
				}
				view.Chats = filterUnreadChats(all)
			}
			if !chatsOnly {
				if !cmd.Flags().Changed("since") && window.until == "" {
					window.since = "24h"
				}
				since, until, err := window.window(a.Clock.Now())
				if err != nil {
					return err
				}
				a.Printer.Statusf("searching mentions...")
				filters := graph.SearchFilters{MentionsMe: true, Since: since, Until: until}
				result, err := client.SearchMessages(ctx, graph.SearchQuery{Query: filters.KQL(), Size: graph.MaxTopSearch})
				if err != nil {
					return err
				}
				view.Mentions = filterHitsSince(result.Hits, since)
				if !since.IsZero() {
					view.Since = since.UTC().Format("2006-01-02T15:04:05Z")
				}
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(view)
			}
			if !mentionsOnly {
				a.Printer.Printf("unread chats: %d\n", len(view.Chats))
				rows := make([][]string, 0, len(view.Chats))
				for _, chat := range view.Chats {
					rows = append(rows, []string{chatTitle(chat), lastActivity(chat), chat.ID})
				}
				a.Printer.Table([]string{"TITLE", "LAST ACTIVITY", "ID"}, rows)
			}
			if !chatsOnly {
				if !mentionsOnly {
					a.Printer.Println()
				}
				a.Printer.Printf("mentions: %d\n", len(view.Mentions))
				rows := make([][]string, 0, len(view.Mentions))
				for _, hit := range view.Mentions {
					rows = append(rows, []string{
						hit.Resource.CreatedDateTime.UTC().Format("2006-01-02 15:04"),
						hit.Resource.From.DisplayName(),
						hitContainer(hit.Resource),
						hitSummary(hit),
						hit.Resource.WebLink,
					})
				}
				a.Printer.Table([]string{"WHEN", "FROM", "WHERE", "SUMMARY", "LINK"}, rows)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&chatsOnly, "chats", false, "only unread chats")
	cmd.Flags().BoolVar(&mentionsOnly, "mentions", false, "only unread mentions")
	cmd.Flags().StringVar(&window.since, "since", "", "how far back to look for mentions (default 24h)")
	cmd.Flags().StringVar(&window.until, "until", "", "ignore mentions newer than this")
	return cmd
}
