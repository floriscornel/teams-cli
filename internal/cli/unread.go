package cli

import (
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
)

// `teams unread` is the inbox view PLAN.md:125 describes: unread chats plus
// the unread @mentions. Both halves take the same time window, because an
// "unread" chat is not the same thing as an "unread chat that matters": a meeting
// chat whose read state Teams never sets counts as unread forever, so without a
// window the inbox fills up with 2021.
//
// The window is 24h by default, --since/--until override it and --all removes it.
// Because the chat listing is ordered by lastMessagePreview/createdDateTime desc,
// a window also ends the listing early (graph.ChatQuery.Since), which is what keeps
// the command to one round trip on a tenant with hundreds of chats.
//
// Chats come from viewpoint.lastMessageReadDateTime, which is documented as the read
// watermark and returned by List chats
// (refs/graph/api-reference/v1.0/resources/chatviewpoint.md:23); mentions come from
// IsMentioned:true. Unread channel posts are deliberately not covered: there is no
// working read-state API for them (PLAN.md:151).

// defaultUnreadWindow is the inbox window when no --since/--until is given.
const defaultUnreadWindow = 24 * time.Hour

// unreadView is the documented --json shape of `teams unread`.
type unreadView struct {
	Chats    []graph.Chat      `json:"chats"`
	Mentions []graph.SearchHit `json:"mentions"`
	// Since is the lower bound of the window both halves used, so a script can
	// tell what "unread" meant for this run. It is empty with --all.
	Since string `json:"since,omitempty"`
	// Until is the upper bound, when one was given.
	Until string `json:"until,omitempty"`
}

func (a *App) newUnreadCmd() *cobra.Command {
	var (
		chatsOnly    bool
		mentionsOnly bool
		all          bool
		window       windowFlags
	)
	cmd := &cobra.Command{
		Use:   "unread",
		Short: "Show unread chats and the mentions you have not read",
		Long: "Show what is waiting for you: chats whose newest message is newer than\n" +
			"their read watermark, and the messages that mention you. It never marks\n" +
			"anything read.\n\n" +
			"Both halves look at the last 24h by default; --since/--until change the\n" +
			"window and --all removes it, which lists every unread chat however old.",
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
			since, until, err := a.unreadWindow(cmd, window, all)
			if err != nil {
				return err
			}
			view := unreadView{Chats: []graph.Chat{}, Mentions: []graph.SearchHit{}}
			if !since.IsZero() {
				view.Since = since.UTC().Format("2006-01-02T15:04:05Z")
			}
			if !until.IsZero() {
				view.Until = until.UTC().Format("2006-01-02T15:04:05Z")
			}
			if !mentionsOnly {
				a.Printer.Statusf("scanning chats...")
				chats, err := client.ListChats(ctx, graph.ChatQuery{
					Top:     graph.MaxTopChats,
					Members: true,
					// lastMessagePreview carries the newest message, which the unread
					// rule compares against the read watermark, and it is also the
					// only property $orderby accepts
					// (refs/graph/api-reference/v1.0/api/chat-list.md:63).
					LastMessagePreview: true,
					OrderByLastMessage: true,
					Since:              since,
				})
				if err != nil {
					return err
				}
				view.Chats = filterUnreadChats(filterChatsUntil(chats, until))
			}
			if !chatsOnly {
				a.Printer.Statusf("searching mentions...")
				filters := graph.SearchFilters{MentionsMe: true, Since: since, Until: until}
				result, err := client.SearchMessages(ctx, graph.SearchQuery{Query: filters.KQL(), Size: graph.MaxTopSearch})
				if err != nil {
					return err
				}
				view.Mentions = filterHitsUntil(filterHitsSince(result.Hits, since), until)
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(view)
			}
			a.printUnread(view, since, until, !mentionsOnly, !chatsOnly)
			return nil
		},
	}
	cmd.Flags().BoolVar(&chatsOnly, "chats", false, "only unread chats")
	cmd.Flags().BoolVar(&mentionsOnly, "mentions", false, "only unread mentions")
	cmd.Flags().BoolVar(&all, "all", false, "every unread chat, however old (no window)")
	cmd.Flags().StringVar(&window.since, "since", "", "window to look back over (default 24h; a duration or a timestamp)")
	cmd.Flags().StringVar(&window.until, "until", "", "ignore activity newer than this")
	return cmd
}

