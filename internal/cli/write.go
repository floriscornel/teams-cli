package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/auth"
	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/format"
	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/ref"
)

// This file holds what every Phase 4 write command shares: reading the text of a
// message (an argument, stdin or $EDITOR), turning it into a Graph body
// (markdown, plain text or HTML, with mentions and attachments), resolving the
// container a command writes to, and the --dry-run document.
//
// The order inside buildPayload is the contract PLAN.md:179 states: the body is
// sanitized *first* and mentions are inserted *afterwards*, because `<at>` is not
// in the Teams allow-list and a sanitizer pass over the assembled body silently
// drops every mention.

// messageFlags are the flags the message-writing commands share.
type messageFlags struct {
	md         bool
	text       bool
	html       bool
	mentions   []string
	files      []string
	subject    string
	importance string
	dryRun     bool
}

// bind registers the flags every message-writing command shares.
func (f *messageFlags) bind(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.md, "md", true, "treat the text as markdown (the default)")
	cmd.Flags().BoolVar(&f.text, "text", false, "send the text verbatim, without markdown or HTML")
	cmd.Flags().BoolVar(&f.html, "html", false, "send the text as HTML, sanitized against the Teams allow-list")
	cmd.Flags().StringArrayVar(&f.mentions, "mention", nil, "person to mention (repeatable); the text must name them, e.g. @alice")
	cmd.Flags().StringVar(&f.subject, "subject", "", "subject line (channels and chats both store one)")
	cmd.Flags().StringVar(&f.importance, "importance", "", "message importance: high or urgent (default normal)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the Graph request without sending it")
}

// bindFiles adds --file, for the commands that can send an attachment: post and
// reply. An edit cannot, because the delegated PATCH only changes properties of
// an existing message (refs/graph/api-reference/v1.0/api/chatmessage-update.md:44-45),
// so `edit` does not offer the flag at all rather than refusing it after the
// fact.
func (f *messageFlags) bindFiles(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.files, "file", nil, "file to attach (repeatable); an image up to 4 MB is embedded inline")
}

// contentTypeMode decides which of --md/--text/--html applies. --md is the
// default, so a conflict is only possible when more than one of them was set
// explicitly.
func (f *messageFlags) contentTypeMode(cmd *cobra.Command) (string, error) {
	explicit := make([]string, 0, 3)
	if cmd.Flags().Changed("md") && f.md {
		explicit = append(explicit, "--md")
	}
	if f.text {
		explicit = append(explicit, "--text")
	}
	if f.html {
		explicit = append(explicit, "--html")
	}
	if len(explicit) > 1 {
		return "", output.Usagef("%s are mutually exclusive", strings.Join(explicit, " and "))
	}
	switch {
	case f.text:
		return "text", nil
	case f.html:
		return "html", nil
	default:
		return "md", nil
	}
}

// importanceValues are the enum members chatMessage documents
// (refs/graph/api-reference/v1.0/resources/chatmessage.md:75).
var importanceValues = []string{"normal", "high", "urgent"}

// validate checks the flag combination before any network call.
func (f *messageFlags) validate(cmd *cobra.Command) (string, error) {
	mode, err := f.contentTypeMode(cmd)
	if err != nil {
		return "", err
	}
	if f.importance != "" && !containsFold(importanceValues, f.importance) {
		return "", output.Usagef("--importance %q: use normal, high or urgent", f.importance)
	}
	if mode == "text" && len(f.mentions) > 0 {
		// itemBody is either html or text, and Teams carries a mention only in
		// the HTML form: "The content is always in HTML if the chat message
		// contains a chatMessageMention" (refs/graph/api-reference/v1.0/resources/chatmessage.md).
		return "", output.WithHint(output.Usagef("--mention needs an HTML body"), "drop --text, or let the CLI convert the markdown (the default)")
	}
	return mode, nil
}

