package cli

import (
	"context"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/ref"
)

// Search commands: `teams search` and `teams mentions`. Both run the same
// POST /search/query call; mentions is the IsMentioned:true preset PLAN.md:127
// documents.
//
// The KQL scope terms are the documented ones
// (refs/graph/concepts/search-concept-chat-messages.md:258-269); IsRead is
// deliberately never sent, because it returned HTTP 500 in the spike
// (docs/spike/phase1.md:79).

func (a *App) newSearchCmd() *cobra.Command {
	var (
		from          string
		to            string
		in            string
		flags         listFlags
		window        windowFlags
		mentionsMe    bool
		hasAttachment bool
		page          int
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search Teams messages",
		Long: "Search Teams messages with KQL. The documented scope terms are available\n" +
			"through flags (--from, --to, --mentions-me, --has-attachment) and inline in\n" +
			"<query>, so from:bob sent>2026-10-01 keeps working.\n\n" +
			"A hit carries no message body: Graph returns the id, the sender, the\n" +
			"timestamps, the subject and a web link (docs/spike/phase1.md:83).\n" +
			"Use `teams thread read <link>` for the text.",
		// The query is optional: --mentions-me or --has-attachment alone is a
		// perfectly good search, and the empty-filter check below says so.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams search", []string{"Chat.Read", "Chat.ReadWrite", "ChannelMessage.Read.All"}); err != nil {
				return err
			}
			since, until, err := window.window(a.Clock.Now())
			if err != nil {
				return err
			}
			filters := graph.SearchFilters{
				Text:       strings.Join(args, " "),
				From:       from,
				To:         to,
				Since:      since,
				Until:      until,
				MentionsMe: mentionsMe,
			}
			if cmd.Flags().Changed("has-attachment") {
				value := hasAttachment
				filters.HasAttachment = &value
			}
			if filters.Empty() {
				return output.WithHint(output.Usagef("nothing to search for"),
					"pass a query, or one of --from, --to, --since, --mentions-me, --has-attachment")
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			// --in is the only part of a search that resolves a reference, so the
			// resolver (and the entity cache behind it) is built only then.
			var scope *graph.SearchResource
			if in != "" {
				resolver, err := a.resolver(ctx)
				if err != nil {
					return err
				}
				scope, err = a.searchScope(ctx, resolver, in)
				if err != nil {
					return err
				}
			}
			limit := flags.limitOf(graph.DefaultTopSearch)
			if limit > graph.MaxTopSearch {
				limit = graph.MaxTopSearch
			}
			a.Printer.Statusf("searching...")
			result, err := client.SearchMessages(ctx, graph.SearchQuery{
				Query: filters.KQL(),
				From:  pageFrom(page, limit),
				Size:  limit,
			})
			if err != nil {
				return err
			}
			if scope != nil {
				result.Hits = filterHitsByScope(result.Hits, scope)
			}
			return a.printSearchResult(result, page)
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "only messages sent by this person (from:)")
	cmd.Flags().StringVar(&to, "to", "", "only messages sent to this person (to:, 1:1 chats only)")
	cmd.Flags().StringVar(&in, "in", "", "only messages in this channel or chat")
	cmd.Flags().BoolVar(&mentionsMe, "mentions-me", false, "only messages that mention you")
	cmd.Flags().BoolVar(&hasAttachment, "has-attachment", false, "only messages with attachments")
	cmd.Flags().IntVar(&flags.limit, "limit", graph.DefaultTopSearch, "results per page (the API allows up to 50)")
	cmd.Flags().IntVar(&page, "page", 1, "1-based page number (the search API pages by from/size)")
	cmd.Flags().StringVar(&window.since, "since", "", "only messages newer than this (a duration like 7d, or a timestamp)")
	cmd.Flags().StringVar(&window.until, "until", "", "only messages older than this (a duration like 7d, or a timestamp)")
	return cmd
}

func (a *App) newMentionsCmd() *cobra.Command {
	var (
		flags  listFlags
		window windowFlags
	)
	cmd := &cobra.Command{
		Use:   "mentions",
		Short: "Show the messages that mention you",
		Long: "Show the messages that mention you, newest first. The search runs with\n" +
			"IsMentioned:true and a sent>= window, and the timestamps are checked again on\n" +
			"the client, which is what makes --since exact (PLAN.md:233). The default\n" +
			"window is 24h.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams mentions", []string{"Chat.Read", "Chat.ReadWrite", "ChannelMessage.Read.All"}); err != nil {
				return err
			}
			if !cmd.Flags().Changed("since") && window.until == "" {
				window.since = "24h"
			}
			since, until, err := window.window(a.Clock.Now())
			if err != nil {
				return err
			}
			filters := graph.SearchFilters{MentionsMe: true, Since: since, Until: until}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			limit := flags.limitOf(graph.DefaultTopSearch)
			if limit > graph.MaxTopSearch {
				limit = graph.MaxTopSearch
			}
			a.Printer.Statusf("searching mentions...")
			result, err := client.SearchMessages(ctx, graph.SearchQuery{Query: filters.KQL(), Size: limit})
			if err != nil {
				return err
			}
			result.Hits = filterHitsSince(result.Hits, since)
			return a.printSearchResult(result, 0)
		},
	}
	cmd.Flags().IntVar(&flags.limit, "limit", graph.DefaultTopSearch, "results per page (the API allows up to 50)")
	cmd.Flags().StringVar(&window.since, "since", "", "only mentions newer than this (default 24h; a duration or a timestamp)")
	cmd.Flags().StringVar(&window.until, "until", "", "only mentions older than this (a duration or a timestamp)")
	return cmd
}

