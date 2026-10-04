package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/floriscornel/teams-cli/internal/config"
	"github.com/floriscornel/teams-cli/internal/format"
	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
	"github.com/floriscornel/teams-cli/internal/ref"
	"github.com/floriscornel/teams-cli/internal/store"
)

// This file holds what every Phase 3 read command shares: the resolver (with the
// entity cache and the aliases behind it), the --limit/--since/--all flags and
// the message renderer.

// storeKindLabel shortens a token_store setting for a debug line: the value
// without the part that identifies a vault or a path.
func storeKindLabel(spec string) string {
	switch {
	case strings.HasPrefix(spec, config.TokenStoreKeyVault):
		return "keyvault"
	case strings.HasPrefix(spec, config.TokenStoreFileURL):
		return "file://<path>"
	default:
		return spec
	}
}

// resolver builds the reference resolver for this run, loading the per-profile
// entity cache and aliases. The cache is kept on the App so a successful command
// can persist what it learned (PLAN.md:170).
func (a *App) resolver(ctx context.Context) (*ref.Resolver, error) {
	if a.resolverc != nil {
		return a.resolverc, nil
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return nil, err
	}
	paths, err := a.Paths()
	if err != nil {
		return nil, err
	}
	cache, err := store.LoadEntities(paths.EntityCacheFile())
	if err != nil {
		return nil, output.Errorf("%v", err)
	}
	if problem := cache.Problem(); problem != nil {
		a.Printer.Debugf("entity cache: %v (starting from an empty cache)", problem)
	}
	cache.SetClock(a.Clock.Now)
	// TTLs are enforced by pruning with our own clock, not by the lookups: a name
	// is stale after an hour and a person after a week (PLAN.md:170).
	cache.Prune(a.Clock.Now())
	aliases, err := store.LoadAliases(paths.AliasesFile())
	if err != nil {
		return nil, output.Errorf("%v", err)
	}
	a.entityCache = cache
	a.aliases = aliases
	a.resolverc = ref.NewResolver(ref.ResolverOptions{
		Client:  client,
		Cache:   cache,
		Aliases: aliases,
		Refresh: a.refreshFlag,
		Chooser: a,
		Now:     a.Clock.Now,
		Debugf:  a.Printer.Debugf,
	})
	return a.resolverc, nil
}

// saveEntityCache persists what a command learned about names and people. It is
// called after a successful command and never fails the command: a cache that
// cannot be written is rebuildable data, not an error the user needs to act on.
func (a *App) saveEntityCache() {
	if a.entityCache == nil || !a.entityCache.Dirty() {
		return
	}
	paths, err := a.Paths()
	if err != nil {
		a.Printer.Debugf("entity cache: %v", err)
		return
	}
	// The lock is advisory and cross-process: two concurrent commands could
	// otherwise lose each other's entries (internal/store documents the rule).
	path := paths.EntityCacheFile()
	err = store.WithLock(context.Background(), store.LockPath(path), store.LockTimeout, func() error {
		return a.entityCache.Save(path)
	})
	if err != nil {
		a.Printer.Debugf("entity cache: %v", err)
	}
}

// Interactive implements ref.Chooser: a name that matches several objects may be
// disambiguated on a terminal, and only there.
func (a *App) Interactive() bool { return a.Printer.Interactive() }

// Choose implements ref.Chooser. The options are printed to stderr so stdout
// stays parseable, and the answer is a 1-based number.
func (a *App) Choose(question string, options []string) (int, error) {
	_, _ = fmt.Fprintf(a.Stderr, "%s\n", question)
	for i, option := range options {
		_, _ = fmt.Fprintf(a.Stderr, "  %d) %s\n", i+1, option)
	}
	_, _ = fmt.Fprintf(a.Stderr, "select 1-%d: ", len(options))
	var answer int
	if _, err := fmt.Fscanln(a.Stdin, &answer); err != nil {
		return 0, output.Usagef("no selection read")
	}
	if answer < 1 || answer > len(options) {
		return 0, output.Usagef("selection %d is out of range 1-%d", answer, len(options))
	}
	return answer - 1, nil
}

