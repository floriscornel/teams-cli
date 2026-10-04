package cli

import (
	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
)

// Message actions: edit, soft delete and the two reaction verbs. Each one acts
// on a single message reference, which resolveMessageTarget turns into the root
// and reply pair the Graph routes need.

// messageEditScopes is the any-of scope set for editing or deleting a message:
// ChannelMessage.ReadWrite (with Group.ReadWrite.All as the documented
// alternative) on a channel message and Chat.ReadWrite on a chat message
// (refs/graph/api-reference/v1.0/api/chatmessage-update.md:34,45).
func messageEditScopes(isChat bool) []string {
	if isChat {
		return []string{"Chat.ReadWrite"}
	}
	return []string{"ChannelMessage.ReadWrite", "Group.ReadWrite.All"}
}

// messageReactScopes is the any-of scope set for a reaction:
// ChannelMessage.Send on a channel message, Chat.ReadWrite or ChatMessage.Send
// on a chat message (refs/graph/api-reference/v1.0/api/chatmessage-setreaction.md:27,35).
func messageReactScopes(isChat bool) []string {
	if isChat {
		return []string{"Chat.ReadWrite", "ChatMessage.Send"}
	}
	return []string{"ChannelMessage.Send"}
}

func (a *App) newEditCmd() *cobra.Command {
	var (
		flags    messageFlags
		teamFlag string
	)
	cmd := &cobra.Command{
		Use:   "edit <message> [text|-]",
		Short: "Edit a message you sent",
		Long: "Edit a message's body (and, with --subject, its subject).\n\n" +
			"Only the body and the subject are editable, which is what the delegated PATCH\n" +
			"documents (refs/graph/api-reference/v1.0/api/chatmessage-update.md:44-45);\n" +
			"attachments cannot be added to an existing message, so --file is refused.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			mode, err := flags.validate(cmd)
			if err != nil {
				return err
			}
			if len(flags.files) > 0 {
				return output.Usagef("--file cannot be used with edit: a PATCH only changes the body and the subject")
			}
			text, err := a.readBodyText(args[1:])
			if err != nil {
				return err
			}
			isChat, err := classifyChat(args[0])
			if err != nil {
				return err
			}
			// The scope gate runs before the message is resolved (PLAN.md:66).
			if err := a.requireScopes(ctx, "teams edit", messageEditScopes(isChat)); err != nil {
				return err
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
			payload, err := a.buildPayload(ctx, flags, mode, text, container, resolver, false) //nolint:staticcheck // the container labels the payload
			if err != nil {
				return err
			}
			patch := graph.MessagePatch{Body: &payload.Post.Body}
			if cmd.Flags().Changed("subject") {
				subject := payload.Post.Subject
				patch.Subject = &subject
			}
			path := messageTargetPath(target)
			if flags.dryRun {
				return a.printDryRun(dryRunDocument{Method: "PATCH", Path: path, Body: patch})
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			if err := client.UpdateMessage(ctx, target, patch); err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]string{"id": messageID(target), "status": "edited"})
			}
			a.Printer.Successf("edited %s", messageID(target))
			return nil
		},
	}
	flags.bind(cmd)
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to, when the reference does not say")
	return cmd
}

func (a *App) newDeleteCmd() *cobra.Command {
	var (
		teamFlag string
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "delete <message>",
		Short: "Delete a message (soft delete)",
		Long: "Delete a message the way Teams does: a soft delete, which keeps the id and\n" +
			"leaves a tombstone in the conversation\n" +
			"(refs/graph/api-reference/v1.0/api/chatmessage-softdelete.md).\n\n" +
			"A chat message is deleted through the user-relative route the docs document\n" +
			"(POST /users/{user-id}/chats/{chat-id}/messages/{id}/softDelete), because the\n" +
			"/chats form answers 405 (docs/spike/phase1.md:99).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			isChat, err := classifyChat(args[0])
			if err != nil {
				return err
			}
			if err := a.requireScopes(ctx, "teams delete", messageEditScopes(isChat)); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			target, _, err := a.resolveMessageTarget(ctx, resolver, args[0], teamFlag)
			if err != nil {
				return err
			}
			path := messageTargetPath(target) + "/softDelete"
			if target.IsChat() {
				// The documented chat form is user-relative, so the caller's own id
				// is part of the request (see the command's Long text).
				me, err := a.Me(ctx)
				if err != nil {
					return err
				}
				target.UserID = me.ID
				path = "/users/" + me.ID + "/chats/" + target.ChatID + "/messages/" + target.MessageID + "/softDelete"
			}
			if dryRun {
				return a.printDryRun(dryRunDocument{Method: "POST", Path: path})
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			if err := client.SoftDeleteMessage(ctx, target); err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]string{"id": messageID(target), "status": "deleted"})
			}
			a.Printer.Successf("deleted %s", messageID(target))
			return nil
		},
	}
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to, when the reference does not say")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the Graph request without sending it")
	return cmd
}

func (a *App) newReactCmd() *cobra.Command {
	var (
		teamFlag string
		remove   bool
		dryRun   bool
	)
	cmd := &cobra.Command{
		Use:   "react <message> <emoji>",
		Short: "React to a message, or remove your reaction",
		Long: "React to a message with an emoji, or remove the reaction with --remove.\n\n" +
			"The reaction is sent as the reactionType string Graph documents, which is the\n" +
			"unicode character itself (\"like\" is the one named example); the CLI passes it\n" +
			"through, so any emoji Teams accepts works\n" +
			"(refs/graph/api-reference/v1.0/api/chatmessage-setreaction.md:68).",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len([]rune(args[1])) == 0 {
				return output.Usagef("a reaction needs an emoji, for example 👍")
			}
			isChat, err := classifyChat(args[0])
			if err != nil {
				return err
			}
			if err := a.requireScopes(ctx, "teams react", messageReactScopes(isChat)); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			target, _, err := a.resolveMessageTarget(ctx, resolver, args[0], teamFlag)
			if err != nil {
				return err
			}
			action := "setReaction"
			if remove {
				action = "unsetReaction"
			}
			if dryRun {
				return a.printDryRun(dryRunDocument{
					Method: "POST",
					Path:   messageTargetPath(target) + "/" + action,
					Body:   map[string]string{"reactionType": args[1]},
				})
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			if err := client.React(ctx, target, args[1], remove); err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				verb := "reacted"
				if remove {
					verb = "unreacted"
				}
				return a.Printer.JSON(map[string]string{"id": messageID(target), "reaction": args[1], "status": verb})
			}
			if remove {
				a.Printer.Successf("removed %s from %s", args[1], messageID(target))
				return nil
			}
			a.Printer.Successf("reacted %s to %s", args[1], messageID(target))
			return nil
		},
	}
	cmd.Flags().StringVar(&teamFlag, "team", "", "team the channel belongs to, when the reference does not say")
	cmd.Flags().BoolVar(&remove, "remove", false, "remove your reaction instead of adding it")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the Graph request without sending it")
	return cmd
}

// messageID is the id a message action reports: the reply when the reference
// points at one, because that is the message the user named.
func messageID(target graph.MessageTarget) string {
	if target.ReplyID != "" {
		return target.ReplyID
	}
	return target.MessageID
}