// searchScope resolves --in to the ids a hit must carry.
func (a *App) searchScope(ctx context.Context, resolver *ref.Resolver, raw string) (*graph.SearchResource, error) {
	parsed, err := ref.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Kind == ref.KindChat || parsed.IsEmail || parsed.Kind == ref.KindUser || ref.LooksLikeChatID(parsed.Raw) {
		chat, err := resolver.Chat(ctx, raw)
		if err != nil {
			return nil, err
		}
		return &graph.SearchResource{ChatID: chat.ChatID}, nil
	}
	channel, err := resolver.Channel(ctx, raw, "")
	if err != nil {
		return nil, err
	}
	return &graph.SearchResource{ChannelIdentity: &graph.ChannelIdentity{TeamID: channel.TeamID, ChannelID: channel.ChannelID}}, nil
}

// filterHitsByScope keeps the hits that come from one channel or chat.
func filterHitsByScope(hits []graph.SearchHit, scope *graph.SearchResource) []graph.SearchHit {
	out := make([]graph.SearchHit, 0, len(hits))
	for _, hit := range hits {
		if scope.ChatID != "" && hit.Resource.ChatID == scope.ChatID {
			out = append(out, hit)
			continue
		}
		if scope.ChannelIdentity != nil {
			teamID, channelID, ok := hit.Resource.InChannel()
			if ok && teamID == scope.ChannelIdentity.TeamID && channelID == scope.ChannelIdentity.ChannelID {
				out = append(out, hit)
			}
		}
	}
	return out
}

// filterHitsSince drops the hits older than the window. The sent term honours a
// time of day (PLAN.md:235), and this check keeps --since exact for a tenant
// whose index lags.
func filterHitsSince(hits []graph.SearchHit, since time.Time) []graph.SearchHit {
	if since.IsZero() {
		return hits
	}
	out := make([]graph.SearchHit, 0, len(hits))
	for _, hit := range hits {
		if hit.Resource.CreatedDateTime.IsZero() || !hit.Resource.CreatedDateTime.Before(since) {
			out = append(out, hit)
		}
	}
	return out
}

// pageFrom converts a 1-based page number into the API's from offset. The first
// page must start at zero (refs/graph/api-reference/v1.0/resources/search-api-overview.md:67).
func pageFrom(page, size int) int {
	if page <= 1 {
		return 0
	}
	return (page - 1) * size
}

// printSearchResult renders a search response: a table of hits on stdout and a
// "showing N of M" note on stderr, because total is the full match count
// (docs/spike/phase1.md:77).
func (a *App) printSearchResult(result graph.SearchResult, page int) error {
	if a.Printer.JSONMode() {
		return a.Printer.JSON(result)
	}
	if len(result.Hits) == 0 {
		a.Printer.Statusf("no matches")
		return nil
	}
	rows := make([][]string, 0, len(result.Hits))
	for _, hit := range result.Hits {
		rows = append(rows, []string{
			hit.Resource.CreatedDateTime.UTC().Format("2006-01-02 15:04"),
			hit.Resource.From.DisplayName(),
			hitContainer(hit.Resource),
			hitSummary(hit),
			hit.Resource.WebLink,
		})
	}
	a.Printer.Table([]string{"WHEN", "FROM", "WHERE", "SUMMARY", "LINK"}, rows)
	if page > 0 {
		a.Printer.Statusf("page %d: %d hit(s), %d match(es) in total", page, len(result.Hits), result.Total)
		return nil
	}
	a.Printer.Statusf("%d of %d match(es)", len(result.Hits), result.Total)
	return nil
}

// hitContainer names where a hit lives.
func hitContainer(resource graph.SearchResource) string {
	if resource.ChatID != "" {
		return "chat " + resource.ChatID
	}
	if _, channelID, ok := resource.InChannel(); ok {
		return "channel " + channelID
	}
	return ""
}

// hitSummary is what a hit can show: the API's summary, which is the matched
// text with its context.
func hitSummary(hit graph.SearchHit) string {
	if strings.TrimSpace(hit.Summary) != "" {
		return strings.Join(strings.Fields(hit.Summary), " ")
	}
	if hit.Resource.Subject != "" {
		return hit.Resource.Subject
	}
	return hit.Resource.ID
}
