package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/ref"
	"github.com/floriscornel/teams-cli/internal/store"
)

// Channel commands: list, show, read and files. The read scope for messages is
// ChannelMessage.Read.All with Group.Read.All as the documented alternative; the
// files folder is least-privileged Files.Read.All
// (refs/INDEX.md section 1, "Channels and messages" and "Files and inline images").

const defaultMessageLimit = 20

// defaultChatLimit is one page of chats. The API caps $top at 50 and expands at
// most 25 members per chat, so a page is one bounded request; --all pages the
// rest (PLAN.md:230; refs/graph/api-reference/v1.0/api/chat-list.md:20).
const defaultChatLimit = 50

func (a *App) newChannelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "channel",
		Short: "List, read and inspect channels",
	}
	cmd.AddCommand(a.newChannelListCmd(), a.newChannelShowCmd(), a.newChannelReadCmd(), a.newChannelFilesCmd())
	return cmd
}

func (a *App) newChannelListCmd() *cobra.Command {
	var teamFlag string
	cmd := &cobra.Command{
		Use:   "list <team>",
		Short: "List a team's channels",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams channel list", []string{"Channel.ReadBasic.All"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			team, err := resolver.Team(ctx, firstOf(args[0], teamFlag))
			if err != nil {
				return err
			}
			channels, err := resolver.Client().ListChannels(ctx, team.TeamID)
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(channels)
			}
			rows := make([][]string, 0, len(channels))
			for _, channel := range channels {
				rows = append(rows, []string{channel.DisplayName, channel.ID, channel.MembershipType, channel.Description})
			}
			a.Printer.Table([]string{"NAME", "ID", "TYPE", "DESCRIPTION"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to (when the reference does not say)")
	return cmd
}

func (a *App) newChannelShowCmd() *cobra.Command {
	var teamFlag string
	cmd := &cobra.Command{
		Use:   "show <channel>",
		Short: "Show one channel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams channel show", []string{"Channel.ReadBasic.All"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Channel(ctx, args[0], teamFlag)
			if err != nil {
				return err
			}
			channel, err := resolver.Client().GetChannel(ctx, resolved.TeamID, resolved.ChannelID)
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(channel)
			}
			a.Printer.Definitions([][2]string{
				{"name", channel.DisplayName},
				{"id", channel.ID},
				{"team", firstNonEmptyString(resolved.TeamName, resolved.TeamID)},
				{"type", channel.MembershipType},
				{"created", formatTime(channel.CreatedDateTime)},
				{"description", channel.Description},
				{"webUrl", channel.WebURL},
			})
			return nil
		},
	}
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to (when the reference does not say)")
	return cmd
}

func (a *App) newChannelReadCmd() *cobra.Command {
	var (
		teamFlag    string
		repliesFlag bool
		flags       listFlags
		window      windowFlags
	)
	cmd := &cobra.Command{
		Use:   "read <channel>",
		Short: "Read a channel's root messages",
		Long: "Read a channel's messages. --since and --until are applied on the client:\n" +
			"the channel message endpoint supports only $top and $expand, and $filter is a\n" +
			"400 rather than a silent ignore, so the CLI pages the listing and stops at the\n" +
			"first thread older than the window (PLAN.md:187).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams channel read", []string{"ChannelMessage.Read.All", "Group.Read.All", "Group.ReadWrite.All"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Channel(ctx, args[0], teamFlag)
			if err != nil {
				return err
			}
			since, until, err := window.window(a.Clock.Now())
			if err != nil {
				return err
			}
			msgs, err := resolver.Client().ListChannelMessages(ctx, resolved.TeamID, resolved.ChannelID, graph.MessageQuery{
				Top:     graph.MaxTopMessages,
				Limit:   flags.limitOf(defaultMessageLimit),
				Since:   since,
				Until:   until,
				Replies: repliesFlag,
			})
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(msgs)
			}
			if len(msgs) == 0 {
				a.Printer.Statusf("no messages in %s", channelLabel(resolved))
				return nil
			}
			return a.printMessages(msgs, messageOptions{container: channelLabel(resolved), replies: repliesFlag})
		},
	}
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to (when the reference does not say)")
	cmd.Flags().BoolVar(&repliesFlag, "replies", false, "include each thread's replies")
	cmd.Flags().IntVar(&flags.limit, "limit", defaultMessageLimit, "maximum number of messages (newest first)")
	cmd.Flags().BoolVar(&flags.all, "all", false, "fetch every page instead of --limit")
	cmd.Flags().StringVar(&window.since, "since", "", "only messages newer than this (a duration like 24h, or a timestamp)")
	cmd.Flags().StringVar(&window.until, "until", "", "only messages older than this (a duration like 24h, or a timestamp)")
	return cmd
}

func (a *App) newChannelFilesCmd() *cobra.Command {
	var teamFlag string
	cmd := &cobra.Command{
		Use:   "files <channel>",
		Short: "List the files in a channel's SharePoint folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams channel files", []string{"Files.Read.All", "Files.Read"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Channel(ctx, args[0], teamFlag)
			if err != nil {
				return err
			}
			folder, items, err := resolver.Client().ListChannelFiles(ctx, resolved.TeamID, resolved.ChannelID)
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(items)
			}
			if len(items) == 0 {
				a.Printer.Statusf("no files in %s", channelLabel(resolved))
				return nil
			}
			rows := make([][]string, 0, len(items))
			for _, item := range items {
				rows = append(rows, []string{item.Name, itemKind(item), humanSize(item.Size), formatTime(item.LastModifiedDateTime), item.ID})
			}
			a.Printer.Table([]string{"NAME", "TYPE", "SIZE", "MODIFIED", "ID"}, rows)
			if folder.WebURL != "" {
				a.Printer.Statusf("folder: %s", folder.WebURL)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to (when the reference does not say)")
	return cmd
}

// channelLabel is the display name of a resolved channel, for a message header.
func channelLabel(resolved ref.Ref) string {
	return "#" + firstNonEmptyString(resolved.ChannelName, resolved.ChannelID)
}

// itemKind names what a drive item is.
func itemKind(item graph.DriveItem) string {
	if item.IsFolder() {
		return "folder"
	}
	if mime := item.MimeType(); mime != "" {
		return strings.TrimPrefix(mime, "application/")
	}
	return "file"
}

// humanSize renders a byte count for the files table.
func humanSize(n int64) string {
	if n <= 0 {
		return ""
	}
	return store.HumanBytes(n)
}

// firstOf returns the first non-empty value, so a positional argument wins over
// the flag that mirrors it.
func firstOf(values ...string) string { return firstNonEmptyString(values...) }