// readBodyText returns the message text: the argument, stdin when it is "-", or
// $EDITOR when the argument is omitted on a terminal.
func (a *App) readBodyText(args []string) (string, error) {
	switch {
	case len(args) == 0:
		if !a.Printer.Interactive() {
			return "", output.WithHint(output.Usagef("no message text"),
				"pass the text, or - to read stdin (non-interactive sessions never open an editor)")
		}
		return a.editorText(context.Background())
	case args[0] == "-":
		data, err := io.ReadAll(a.Stdin)
		if err != nil {
			return "", output.Errorf("read the message from stdin: %v", err)
		}
		return string(data), nil
	default:
		return args[0], nil
	}
}

// editorText opens $EDITOR (or $VISUAL) on a temp file and returns its contents.
// PLAN.md:176 makes this the interactive default when no text is given.
func (a *App) editorText(ctx context.Context) (string, error) {
	editor := firstNonEmptyString(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		return "", output.WithHint(output.Usagef("no editor configured"), "set $EDITOR, or pass the text (or - to read stdin)")
	}
	tmp, err := os.CreateTemp("", "teams-message-*.md")
	if err != nil {
		return "", output.Errorf("create a temp file for the editor: %v", err)
	}
	path := tmp.Name()
	defer func() {
		_ = os.Remove(path)
	}()
	if err := tmp.Close(); err != nil {
		return "", output.Errorf("close the temp file: %v", err)
	}
	cmd := exec.CommandContext(ctx, editor, path) //nolint:gosec // the user's own $EDITOR, by design
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.Stdin, a.Stderr, a.Stderr
	if err := cmd.Run(); err != nil {
		return "", output.Errorf("run %s: %v", editor, err)
	}
	//nolint:gosec // the path is the temp file this function created
	data, err := os.ReadFile(path)
	if err != nil {
		return "", output.Errorf("read the edited message: %v", err)
	}
	return string(data), nil
}

// writeContainer is the container a write acts on.
type writeContainer struct {
	// Target is the message target: a channel (team and channel) or a chat.
	Target graph.MessageTarget
	// Ref is the resolved reference, for labels.
	Ref    ref.Ref
	IsChat bool
}

// UploadTarget is where a file attachment for this container goes.
func (c writeContainer) UploadTarget() graph.UploadTarget {
	if c.IsChat {
		return graph.UploadTarget{Chat: true}
	}
	return graph.UploadTarget{TeamID: c.Target.TeamID, ChannelID: c.Target.ChannelID}
}

// resolveWriteContainer resolves the <channel|chat> argument of post.
//
// classifyChat decides which of the two families the reference belongs to (see
// there for the rule), and the resolution then asks the right resolver.
func (a *App) resolveWriteContainer(ctx context.Context, resolver *ref.Resolver, raw, teamHint string) (writeContainer, error) {
	parsed, err := ref.Parse(raw)
	if err != nil {
		return writeContainer{}, err
	}
	// A message reference names a message, not the container to post into: that
	// is `teams reply` (or `edit`/`delete`/`react`).
	if parsed.Kind == ref.KindMessage || parsed.MessageID != "" || len(parsed.Path) >= 3 {
		return writeContainer{}, output.WithHint(output.Usagef("%q is a message, not a container", raw),
			"use `teams reply` for a message, or name the channel or chat to post to")
	}
	isChat, err := classifyChat(raw)
	if err != nil {
		return writeContainer{}, err
	}
	if !isChat {
		resolved, err := resolver.Channel(ctx, raw, teamHint)
		if err != nil {
			return writeContainer{}, err
		}
		return writeContainer{
			Target: graph.MessageTarget{TeamID: resolved.TeamID, ChannelID: resolved.ChannelID},
			Ref:    resolved,
		}, nil
	}
	resolved, err := a.chatForWrite(ctx, resolver, raw)
	if err != nil {
		return writeContainer{}, err
	}
	return writeContainer{Target: graph.MessageTarget{ChatID: resolved.ChatID}, Ref: resolved, IsChat: true}, nil
}