// listFlags are the shared paging flags of the read commands.
type listFlags struct {
	limit int
	all   bool
}

// limitOf turns --limit/--all into the resolver's limit, where 0 means every
// page.
func (f listFlags) limitOf(def int) int {
	if f.all {
		return 0
	}
	if f.limit > 0 {
		return f.limit
	}
	return def
}

// windowFlags are the shared --since/--until flags.
type windowFlags struct {
	since string
	until string
}

// window parses --since and --until against the injected clock.
func (f windowFlags) window(now time.Time) (time.Time, time.Time, error) {
	since, err := parseTimeFlag("since", f.since, now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	until, err := parseTimeFlag("until", f.until, now)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return time.Time{}, time.Time{}, output.Usagef("--until is before --since")
	}
	return since, until, nil
}

// parseTimeFlag accepts an ISO date, a full timestamp, or a duration meaning
// "that long ago": 30m, 24h, 7d and 2w all work, which is how PLAN.md writes
// its examples (--since 24h, --since 7d).
func parseTimeFlag(flag, value string, now time.Time) (time.Time, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), nil
	}
	if d, err := parseWindowDuration(v); err == nil {
		return now.Add(-d).UTC(), nil
	}
	return time.Time{}, output.Usagef("--%s %q: want a timestamp (2026-10-04 or 2026-10-04T10:00:00Z) or a duration (24h, 7d)", flag, value)
}

// parseWindowDuration extends time.ParseDuration with the day and week units
// people actually type for a message window.
func parseWindowDuration(v string) (time.Duration, error) {
	lower := strings.ToLower(strings.TrimSpace(v))
	multiplier := time.Duration(0)
	switch {
	case strings.HasSuffix(lower, "d"):
		multiplier, lower = 24*time.Hour, strings.TrimSuffix(lower, "d")
	case strings.HasSuffix(lower, "w"):
		multiplier, lower = 7*24*time.Hour, strings.TrimSuffix(lower, "w")
	default:
		return time.ParseDuration(lower)
	}
	if lower == "" {
		return 0, fmt.Errorf("missing number before the unit")
	}
	base, err := time.ParseDuration(lower + "h")
	if err != nil {
		return 0, err
	}
	return time.Duration(float64(base) / float64(time.Hour) * float64(multiplier)), nil
}

// messageOptions controls how a message is rendered.
type messageOptions struct {
	// container is the channel or chat name shown next to the author when a
	// listing mixes containers (search, mentions, unread).
	container string
	// replies renders each reply under its root.
	replies bool
	// indent prefixes every line.
	indent string
}

// printMessages renders messages newest-first as they arrive, and in
// chronological order on screen: reading a conversation top-down is the point,
// and --limit then means "the newest N". --json keeps the fetched order, so
// .[0] is the newest message.
func (a *App) printMessages(msgs []graph.Message, opts messageOptions) error {
	if a.Printer.JSONMode() {
		return a.Printer.JSON(msgs)
	}
	chronological := make([]graph.Message, 0, len(msgs))
	for i := len(msgs) - 1; i >= 0; i-- {
		chronological = append(chronological, msgs[i])
	}
	for i, msg := range chronological {
		if i > 0 {
			a.Printer.Println()
		}
		a.printMessage(msg, opts)
	}
	return nil
}

