package cli

import (
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/ref"
)

// Chat commands: list, show and read. Chats are the container with the working
// server-side read state: GET /me/chats carries viewpoint.lastMessageReadDateTime,
// which is what makes `chat list --unread` and `teams unread` exact
// (refs/graph/api-reference/v1.0/resources/chatviewpoint.md:23).

func (a *App) newChatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "List, show and read your chats",
	}
	cmd.AddCommand(a.newChatListCmd(), a.newChatShowCmd(), a.newChatReadCmd())
	return cmd
}

func (a *App) newChatListCmd() *cobra.Command {
	var (
		with   string
		topic  string
		unread bool
		flags  listFlags
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List your chats, newest activity first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams chat list", []string{"Chat.ReadBasic", "Chat.Read", "Chat.ReadWrite"}); err != nil {
				return err
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			// One page by default. A plain listing wants the newest chats, and
			// paging the whole tenant - with $expand=members on every page - is
			// slow enough to look like a hang: --all asks for it explicitly, and
			// --with/--unread need it, because the chat (or the unread one) may be
			// older than the newest page.
			limit := flags.limitOf(defaultChatLimit)
			if flags.all || with != "" || unread {
				limit = flags.limitOf(0)
			}
			// --unread means the same thing as `teams unread`: chats with unread
			// activity in the last 24h, unless --all asks for every unread chat
			// however old (a meeting chat read state is often never set, so the
			// unbounded form fills up with years-old chats).
			since := time.Time{}
			if unread && !flags.all {
				since = a.Clock.Now().Add(-defaultUnreadWindow)
			}
			a.Printer.Statusf("fetching chats...")
			chats, err := client.ListChats(ctx, graph.ChatQuery{
				Top: graph.MaxTopChats,
				// lastMessagePreview carries the newest message, which the unread
				// rule compares against viewpoint.lastMessageReadDateTime, and it
				// is also the only property $orderby accepts
				// (refs/graph/api-reference/v1.0/api/chat-list.md:63).
				LastMessagePreview: true,
				OrderByLastMessage: true,
				// Members are what names a 1:1 chat: it has no topic, so its title is
				// built from the other participants. The documented cap is 25 member
				// items per chat (refs/graph/api-reference/v1.0/api/chat-list.md:20).
				Members: true,
				Limit:   limit,
				Since:   since,
			})
			if err != nil {
				return err
			}
			if with != "" {
				// --with is the only part of a chat listing that resolves a
				// reference, so the resolver is built only then.
				resolver, rerr := a.resolver(ctx)
				if rerr != nil {
					return rerr
				}
				person, perr := resolver.Person(ctx, with)
				if perr != nil {
					return perr
				}
				chats = filterChatsWith(chats, person.UserID)
			}
			if topic != "" {
				chats = filterChatsByTopic(chats, topic)
			}
			if unread {
				chats = filterUnreadChats(chats)
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(chats)
			}
			rows := make([][]string, 0, len(chats))
			for _, chat := range chats {
				rows = append(rows, []string{chatTitle(chat), chat.ChatType, unreadMark(chat), lastActivity(chat), chat.ID})
			}
			a.Printer.Table([]string{"TITLE", "TYPE", "UNREAD", "LAST ACTIVITY", "ID"}, rows)
			return nil
		},
	}
	cmd.Flags().StringVar(&with, "with", "", "only the 1:1 chat with this person")
	cmd.Flags().StringVar(&topic, "topic", "", "only chats whose topic contains this text")
	cmd.Flags().BoolVar(&unread, "unread", false, "only chats with unread messages")
	cmd.Flags().IntVar(&flags.limit, "limit", defaultChatLimit, "maximum number of chats, newest first")
	cmd.Flags().BoolVar(&flags.all, "all", false, "page through every chat (slow on a busy tenant: each page expands the members)")
	return cmd
}

func (a *App) newChatShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <chat>",
		Short: "Show one chat",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams chat show", []string{"Chat.ReadBasic", "Chat.Read", "Chat.ReadWrite"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Chat(ctx, args[0])
			if err != nil {
				return err
			}
			chat, err := resolver.Client().GetChat(ctx, resolved.ChatID, true)
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(chat)
			}
			members := make([]string, 0, len(chat.Members))
			for _, member := range chat.Members {
				members = append(members, memberName(member))
			}
			a.Printer.Definitions([][2]string{
				{"title", chatTitle(chat)},
				{"id", chat.ID},
				{"type", chat.ChatType},
				{"members", strings.Join(members, ", ")},
				{"created", formatTime(chat.CreatedDateTime)},
				{"updated", formatTime(chat.LastUpdatedDateTime)},
				{"unread", unreadMark(chat)},
				{"webUrl", chat.WebURL},
			})
			return nil
		},
	}
	return cmd
}

