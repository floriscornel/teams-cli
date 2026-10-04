package cli

import (
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/ref"
)

// Chat commands: list, show, read and — since Phase 4 — create, add-member,
// delete and the per-user read state. Chats are the container with the working
// server-side read state: GET /me/chats carries viewpoint.lastMessageReadDateTime,
// which is what makes `chat list --unread` and `teams unread` exact
// (refs/graph/api-reference/v1.0/resources/chatviewpoint.md:23).

func (a *App) newChatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chat",
		Short: "List, show, read and manage your chats",
	}
	cmd.AddCommand(
		a.newChatListCmd(), a.newChatShowCmd(), a.newChatReadCmd(),
		// Phase 4 writes.
		a.newChatCreateCmd(), a.newChatAddMemberCmd(), a.newChatDeleteCmd(),
		a.newChatReadStateCmd(true), a.newChatReadStateCmd(false),
	)
	return cmd
}

// newChatCreateCmd creates a chat with the people named by --with.
//
// The documented shape forces three things (refs/graph/api-reference/v1.0/api/chat-post.md:47):
// every participant including the caller must be listed, every member carries a
// role of owner or guest, and a topic is only valid on a group chat. A
// one-on-one chat is unique - creating the one that already exists returns the
// existing chat (:16).
func (a *App) newChatCreateCmd() *cobra.Command {
	var (
		with   []string
		topic  string
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "create --with <person> [--with <person>…] [--topic <title>]",
		Short: "Create a group chat or a one-on-one chat",
		Long: "Create a chat. --with names the other participants (repeatable); you are added\n" +
			"automatically. One participant makes a one-on-one chat, which the API returns\n" +
			"from its cache when that chat already exists; two or more make a group chat,\n" +
			"where --topic is allowed.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			if len(with) == 0 {
				return output.Usagef("name at least one participant with --with")
			}
			if err := a.requireScopes(ctx, "teams chat create", []string{"Chat.Create", "Chat.ReadWrite"}); err != nil {
				return err
			}
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			me, err := a.Me(ctx)
			if err != nil {
				return err
			}
			members := []graph.ChatMemberSpec{{UserID: me.ID, DisplayName: me.DisplayName, Roles: []string{graph.RoleOwner}}}
			var oneOnOneID string
			for _, person := range with {
				resolved, err := resolver.Person(ctx, person)
				if err != nil {
					return err
				}
				if resolved.UserID == me.ID {
					// The caller is already listed; naming yourself is a no-op
					// rather than a duplicate member Graph would reject.
					continue
				}
				oneOnOneID = resolved.UserID
				members = append(members, graph.ChatMemberSpec{
					UserID: resolved.UserID, DisplayName: resolved.UserName, Roles: []string{graph.RoleOwner},
				})
			}
			if len(members) < 2 {
				return output.Usagef("a chat needs at least two participants; --with did not add anyone else")
			}
			in := graph.ChatCreate{ChatType: graph.ChatTypeGroup, Topic: topic, Members: members}
			if len(members) == 2 {
				in.ChatType = graph.ChatTypeOneOnOne
			}
			if dryRun {
				return a.printDryRun(dryRunDocument{
					Method: "POST", Path: "/chats", Body: chatCreatePreview(in, a.GraphBaseURL()),
				})
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			chat, err := client.CreateChat(ctx, in)
			if err != nil {
				return err
			}
			// Remember the chat for that person, so a later read finds it without
			// the members scan (PLAN.md:166).
			if resolver.Cache() != nil && in.ChatType == graph.ChatTypeOneOnOne {
				resolver.Cache().PutPersonChat(ref.PersonKey(oneOnOneID), chat.ID)
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(chat)
			}
			a.Printer.Successf("created %s", chatTitle(chat))
			a.Printer.Definitions([][2]string{
				{"id", chat.ID},
				{"type", chat.ChatType},
				{"members", chatMemberNames(chat, members)},
			})
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&with, "with", nil, "person to include (repeatable): @name, an e-mail address or a user id")
	cmd.Flags().StringVar(&topic, "topic", "", "title of the chat (group chats only)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the Graph request without sending it")
	return cmd
}