// unreadWindow resolves the inbox window: --all removes it, --since/--until set it,
// and the default is the last 24 hours for both halves.
func (a *App) unreadWindow(cmd *cobra.Command, flags windowFlags, all bool) (time.Time, time.Time, error) {
	now := a.Clock.Now()
	if all {
		return time.Time{}, time.Time{}, nil
	}
	if !cmd.Flags().Changed("since") && !cmd.Flags().Changed("until") {
		return now.Add(-defaultUnreadWindow), time.Time{}, nil
	}
	return flags.window(now)
}

// printUnread renders the inbox: the chats, then the mentions, each with a count
// that names the window and a hint back to the complete list.
func (a *App) printUnread(view unreadView, since, until time.Time, showChats, showMentions bool) {
	if !showChats && !showMentions {
		return
	}
	scope := " (all)"
	if !since.IsZero() {
		scope = " (since " + since.UTC().Format("2006-01-02 15:04") + ")"
	}
	if !showChats {
		if showMentions {
			a.printMentions(view)
		}
		return
	}
	a.Printer.Printf("unread chats: %d%s\n", len(view.Chats), scope)
	rows := make([][]string, 0, len(view.Chats))
	for _, chat := range view.Chats {
		rows = append(rows, []string{chatTitle(chat), lastActivity(chat), chat.ID})
	}
	a.Printer.Table([]string{"TITLE", "LAST ACTIVITY", "ID"}, rows)
	if since.IsZero() && until.IsZero() {
		a.Printer.Statusf("every unread chat is listed; without --all the last %s are shown", output.HumanDuration(defaultUnreadWindow))
	} else {
		a.Printer.Statusf("chats with activity in the window; --all lists every unread chat, however old")
	}
	if !showMentions {
		return
	}
	a.Printer.Println()
	a.printMentions(view)
}

// printMentions renders the mention table on its own, so --chats and --mentions
// print exactly one half.
func (a *App) printMentions(view unreadView) {
	a.Printer.Printf("mentions: %d\n", len(view.Mentions))
	mrows := make([][]string, 0, len(view.Mentions))
	for _, hit := range view.Mentions {
		mrows = append(mrows, []string{
			hit.Resource.CreatedDateTime.UTC().Format("2006-01-02 15:04"),
			hit.Resource.From.DisplayName(),
			hitContainer(hit.Resource),
			hitSummary(hit),
			hit.Resource.WebLink,
		})
	}
	a.Printer.Table([]string{"WHEN", "FROM", "WHERE", "SUMMARY", "LINK"}, mrows)
}

// filterChatsUntil keeps the chats whose last activity is at or before until.
func filterChatsUntil(chats []graph.Chat, until time.Time) []graph.Chat {
	if until.IsZero() {
		return chats
	}
	out := make([]graph.Chat, 0, len(chats))
	for _, chat := range chats {
		activity := chat.LastActivity()
		if activity.IsZero() || !activity.After(until) {
			out = append(out, chat)
		}
	}
	return out
}

// filterHitsUntil keeps the hits older than until.
func filterHitsUntil(hits []graph.SearchHit, until time.Time) []graph.SearchHit {
	if until.IsZero() {
		return hits
	}
	out := make([]graph.SearchHit, 0, len(hits))
	for _, hit := range hits {
		if hit.Resource.CreatedDateTime.IsZero() || !hit.Resource.CreatedDateTime.After(until) {
			out = append(out, hit)
		}
	}
	return out
}