// printMessage renders one message: a header line with the author and the age,
// the body as markdown, then attachments, reactions and the link.
func (a *App) printMessage(msg graph.Message, opts messageOptions) {
	indent := opts.indent
	a.Printer.Printf("%s%s", indent, a.Printer.Bold(messageAuthor(msg)))
	if age := output.HumanAge(a.Clock.Now(), msg.CreatedDateTime); age != "" {
		a.Printer.Printf(" · %s", age)
	}
	if opts.container != "" {
		a.Printer.Printf(" · %s", opts.container)
	}
	if msg.LastEditedDateTime != nil && !msg.LastEditedDateTime.IsZero() {
		a.Printer.Printf(" · edited")
	}
	if msg.IsDeleted() {
		a.Printer.Printf(" · deleted")
	}
	if msg.Importance != "" && msg.Importance != "normal" {
		a.Printer.Printf(" · %s importance", msg.Importance)
	}
	a.Printer.Println()
	if msg.Subject != "" {
		a.Printer.Printf("%s  Subject: %s\n", indent, msg.Subject)
	}
	body, err := format.BodyToMarkdown(msg.Body.Content, msg.Body.ContentType, mentionNames(msg.Mentions))
	if err != nil {
		a.Printer.Debugf("message %s: %v", msg.ID, err)
		body = strings.TrimSpace(msg.Body.Content)
	}
	a.Printer.Markdown(body, indent+"  ")
	if line := attachmentLine(msg.Attachments); line != "" {
		a.Printer.Printf("%s  %s\n", indent, line)
	}
	if line := reactionLine(msg.Reactions); line != "" {
		a.Printer.Printf("%s  %s\n", indent, line)
	}
	if msg.WebURL != "" {
		a.Printer.Printf("%s  %s\n", indent, a.Printer.Dim(msg.WebURL))
	}
	if opts.replies && len(msg.Replies) > 0 {
		replies := append([]graph.Message(nil), msg.Replies...)
		sortMessagesChronological(replies)
		for _, reply := range replies {
			a.Printer.Println()
			a.printMessage(reply, messageOptions{indent: opts.indent + "    "})
		}
	}
}

// messageAuthor is the name to show for a message: the sender's display name,
// falling back to the id, and then to a system-event marker.
func messageAuthor(msg graph.Message) string {
	sender := msg.From.Sender()
	switch {
	case sender.DisplayName != "":
		return sender.DisplayName
	case sender.ID != "":
		return sender.ID
	case msg.IsSystemEvent():
		return "Teams"
	default:
		return "unknown sender"
	}
}

// attachmentLine summarises a message's attachments.
func attachmentLine(attachments []graph.Attachment) string {
	if len(attachments) == 0 {
		return ""
	}
	names := make([]string, 0, len(attachments))
	for _, att := range attachments {
		name := att.Name
		if name == "" {
			name = att.ID
		}
		if name == "" {
			name = "unnamed"
		}
		names = append(names, name)
	}
	return "attachment: " + strings.Join(names, ", ")
}

// reactionLine summarises a message's reactions as emoji counts, which is the
// reaction summary PLAN.md asks for next to the author and the relative time.
func reactionLine(reactions []graph.Reaction) string {
	if len(reactions) == 0 {
		return ""
	}
	counts := map[string]int{}
	order := make([]string, 0, len(reactions))
	for _, r := range reactions {
		if _, seen := counts[r.ReactionType]; !seen {
			order = append(order, r.ReactionType)
		}
		counts[r.ReactionType]++
	}
	parts := make([]string, 0, len(order))
	for _, kind := range order {
		if counts[kind] > 1 {
			parts = append(parts, fmt.Sprintf("%s %d", kind, counts[kind]))
			continue
		}
		parts = append(parts, kind)
	}
	return "reactions: " + strings.Join(parts, " · ")
}

// mentionNames indexes a message's mentions by the id the body's <at> tag uses.
func mentionNames(mentions []graph.Mention) map[int]string {
	if len(mentions) == 0 {
		return nil
	}
	out := make(map[int]string, len(mentions))
	for _, m := range mentions {
		out[m.ID] = m.DisplayName()
	}
	return out
}

// sortMessagesChronological orders replies oldest-first for display.
func sortMessagesChronological(msgs []graph.Message) {
	for i := 1; i < len(msgs); i++ {
		for j := i; j > 0; j-- {
			if msgs[j].CreatedDateTime.Before(msgs[j-1].CreatedDateTime) {
				msgs[j], msgs[j-1] = msgs[j-1], msgs[j]
				continue
			}
			break
		}
	}
}

// requireScopes checks the token's scopes against the feature matrix before any
// Graph call, naming the missing scope and the preset that has it (PLAN.md:66).
func (a *App) requireScopes(ctx context.Context, command string, scopes []string) error {
	return a.requireScope(ctx, command, scopes)
}

// formatTime renders a Graph timestamp for a definitions block, or an empty
// string when Graph sent none (a nullable timestamp arrives as a zero time).
func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// firstNonEmptyString returns the first non-empty, trimmed value.
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
