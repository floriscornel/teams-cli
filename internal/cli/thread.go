package cli

import (
	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
)

// Thread commands: read a message together with its replies. A channel message
// has replies; a chat message has none, because chats have no threads (PLAN.md:147),
// so a chat reference reads the message itself and says so.

func (a *App) newThreadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "Read a message and its replies",
	}
	cmd.AddCommand(a.newThreadReadCmd())
	return cmd
}

// threadView is the documented --json shape of `teams thread read`.
type threadView struct {
	TeamID    string          `json:"teamId,omitempty"`
	ChannelID string          `json:"channelId,omitempty"`
	ChatID    string          `json:"chatId,omitempty"`
	MessageID string          `json:"messageId"`
	Root      graph.Message   `json:"root"`
	Replies   []graph.Message `json:"replies,omitempty"`
}

func (a *App) newThreadReadCmd() *cobra.Command {
	var (
		teamFlag string
		flags    listFlags
		window   windowFlags
	)
	cmd := &cobra.Command{
		Use:   "read <message>",
		Short: "Read one message and, for a channel, its thread",
		Long: "Read a message. <message> is a Teams message link, Team/Channel/MessageId,\n" +
			"Team/Channel/<id> or a chat message link. Channel threads are read with the\n" +
			"messages/{id} and messages/{id}/replies endpoints; chats have no threads, so a\n" +
			"chat message is shown on its own.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Message(ctx, args[0], teamFlag)
			if err != nil {
				return err
			}
			since, until, err := window.window(a.Clock.Now())
			if err != nil {
				return err
			}
			if resolved.InChat {
				if err := a.requireScopes(ctx, "teams thread read", []string{"Chat.Read", "Chat.ReadWrite"}); err != nil {
					return err
				}
				msg, err := resolver.Client().GetChatMessage(ctx, resolved.ChatID, resolved.MessageID)
				if err != nil {
					return err
				}
				view := threadView{ChatID: resolved.ChatID, MessageID: resolved.MessageID, Root: msg}
				if a.Printer.JSONMode() {
					return a.Printer.JSON(view)
				}
				a.Printer.Statusf("chats have no threads; showing the message from %s", chatLabel(resolved))
				return a.printMessages([]graph.Message{msg}, messageOptions{container: chatLabel(resolved)})
			}
			if err := a.requireScopes(ctx, "teams thread read", []string{"ChannelMessage.Read.All", "Group.Read.All", "Group.ReadWrite.All"}); err != nil {
				return err
			}
			client := resolver.Client()
			root, err := client.GetChannelMessage(ctx, resolved.TeamID, resolved.ChannelID, resolved.MessageID)
			if err != nil {
				return err
			}
			// A reply id is a valid message id for the get endpoint, but its thread
			// is addressed by the root, so walk up one level.
			if root.IsReply() {
				root, err = client.GetChannelMessage(ctx, resolved.TeamID, resolved.ChannelID, root.ReplyToID)
				if err != nil {
					return err
				}
			}
			replies, err := client.ListChannelReplies(ctx, resolved.TeamID, resolved.ChannelID, root.ID, graph.MessageQuery{
				Top:   graph.MaxTopMessages,
				Limit: flags.limitOf(0),
				Since: since,
				Until: until,
			})
			if err != nil {
				return err
			}
			root.Replies = replies
			view := threadView{
				TeamID:    resolved.TeamID,
				ChannelID: resolved.ChannelID,
				MessageID: resolved.MessageID,
				Root:      root,
				Replies:   replies,
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(view)
			}
			return a.printMessages([]graph.Message{root}, messageOptions{container: channelLabel(resolved), replies: true})
		},
	}
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to (when the reference does not say)")
	cmd.Flags().IntVar(&flags.limit, "limit", 0, "maximum number of replies (0 means every page)")
	cmd.Flags().BoolVar(&flags.all, "all", false, "fetch every page of replies")
	cmd.Flags().StringVar(&window.since, "since", "", "only replies newer than this (a duration like 24h, or a timestamp)")
	cmd.Flags().StringVar(&window.until, "until", "", "only replies older than this (a duration like 24h, or a timestamp)")
	return cmd
}