// classifyChat decides, from a reference's shape alone, whether it names a chat.
//
// It exists so the scope gate can run *before* any Graph call, which is what
// PLAN.md:66 requires ("A command whose scope is missing fails before it calls
// Graph"): resolving a name needs the network, so the check cannot wait for the
// resolution. The rule is the one `post --help` documents, applied to
// containers and messages alike:
//
//   - a chat message link (its query string says context=chat), a chat link, a
//     chat id, @person, an e-mail address or a bare name is a chat;
//   - a name path with a slash, a channel link or a channel id is a channel.
//
// A message reference answers the same way, so `reply`, `edit`, `delete` and
// `react` can gate their scopes before resolving the message they act on.
func classifyChat(raw string) (bool, error) {
	parsed, err := ref.Parse(raw)
	if err != nil {
		return false, err
	}
	switch {
	case parsed.InChat:
		return true, nil
	case parsed.Kind == ref.KindMessage && parsed.MessageID != "":
		return false, nil
	case parsed.Kind == ref.KindChannel || parsed.ChannelID != "":
		return false, nil
	case parsed.Kind == ref.KindChat || parsed.Kind == ref.KindUser || parsed.IsEmail || parsed.User != "":
		return true, nil
	case len(parsed.Path) >= 2:
		return false, nil
	default:
		// Anything else - a bare word or several words - is a name, and only a
		// chat can be resolved by one: a channel name is unique only inside its
		// team, so a channel is always written Team/Channel.
		return true, nil
	}
}

// chatForWrite resolves a chat reference for a write, creating the one-on-one
// chat when the reference is a person.
//
// PLAN.md:166 makes this the write-mode half of the person form: a read scans
// the existing chats, while a write may POST /chats, which the API documents as
// returning the existing chat when there is one ("Only one one-on-one chat can
// exist between two members", refs/graph/api-reference/v1.0/api/chat-post.md:16).
// The resulting chat id is cached exactly like the scan's, so a later read finds
// it without a scan.
func (a *App) chatForWrite(ctx context.Context, resolver *ref.Resolver, raw string) (ref.Ref, error) {
	parsed, err := ref.Parse(raw)
	if err != nil {
		return ref.Ref{}, err
	}
	person := parsed.Kind == ref.KindUser || parsed.IsEmail || parsed.User != ""
	if !person {
		return resolver.Chat(ctx, raw)
	}
	// The person -> chat mapping is the same cache the read path fills, so a
	// chat this CLI created (or scanned) is reused without another create.
	key := personCacheKey(parsed)
	if cache := resolver.Cache(); cache != nil && key != "" {
		if chatID, ok := cache.PersonChat(key); ok {
			parsed.Kind, parsed.ChatID = ref.KindChat, chatID
			return parsed, nil
		}
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return ref.Ref{}, err
	}
	target, err := resolver.Person(ctx, raw)
	if err != nil {
		return ref.Ref{}, err
	}
	me, err := a.Me(ctx)
	if err != nil {
		return ref.Ref{}, err
	}
	chat, err := client.CreateChat(ctx, graph.ChatCreate{
		ChatType: graph.ChatTypeOneOnOne,
		Members: []graph.ChatMemberSpec{
			{UserID: me.ID, Roles: []string{graph.RoleOwner}},
			{UserID: target.UserID, Roles: []string{graph.RoleOwner}},
		},
	})
	if err != nil {
		return ref.Ref{}, err
	}
	if cache := resolver.Cache(); cache != nil && key != "" {
		cache.PutPersonChat(key, chat.ID)
	}
	target.Kind, target.ChatID = ref.KindChat, chat.ID
	return target, nil
}