// chatCreatePreview renders the POST /chats body for --dry-run. The
// user@odata.bind URL is the documented member address
// (refs/graph/api-reference/v1.0/api/chat-post.md:615-620).
func chatCreatePreview(in graph.ChatCreate, baseURL string) map[string]any {
	members := make([]map[string]any, 0, len(in.Members))
	for _, member := range in.Members {
		roles := member.Roles
		if len(roles) == 0 {
			roles = []string{graph.RoleOwner}
		}
		members = append(members, map[string]any{
			"@odata.type":     "#microsoft.graph.aadUserConversationMember",
			"roles":           roles,
			"user@odata.bind": baseURL + "/users('" + member.UserID + "')",
		})
	}
	body := map[string]any{"chatType": in.ChatType, "members": members}
	if in.ChatType == graph.ChatTypeGroup && in.Topic != "" {
		body["topic"] = in.Topic
	}
	return body
}

// chatMemberNames lists the participants to show after a create.
func chatMemberNames(chat graph.Chat, fallback []graph.ChatMemberSpec) string {
	names := make([]string, 0, len(chat.Members))
	for _, member := range chat.Members {
		if name := memberName(member); name != "" {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		for _, member := range fallback {
			names = append(names, firstNonEmptyString(member.DisplayName, member.UserID))
		}
	}
	return strings.Join(names, ", ")
}

// newChatAddMemberCmd adds people to an existing chat.
func (a *App) newChatAddMemberCmd() *cobra.Command {
	var dryRun bool
	cmd := &cobra.Command{
		Use:   "add-member <chat> <person…>",
		Short: "Add one or more members to a chat",
		Long: "Add members to a chat. Each one is added with the documented owner role: a\n" +
			"guest role is a tenant decision, and `member` is not accepted in the delegated\n" +
			"flow (refs/graph/api-reference/v1.0/api/chat-post.md:47).\n\n" +
			"Every member is attempted, so one failure does not hide the others; the\n" +
			"command exits 1 when any of them failed.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams chat add-member", []string{"Chat.ReadWrite"}); err != nil {
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
			people := make([]ref.Ref, 0, len(args)-1)
			for _, person := range args[1:] {
				user, err := resolver.Person(ctx, person)
				if err != nil {
					return err
				}
				people = append(people, user)
			}
			if dryRun {
				doc := dryRunDocument{Method: "POST", Path: "/chats/" + resolved.ChatID + "/members"}
				for _, person := range people {
					doc.Paths = append(doc.Paths, "/chats/"+resolved.ChatID+"/members  user@odata.bind="+
						a.GraphBaseURL()+"/users/"+person.UserID)
				}
				return a.printDryRun(doc)
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			var failed int
			type addResult struct {
				Name string `json:"name"`
				ID   string `json:"id,omitempty"`
				Err  string `json:"error,omitempty"`
			}
			results := make([]addResult, 0, len(people))
			for _, person := range people {
				member, err := client.AddChatMember(ctx, resolved.ChatID, person.UserID, []string{graph.RoleOwner})
				if err != nil {
					failed++
					results = append(results, addResult{Name: person.UserName, Err: err.Error()})
					if !a.Printer.JSONMode() {
						a.Printer.Warnf("%s: %v", firstNonEmptyString(person.UserName, person.UserID), err)
					}
					continue
				}
				results = append(results, addResult{Name: memberName(member), ID: member.ID})
				if !a.Printer.JSONMode() {
					a.Printer.Successf("added %s to %s", firstNonEmptyString(person.UserName, person.UserID), chatLabel(resolved))
				}
			}
			if a.Printer.JSONMode() {
				if err := a.Printer.JSON(map[string]any{"chat": resolved.ChatID, "added": results}); err != nil {
					return err
				}
			}
			if failed > 0 {
				return output.Errorf("%d of %d members could not be added", failed, len(people))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the Graph requests without sending them")
	return cmd
}

// newChatDeleteCmd deletes a chat. It is the one command that needs a scope no
// preset carries: Chat.ManageDeletion.All is admin-consented, so the CLI asks to
// sign in again with it when it is missing, and otherwise fails with exit 3 and
// the admin ticket (PLAN.md:62).
func (a *App) newChatDeleteCmd() *cobra.Command {
	var (
		dryRun bool
		yes    bool
	)
	cmd := &cobra.Command{
		Use:   "delete <chat>",
		Short: "Delete a chat (needs admin consent, requested on demand)",
		Long: "Delete a chat.\n\n" +
			"Graph needs Chat.ManageDeletion.All for this, which no preset carries because an\n" +
			"admin has to consent to it. On a terminal the CLI asks to sign in again with that\n" +
			"scope; anywhere else it fails with exit 3 and the text of the admin request\n" +
			"(`teams auth status --admin-request`).\n\n" +
			"Deleting a chat is confirmed first; --yes skips the prompt, and is required in\n" +
			"non-interactive mode.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			resolver, err := a.resolver(ctx)
			if err != nil {
				return err
			}
			resolved, err := resolver.Chat(ctx, args[0])
			if err != nil {
				return err
			}
			if err := a.requireIncrementalScope(ctx, "teams chat delete", "Chat.ManageDeletion.All"); err != nil {
				return err
			}
			if dryRun {
				return a.printDryRun(dryRunDocument{Method: "DELETE", Path: "/chats/" + resolved.ChatID})
			}
			if err := a.confirm("delete the chat "+chatLabel(resolved)+"?", yes); err != nil {
				return err
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			if err := client.DeleteChat(ctx, resolved.ChatID); err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]string{"chat": resolved.ChatID, "status": "deleted"})
			}
			a.Printer.Successf("deleted %s", chatLabel(resolved))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the Graph request without sending it")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func (a *App) newChatReadStateCmd(read bool) *cobra.Command {
	use, short := "mark-read", "Mark a chat read"
	if !read {
		use, short = "mark-unread", "Mark a chat unread"
	}
	var dryRun bool
	cmd := &cobra.Command{
		Use:   use + " <chat>",
		Short: short,
		Long: "Move your own read watermark for a chat.\n\n" +
			"Graph stores it in the chat's viewpoint, which is what `teams unread` and\n" +
			"`teams chat list --unread` read back\n" +
			"(refs/graph/api-reference/v1.0/resources/chatviewpoint.md).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if err := a.requireScopes(ctx, "teams chat "+use, []string{"Chat.ReadWrite"}); err != nil {
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
			me, err := a.Me(ctx)
			if err != nil {
				return err
			}
			action := "markChatReadForUser"
			if !read {
				action = "markChatUnreadForUser"
			}
			if dryRun {
				return a.printDryRun(dryRunDocument{
					Method: "POST",
					Path:   "/chats/" + resolved.ChatID + "/" + action,
					Body:   map[string]any{"user": map[string]string{"id": me.ID}},
				})
			}
			client, err := a.Graph(ctx)
			if err != nil {
				return err
			}
			if read {
				err = client.MarkChatRead(ctx, resolved.ChatID, me.ID)
			} else {
				err = client.MarkChatUnread(ctx, resolved.ChatID, me.ID)
			}
			if err != nil {
				return err
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(map[string]any{"chat": resolved.ChatID, "read": read})
			}
			if read {
				a.Printer.Successf("marked %s read", chatLabel(resolved))
				return nil
			}
			a.Printer.Successf("marked %s unread", chatLabel(resolved))
			return nil
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the Graph request without sending it")
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