func (a *App) newChatReadCmd() *cobra.Command {
	var (
		from   string
		flags  listFlags
		window windowFlags
	)
	cmd := &cobra.Command{
		Use:   "read <chat>",
		Short: "Read a chat's messages",
		Long: "Read a chat's messages. --since and --until are applied on the server: the\n" +
			"chat message endpoint documents $orderby and a $filter with gt/lt on one date\n" +
			"property, and a $filter that does not match the $orderby is silently ignored -\n" +
			"so --since uses lastModifiedDateTime gt and the result is re-checked on\n" +
			"createdDateTime on the client (PLAN.md:183).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams chat read", []string{"Chat.Read", "Chat.ReadWrite"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Chat(ctx, args[0])
			if err != nil {
				return err
			}
			since, until, err := window.window(a.Clock.Now())
			if err != nil {
				return err
			}
			msgs, err := resolver.Client().ListChatMessages(ctx, resolved.ChatID, graph.MessageQuery{
				Top:   graph.MaxTopMessages,
				Limit: flags.limitOf(defaultMessageLimit),
				Since: since,
				Until: until,
			})
			if err != nil {
				return err
			}
			if from != "" {
				person, perr := resolver.Person(ctx, from)
				if perr != nil {
					return perr
				}
				msgs = filterMessagesBySender(msgs, person.UserID)
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(msgs)
			}
			if len(msgs) == 0 {
				a.Printer.Statusf("no messages in %s", chatLabel(resolved))
				return nil
			}
			return a.printMessages(msgs, messageOptions{container: chatLabel(resolved)})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "only messages sent by this person")
	cmd.Flags().IntVar(&flags.limit, "limit", defaultMessageLimit, "maximum number of messages (newest first)")
	cmd.Flags().BoolVar(&flags.all, "all", false, "fetch every page instead of --limit")
	cmd.Flags().StringVar(&window.since, "since", "", "only messages newer than this (a duration like 24h, or a timestamp)")
	cmd.Flags().StringVar(&window.until, "until", "", "only messages older than this (a duration like 24h, or a timestamp)")
	return cmd
}

// chatLabel names a chat for a message header.
func chatLabel(resolved ref.Ref) string {
	return firstNonEmptyString(resolved.ChatTopic, resolved.UserName, resolved.ChatID)
}

// chatTitle names a chat: its topic, then the other members, then the id.
func chatTitle(chat graph.Chat) string {
	if topic := chat.Topic; topic != nil && *topic != "" {
		return *topic
	}
	names := make([]string, 0, len(chat.Members))
	for _, member := range chat.Members {
		name := memberName(member)
		if name != "" {
			names = append(names, name)
		}
	}
	switch len(names) {
	case 0:
		return chat.ID
	case 1, 2, 3:
		return strings.Join(names, ", ")
	default:
		return strings.Join(names[:3], ", ") + " +" + strconv.Itoa(len(names)-3)
	}
}

// memberName is the name to show for a conversation member.
func memberName(member graph.ConversationMember) string {
	if member.DisplayName != "" {
		return member.DisplayName
	}
	if member.Email != "" {
		return member.Email
	}
	return member.UserID
}

// unreadMark renders the read state of a chat.
func unreadMark(chat graph.Chat) string {
	if chat.Viewpoint == nil && chat.LastMessagePreview == nil {
		return ""
	}
	if chat.Unread() {
		return "yes"
	}
	return "no"
}

// lastActivity renders when a chat last moved, from its message preview.
func lastActivity(chat graph.Chat) string {
	if chat.LastMessagePreview != nil && !chat.LastMessagePreview.CreatedDateTime.IsZero() {
		return chat.LastMessagePreview.CreatedDateTime.UTC().Format("2006-01-02 15:04")
	}
	if !chat.LastUpdatedDateTime.IsZero() {
		return chat.LastUpdatedDateTime.UTC().Format("2006-01-02 15:04")
	}
	return ""
}

// filterChatsWith keeps the 1:1 chats that include the given user.
func filterChatsWith(chats []graph.Chat, userID string) []graph.Chat {
	out := make([]graph.Chat, 0, len(chats))
	for _, chat := range chats {
		// A group chat that happens to contain the person is not "the chat with"
		// them; only a 1:1 chat is.
		if chat.ChatType != "oneOnOne" {
			continue
		}
		for _, member := range chat.Members {
			if member.UserID == userID {
				out = append(out, chat)
				break
			}
		}
	}
	return out
}

// filterChatsByTopic keeps the chats whose topic contains the text, because the
// API own $filter on topic is an exact match and a person types part of a name.
func filterChatsByTopic(chats []graph.Chat, topic string) []graph.Chat {
	out := make([]graph.Chat, 0, len(chats))
	for _, chat := range chats {
		if chat.Topic != nil && strings.Contains(strings.ToLower(*chat.Topic), strings.ToLower(topic)) {
			out = append(out, chat)
		}
	}
	return out
}

// filterUnreadChats keeps the chats whose newest message is newer than the read
// watermark (PLAN.md:149).
func filterUnreadChats(chats []graph.Chat) []graph.Chat {
	out := make([]graph.Chat, 0, len(chats))
	for _, chat := range chats {
		if chat.Unread() {
			out = append(out, chat)
		}
	}
	return out
}

// filterMessagesBySender keeps the messages one person sent.
func filterMessagesBySender(msgs []graph.Message, userID string) []graph.Message {
	out := make([]graph.Message, 0, len(msgs))
	for _, msg := range msgs {
		sender := msg.From.Sender()
		if sender.ID == userID {
			out = append(out, msg)
		}
	}
	return out
}