// personCacheKey is the entity-cache key a person reference uses, which is the
// same one internal/ref fills (ref.PersonKey over the name as typed).
func personCacheKey(parsed ref.Ref) string {
	if parsed.User != "" {
		return ref.PersonKey(parsed.User)
	}
	return ref.PersonKey(parsed.Raw)
}

// resolveMessageTarget resolves a message reference and normalizes it into the
// root/reply pair every message route needs.
//
// A reply is a valid message id for the get endpoint but its thread is addressed
// by the root, so a reference that points at a reply is walked up one level -
// the same rule `teams thread read` applies (internal/cli/thread.go:85).
func (a *App) resolveMessageTarget(ctx context.Context, resolver *ref.Resolver, raw, teamHint string) (graph.MessageTarget, ref.Ref, error) {
	resolved, err := resolver.Message(ctx, raw, teamHint)
	if err != nil {
		return graph.MessageTarget{}, ref.Ref{}, err
	}
	if resolved.InChat || resolved.ChatID != "" {
		return graph.MessageTarget{ChatID: resolved.ChatID, MessageID: resolved.MessageID}, resolved, nil
	}
	client := resolver.Client()
	msg, err := client.GetChannelMessage(ctx, resolved.TeamID, resolved.ChannelID, resolved.MessageID)
	if err != nil {
		return graph.MessageTarget{}, ref.Ref{}, err
	}
	target := graph.MessageTarget{TeamID: resolved.TeamID, ChannelID: resolved.ChannelID, MessageID: msg.ID}
	if msg.ReplyToID != "" {
		target.MessageID, target.ReplyID = msg.ReplyToID, msg.ID
	}
	return target, resolved, nil
}

// writePayload is a built message: the body plus everything the POST carries.
type writePayload struct {
	Post    graph.MessagePost
	Uploads []plannedUpload
}

// plannedUpload describes an upload --dry-run did not perform.
type plannedUpload struct {
	File        string `json:"file"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	ContentType string `json:"contentType"`
	// Kind is "inline image" (hosted content in the message POST) or
	// "attachment" (a file in the drive, referenced from the body).
	Kind string `json:"kind"`
}

// buildPayload turns the flags and the text into the Graph request body.
//
// upload decides whether files are really uploaded: --dry-run sets it to false,
// and the body then carries no attachment reference, because the id and URL an
// attachment reference needs come from the upload (refs/INDEX.md:88).
func (a *App) buildPayload(ctx context.Context, flags messageFlags, mode, text string, container writeContainer, resolver *ref.Resolver, upload bool) (writePayload, error) {
	var payload writePayload

	targets, err := a.mentionTargets(ctx, resolver, flags.mentions)
	if err != nil {
		return writePayload{}, err
	}

	body, err := buildItemBody(mode, text)
	if err != nil {
		return writePayload{}, err
	}
	if len(targets) > 0 {
		content, entries, err := format.ApplyMentions(body.Content, targets)
		if err != nil {
			var unmatched *format.UnmatchedMentionError
			if errors.As(err, &unmatched) {
				return writePayload{}, output.WithHint(output.Usagef("%s", err.Error()),
					"write the mention into the text the way you want it shown, e.g. @alice or @\"Alice Example\"")
			}
			return writePayload{}, output.Errorf("%v", err)
		}
		body.Content = content
		payload.Post.Mentions = graphMentions(entries)
	}

	if len(flags.files) > 0 {
		if err := a.applyFiles(ctx, &payload, &body, flags.files, container, upload); err != nil {
			return writePayload{}, err
		}
	}

	payload.Post.Body = body
	payload.Post.Subject = strings.TrimSpace(flags.subject)
	payload.Post.Importance = strings.ToLower(strings.TrimSpace(flags.importance))
	return payload, nil
}

// buildItemBody renders the text in the requested mode.
func buildItemBody(mode, text string) (graph.ItemBody, error) {
	switch mode {
	case "text":
		return graph.ItemBody{Content: strings.TrimSpace(text), ContentType: "text"}, nil
	case "html":
		return graph.ItemBody{Content: format.SanitizeHTML(text), ContentType: "html"}, nil
	default:
		html, err := format.MarkdownToHTML(text)
		if err != nil {
			return graph.ItemBody{}, output.Errorf("%v", err)
		}
		return graph.ItemBody{Content: html, ContentType: "html"}, nil
	}
}

// mentionTargets resolves each --mention value into the person and the
// spellings to look for in the body.
func (a *App) mentionTargets(ctx context.Context, resolver *ref.Resolver, mentions []string) ([]format.MentionTarget, error) {
	if len(mentions) == 0 {
		return nil, nil
	}
	out := make([]format.MentionTarget, 0, len(mentions))
	for _, value := range mentions {
		person, err := resolver.Person(ctx, value)
		if err != nil {
			return nil, err
		}
		out = append(out, format.MentionTarget{
			UserID:      person.UserID,
			DisplayName: person.UserName,
			Handles:     mentionHandles(value, person),
		})
	}
	return out, nil
}

// mentionHandles lists the spellings a mention may have been written as, most
// specific first: what the user typed, the display name, the alias name and the
// local part of the address.
//
// The typed form comes first so that a body which spells the address out
// ("@alice@example.com") is matched whole rather than by its local part.
func mentionHandles(typed string, person ref.Ref) []string {
	candidates := []string{
		strings.Trim(strings.TrimPrefix(strings.TrimSpace(typed), "@"), `"`),
		person.Target,
		person.UserName,
	}
	for _, address := range []string{person.UserMail, person.User} {
		if local, _, ok := strings.Cut(address, "@"); ok {
			candidates = append(candidates, local)
		}
	}
	handles := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		key := strings.ToLower(candidate)
		if seen[key] {
			continue
		}
		seen[key] = true
		handles = append(handles, candidate)
	}
	return handles
}

