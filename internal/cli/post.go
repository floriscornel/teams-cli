package cli

import (
	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
)

// Message writes: post a root message to a channel or a chat, and reply in a
// thread. The flags and the body building are in write.go; these commands
// resolve the container, check the scope tier and send one request.

func (a *App) newPostCmd() *cobra.Command {
	var (
		flags    messageFlags
		teamFlag string
	)
	cmd := &cobra.Command{
		Use:   "post <channel|chat> [text|-]",
		Short: "Post a message to a channel or a chat",
		Long: "Post a message.\n\n" +
			"The container is a channel (Engineering/General, a channel link or a channel\n" +
			"id, with --team when the reference does not name the team) or a chat (a chat\n" +
			"link or id, a topic name, @person or an e-mail address). A bare name is a chat\n" +
			"topic: a channel name is only unique inside its team, so a channel is always\n" +
			"written Team/Channel.\n\n" +
			"The text is markdown by default (--md), or plain text (--text) or HTML\n" +
			"(--html). With no text on a terminal, $EDITOR opens; `-` reads stdin. Files\n" +
			"(--file) are uploaded before the message is sent, and an image that fits the\n" +
			"4 MB inline-image limit is embedded in the message instead.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			mode, err := flags.validate(cmd)
			if err != nil {
				return err
			}
			text, err := a.readBodyText(args[1:])
			if err != nil {
				return err
			}
			// The scope gate runs before the container is resolved, so a missing
			// scope fails without any call (PLAN.md:66).
			isChat, err := classifyChat(args[0])
			if err != nil {
				return err
			}
			if err := a.requireMessageScope(ctx, "teams post", isChat); err != nil {
				return err
			}
			if len(flags.files) > 0 {
				if err := a.requireFileScope(ctx, "teams post --file", isChat); err != nil {
					return err
				}
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			container, err := a.resolveWriteContainer(ctx, resolver, args[0], teamFlag)
			if err != nil {
				return err
			}
			payload, err := a.buildPayload(ctx, flags, mode, text, container, resolver, !flags.dryRun)
			if err != nil {
				return err
			}
			if flags.dryRun {
				return a.printDryRun(dryRunDocument{
					Method:  "POST",
					Path:    messageTargetPath(container.Target),
					Body:    payload.Post,
					Uploads: payload.Uploads,
				})
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			msg, err := client.PostMessage(ctx, container.Target, payload.Post)
			if err != nil {
				return err
			}
			return a.reportPosted("posted", msg)
		},
	}
	flags.bind(cmd)
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to, when the reference does not say")
	return cmd
}

func (a *App) newReplyCmd() *cobra.Command {
	var (
		flags    messageFlags
		teamFlag string
	)
	cmd := &cobra.Command{
		Use:   "reply <message> [text|-]",
		Short: "Reply to a message",
		Long: "Reply to a message. In a channel this posts into the message's thread;\n" +
			"chats have no threads, so a chat reply quotes the message it answers\n" +
			"(replyWithQuote, refs/graph/api-reference/v1.0/api/chatmessage-replywithquote.md).\n\n" +
			"<message> is a Teams message link, Team/Channel/<messageId>, or a chat message\n" +
			"reference. The flags are the same as `teams post`.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			mode, err := flags.validate(cmd)
			if err != nil {
				return err
			}
			text, err := a.readBodyText(args[1:])
			if err != nil {
				return err
			}
			isChat, err := classifyChat(args[0])
			if err != nil {
				return err
			}
			if err := a.requireMessageScope(ctx, "teams reply", isChat); err != nil {
				return err
			}
			if len(flags.files) > 0 {
				// A reply carries attachments like a post does, so the same upload
				// scope applies.
				if err := a.requireFileScope(ctx, "teams reply --file", isChat); err != nil {
					return err
				}
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			target, resolved, err := a.resolveMessageTarget(ctx, resolver, args[0], teamFlag)
			if err != nil {
				return err
			}
			container := writeContainer{Target: target, Ref: resolved, IsChat: target.IsChat()}
			payload, err := a.buildPayload(ctx, flags, mode, text, container, resolver, !flags.dryRun)
			if err != nil {
				return err
			}
			if flags.dryRun {
				path := messageTargetPath(target)
				var body any = payload.Post
				if target.IsChat() {
					// The documented quote-reply body (chatmessage-replywithquote.md:52-55).
					path = "/chats/" + target.ChatID + "/messages/replyWithQuote"
					body = map[string]any{"replyMessage": payload.Post, "messageIds": []string{target.MessageID}}
				} else {
					path += "/replies"
				}
				return a.printDryRun(dryRunDocument{Method: "POST", Path: path, Body: body, Uploads: payload.Uploads})
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			msg, err := client.PostReply(ctx, target, payload.Post)
			if err != nil {
				return err
			}
			return a.reportPosted("replied", msg)
		},
	}
	flags.bind(cmd)
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to, when the reference does not say")
	return cmd
}

// reportPosted renders a created message: the id on stdout (so a script can
// grab it), the link below it, and the whole message under --json.
func (a *App) reportPosted(verb string, msg graph.Message) error {
	if a.Printer.JSONMode() {
		return a.Printer.JSON(msg)
	}
	if msg.ID == "" {
		return output.Errorf("Graph did not return the created message")
	}
	a.Printer.Successf("%s %s", verb, msg.ID)
	if msg.WebURL != "" {
		a.Printer.Printf("  %s\n", a.Printer.Dim(msg.WebURL))
	}
	return nil
}