// graphMentions renders the format package's mention entries as the Graph
// payload, with the documented fields the MCP omits
// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:213-224).
func graphMentions(entries []format.MentionEntry) []graph.Mention {
	out := make([]graph.Mention, 0, len(entries))
	for _, entry := range entries {
		out = append(out, graph.Mention{
			ID:          entry.ID,
			MentionText: entry.DisplayName,
			Mentioned: &graph.MentionedIdentitySet{
				User: &graph.TeamworkUserIdentity{
					ID:               entry.UserID,
					DisplayName:      entry.DisplayName,
					TenantID:         entry.TenantID,
					UserIdentityType: "aadUser",
				},
			},
		})
	}
	return out
}

// maxHostedContentSize is the documented cap on one inline image
// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:851). A larger image is
// sent as a file attachment instead.
const maxHostedContentSize = 4 << 20

// applyFiles attaches the --file values to a message.
//
// An image that fits the hosted-content cap is embedded inline, which is what
// Teams itself does with a pasted screenshot: the bytes ride in the message POST
// as hostedContents[] and the body references them as
// ../hostedContents/{id}/$value (refs/graph/api-reference/v1.0/api/chatmessage-post.md:731).
// Everything else is uploaded to the drive and referenced as an attachment,
// which is the MCP's flow and the reason a channel upload needs
// Files.ReadWrite.All while a chat upload only needs Files.ReadWrite
// (PLAN.md:57,221).
func (a *App) applyFiles(ctx context.Context, payload *writePayload, body *graph.ItemBody, files []string, container writeContainer, upload bool) error {
	// A plain-text body cannot carry an attachment reference, so it becomes the
	// escaped HTML paragraph the MCP builds instead
	// (refs/teams-mcp/src/utils/file-upload.ts:337-344).
	if body.ContentType == "text" && len(files) > 0 {
		body.Content = format.ParagraphHTML(format.EscapeText(body.Content))
		body.ContentType = "html"
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	var added []string
	for i, path := range files {
		//nolint:gosec // the user names the file to attach on purpose
		data, err := os.ReadFile(path)
		if err != nil {
			return output.Errorf("read %s: %v", path, err)
		}
		name := filepath.Base(path)
		contentType := format.DetectContentType(name, data)
		inline := format.IsImageContentType(contentType) && len(data) <= maxHostedContentSize
		plan := plannedUpload{
			File: path, Name: name, Size: int64(len(data)), ContentType: contentType,
			Kind: "attachment",
		}
		if inline {
			plan.Kind = "inline image"
		}
		payload.Uploads = append(payload.Uploads, plan)

		if !upload {
			continue
		}
		if inline {
			temporaryID := strconv.Itoa(i + 1)
			payload.Post.HostedContents = append(payload.Post.HostedContents,
				graph.HostedContentUploadFor(temporaryID, contentType, data))
			added = append(added, inlineImageHTML(temporaryID, name))
			continue
		}
		result, err := client.UploadFile(ctx, container.UploadTarget(), name, contentType, data)
		if err != nil {
			return err
		}
		payload.Post.Attachments = append(payload.Post.Attachments, graph.Attachment{
			ID:          result.AttachmentID,
			ContentType: "reference",
			ContentURL:  result.WebURL,
			Name:        result.Name,
		})
		added = append(added, attachmentHTML(result.AttachmentID))
	}
	body.Content = appendToBody(body.Content, added)
	return nil
}

// inlineImageHTML is the body reference for an inline image
// (refs/graph/api-reference/v1.0/api/chatmessage-post.md:731).
func inlineImageHTML(temporaryID, name string) string {
	return `<p><img src="../hostedContents/` + temporaryID + `/$value" alt="` + escapeAttribute(name) + `"></p>`
}

// attachmentHTML is the body reference for an uploaded file. The id must be the
// same one the attachments[] entry carries (refs/INDEX.md:88).
func attachmentHTML(id string) string {
	return `<p><attachment id="` + escapeAttribute(id) + `"></attachment></p>`
}

// appendToBody adds generated markup to the message body.
func appendToBody(content string, added []string) string {
	if len(added) == 0 {
		return content
	}
	out := strings.TrimSpace(content)
	for _, part := range added {
		out += part
	}
	return out
}

// escapeAttribute escapes a value for an HTML attribute. The values here are a
// file name and an id from Graph, but a name may contain quotes and the body is
// HTML the service stores as-is.
func escapeAttribute(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(value)
}

// dryRunDocument is what --dry-run prints: the requests the command would make,
// in a stable, documented shape. --jq applies to it like any other JSON output.
type dryRunDocument struct {
	DryRun bool   `json:"dryRun"`
	Method string `json:"method"`
	Path   string `json:"path"`
	// Body is the request body. It is named requestBody rather than body because
	// several Graph bodies carry a property called "body" (an event, a message),
	// and a reader of the JSON could not tell the two apart.
	Body any `json:"requestBody,omitempty"`
	// Paths lists the extra requests a command makes when one is not enough
	// (chat add-member adds one member per call).
	Paths []string `json:"paths,omitempty"`
	// Headers lists the extra request headers a raw `teams api` call sets.
	Headers []string `json:"headers,omitempty"`
	// Uploads lists the files that a real run would put in a drive first; their
	// ids and URLs are what an attachment reference needs, so the printed body
	// has none.
	Uploads []plannedUpload `json:"uploads,omitempty"`
	Notes   []string        `json:"notes,omitempty"`
}

// printDryRun renders the document through the usual JSON path, so --json and
// --jq behave as everywhere else.
func (a *App) printDryRun(doc dryRunDocument) error {
	doc.DryRun = true
	if doc.Notes == nil && len(doc.Uploads) > 0 {
		doc.Notes = []string{"files are uploaded before the message is sent, so the printed body has no attachment reference for them"}
	}
	return a.Printer.JSON(doc)
}

// requireMessageScope is the shared scope check for a message write: a channel
// message needs ChannelMessage.Send and a chat message needs ChatMessage.Send or
// Chat.ReadWrite (refs/INDEX.md, "Channels and messages").
//
// It takes the container *kind* rather than a resolved container, because the
// gate runs before any Graph call (PLAN.md:66); classifyChat decides the kind
// from the reference's shape.
func (a *App) requireMessageScope(ctx context.Context, command string, isChat bool) error {
	if isChat {
		return a.requireScopes(ctx, command, []string{"ChatMessage.Send", "Chat.ReadWrite"})
	}
	return a.requireScopes(ctx, command, []string{"ChannelMessage.Send"})
}

// requireFileScope checks the upload scope of a message that carries files: a
// channel's drive needs Files.ReadWrite.All and the caller's own OneDrive is
// covered by Files.ReadWrite (PLAN.md:57).
func (a *App) requireFileScope(ctx context.Context, command string, isChat bool) error {
	if isChat {
		return a.requireScopes(ctx, command, []string{"Files.ReadWrite", "Files.ReadWrite.All"})
	}
	return a.requireScopes(ctx, command, []string{"Files.ReadWrite.All"})
}

// Me fetches (and caches for the run) the signed-in user.
func (a *App) Me(ctx context.Context) (graph.Me, error) {
	if a.me != nil {
		return *a.me, nil
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return graph.Me{}, err
	}
	me, err := client.GetMe(ctx)
	if err != nil {
		return graph.Me{}, err
	}
	a.me = &me
	return me, nil
}

// GraphBaseURL is the service root a request goes to, for the few places that
// have to spell it out in a body: an odata bind URL and a --dry-run document.
func (a *App) GraphBaseURL() string {
	if a.Hooks.GraphBaseURL != "" {
		return a.Hooks.GraphBaseURL
	}
	eff, err := a.Effective()
	if err != nil {
		return ""
	}
	return eff.GraphBaseURL
}

// requireIncrementalScope checks a scope that no preset carries and, on a
// terminal, offers to sign in again to request it.
//
// PLAN.md:62 makes `chat delete` the incremental case: Chat.ManageDeletion.All
// needs admin consent, so it is never part of a preset. On a TTY the CLI can ask
// the user to re-consent; in non-interactive mode it fails with exit 3 and the
// hint (which names the admin request), because prompting is not allowed there.
func (a *App) requireIncrementalScope(ctx context.Context, command, scope string) error {
	// A thin wrapper, so `chat delete` behaves exactly as it always has: one
	// required scope, one requested scope, the same prompt and the same exit-3
	// error.
	return a.requireIncrementalScopes(ctx, command, []string{scope}, []string{scope})
}

// requireIncrementalScopes checks scopes that no preset carries and, on a
// terminal, offers to sign in again to request them.
//
// anyOf is the set the command can work with (the any-of rule requireScopes
// applies); request is the set a re-consent asks for, which is a superset when
// a command can use either of two scopes but needs the narrower one granted to
// be useful. The calendar commands are the reason this exists: Calendars.Read or
// Calendars.ReadWrite satisfies a read, but the re-consent asks for
// Calendars.Read and Calendars.Read.Shared, so the first `calendar list`
// afterwards also works for a colleague (plans/calendar.md §4.8).
func (a *App) requireIncrementalScopes(ctx context.Context, command string, anyOf, request []string) error {
	// An explicit any-of check first, so the answer does not depend on which
	// scope source requireScope would consult: a TEAMS_ACCESS_TOKEN is not checked
	// there at all, and an injected test grant must still be honoured.
	if granted := a.Hooks.GrantedScopes; granted != nil {
		if missing := config.Scopes.Missing(granted, anyOf); len(missing) < len(anyOf) {
			return nil
		}
		return a.requireScopesFrom(command, granted, anyOf)
	}
	authClient, authErr := a.Auth(ctx)
	if authErr != nil {
		return authErr
	}
	if authClient.Static() {
		// The token is not scope-checked, so nothing to pre-check: the request
		// itself is the check, and a 403 names the scope.
		a.Printer.Debugf("TEAMS_ACCESS_TOKEN is set; skipping the scope pre-check")
		return nil
	}
	granted, gerr := authClient.GrantedScopes(ctx)
	if gerr != nil {
		return gerr
	}
	if missing := config.Scopes.Missing(granted, anyOf); len(missing) < len(anyOf) {
		return nil
	}
	err := a.requireScopesFrom(command, granted, anyOf)
	if err == nil {
		return nil
	}
	if a.Hooks.GrantedScopes != nil {
		// Tests inject the granted set; there is no session to re-consent.
		return err
	}
	if !a.Printer.Interactive() {
		return err
	}
	if confirmErr := a.confirm(a.incrementalScopePrompt(command, request), false); confirmErr != nil {
		return confirmErr
	}
	eff, effErr := a.Effective()
	if effErr != nil {
		return effErr
	}
	scopes := append(append([]string(nil), eff.Scopes...), request...)
	a.Printer.Statusf("requesting %s", strings.Join(scopes, " "))
	_, loginErr := authClient.Login(ctx, auth.LoginOptions{
		Scopes:      scopes,
		Interactive: true,
		OnDeviceCode: func(dc auth.DeviceCode) {
			if dc.Message != "" {
				a.Printer.Statusf("%s", dc.Message)
				return
			}
			a.Printer.Statusf("open %s and enter the code %s", dc.VerificationURL, dc.UserCode)
		},
		OnBrowser: func(url string) {
			a.Printer.Statusf("opening your browser; if it does not open, visit:")
			a.Printer.Statusf("  %s", url)
		},
		OnFallback: func(cause error) {
			a.Printer.Warnf("the browser sign-in failed (%v)", cause)
		},
	})
	if loginErr != nil {
		return auth.Classify(loginErr, eff.Name)
	}
	// Re-check: an admin may still not have consented, and then the honest
	// failure is the exit-3 scope error with its admin hint.
	return a.requireScope(ctx, command, anyOf)
}

// incrementalScopePrompt words the re-consent question. "(admin consent)" is
// only claimed for a scope the Graph permissions reference actually marks as
// needing one; otherwise the honest wording is that the tenant may still
// require an admin to approve it, which is what the handoff saw on a production
// tenant (plans/calendar.md §3, F1).
func (a *App) incrementalScopePrompt(command string, request []string) string {
	needsAdmin := ""
	for _, scope := range request {
		if config.Scopes.RequiresAdminConsent(scope) {
			needsAdmin = " (admin consent)"
			break
		}
	}
	if needsAdmin == "" {
		needsAdmin = " (your tenant may still require an admin to approve it)"
	}
	return command + " needs " + strings.Join(request, ", ") + needsAdmin + ". Sign in again to request it?"
}

// messageTargetPath is the path a dry-run document shows for a message action.
func messageTargetPath(target graph.MessageTarget) string {
	switch {
	case target.IsChat() && target.MessageID == "":
		return "/chats/" + target.ChatID + "/messages"
	case target.IsChat():
		return "/chats/" + target.ChatID + "/messages/" + target.MessageID
	case target.MessageID == "":
		return "/teams/" + target.TeamID + "/channels/" + target.ChannelID + "/messages"
	case target.ReplyID != "":
		return "/teams/" + target.TeamID + "/channels/" + target.ChannelID + "/messages/" +
			target.MessageID + "/replies/" + target.ReplyID
	default:
		return "/teams/" + target.TeamID + "/channels/" + target.ChannelID + "/messages/" + target.MessageID
	}
}

// containsFold reports whether values contains want, case-insensitively.
func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
