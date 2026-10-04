package ref

import (
	"context"
	"strings"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/store"
)

// Chooser picks one of several candidates on a terminal. The CLI implements it;
// when it is nil, or says the session is not interactive, the resolver fails
// with the candidate list instead of guessing - which is what PLAN.md requires
// of non-interactive mode.
type Chooser interface {
	Interactive() bool
	Choose(question string, options []string) (int, error)
}

// ResolverOptions configures a Resolver.
type ResolverOptions struct {
	// Client is the Graph client every lookup uses.
	Client *graph.Client
	// Cache is the per-profile entity cache (may be nil in tests).
	Cache *store.EntityCache
	// Aliases is the per-profile alias table, name to target.
	Aliases map[string]string
	// Refresh bypasses the cache, for --refresh.
	Refresh bool
	// Chooser is the interactive picker; nil never prompts.
	Chooser Chooser
	// Now supplies the clock for cache metadata.
	Now func() time.Time
	// Debugf logs cache hits and lookups under --verbose.
	Debugf func(format string, args ...any)
}

// Resolver turns references into Graph ids. It is the only part of this package
// that talks to Graph or to local state.
type Resolver struct {
	opts ResolverOptions

	membersCache  []graph.User
	membersLoaded bool
}

// NewResolver builds a Resolver.
func NewResolver(opts ResolverOptions) *Resolver {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Debugf == nil {
		opts.Debugf = func(string, ...any) {}
	}
	return &Resolver{opts: opts}
}

// Client exposes the Graph client the resolver was built with, so a command can
// keep using the same one.
func (r *Resolver) Client() *graph.Client { return r.opts.Client }

// Cache exposes the entity cache, so the CLI can persist it after a run.
func (r *Resolver) Cache() *store.EntityCache { return r.opts.Cache }

func (r *Resolver) debugf(format string, args ...any) { r.opts.Debugf(format, args...) }

// expand applies the alias table, then parses. An alias may be written with or
// without its leading @, so `teams alias set boss alice@x` and `teams post
// @boss ...` meet in the middle.
func (r *Resolver) expand(raw string) (Ref, error) {
	key := strings.ToLower(strings.TrimSpace(raw))
	for _, candidate := range []string{key, strings.TrimPrefix(key, "@")} {
		target, ok := r.opts.Aliases[candidate]
		if !ok || strings.TrimSpace(target) == "" {
			continue
		}
		r.debugf("alias %s resolves to %s", candidate, target)
		parsed, err := Parse(target)
		if err != nil {
			return Ref{}, err
		}
		parsed.Raw, parsed.Target = raw, target
		return parsed, nil
	}
	return Parse(raw)
}

// Team resolves a team reference to a team id. A URL, a name path's first
// element, a bare name, an alias and a raw id are all accepted (PLAN.md:158).
func (r *Resolver) Team(ctx context.Context, raw string) (Ref, error) {
	parsed, err := r.expand(raw)
	if err != nil {
		return Ref{}, err
	}
	switch {
	case parsed.Kind == KindTeam && parsed.TeamID != "":
		return parsed, nil
	case parsed.TeamName != "":
		return r.teamByName(ctx, parsed, parsed.TeamName)
	case parsed.Kind == KindUnknown && isGUID(parsed.Raw):
		parsed.Kind = KindTeam
		parsed.TeamID = parsed.Raw
		return parsed, nil
	case parsed.Kind == KindUnknown && parsed.Raw != "":
		return r.teamByName(ctx, parsed, firstNonEmpty(parsed.Target, parsed.Raw))
	case parsed.TeamID != "":
		parsed.Kind = KindTeam
		return parsed, nil
	case len(parsed.Path) == 1:
		return r.teamByName(ctx, parsed, parsed.Path[0])
	default:
		return Ref{}, usagef("%q is not a team; use a team name, a Teams link or an id", raw)
	}
}

// teamByName resolves a team by display name: cache, then the joined-teams
// listing, matching case-insensitively and falling back to a substring match
// when nothing matches exactly.
func (r *Resolver) teamByName(ctx context.Context, parsed Ref, name string) (Ref, error) {
	if !r.opts.Refresh && r.opts.Cache != nil {
		if id, ok := r.opts.Cache.Team(name); ok {
			r.debugf("cache: team %s is %s", name, id)
			parsed.Kind, parsed.TeamID, parsed.TeamName = KindTeam, id, name
			return parsed, nil
		}
	}
	teams, err := r.opts.Client.ListJoinedTeams(ctx)
	if err != nil {
		return Ref{}, err
	}
	candidates := make([]candidate, 0, len(teams))
	for _, team := range teams {
		candidates = append(candidates, candidate{id: team.ID, name: team.DisplayName})
	}
	picked, err := r.pick("team", name, candidates)
	if err != nil {
		return Ref{}, err
	}
	if r.opts.Cache != nil {
		r.opts.Cache.PutTeam(name, picked.id)
		if picked.name != "" {
			r.opts.Cache.PutTeam(picked.name, picked.id)
		}
	}
	parsed.Kind, parsed.TeamID, parsed.TeamName = KindTeam, picked.id, picked.name
	return parsed, nil
}

// Channel resolves a channel reference. teamHint is the --team flag, which a
// channel-message link without groupId needs: every channel route takes the
// team id, and there is no channel-to-team lookup (PLAN.md:164).
func (r *Resolver) Channel(ctx context.Context, raw, teamHint string) (Ref, error) {
	parsed, err := r.expand(raw)
	if err != nil {
		return Ref{}, err
	}
	switch {
	case parsed.ChannelID != "":
		teamID := parsed.TeamID
		if teamID == "" {
			resolved, err := r.teamIDFromHint(ctx, teamHint)
			if err != nil {
				return Ref{}, err
			}
			teamID = resolved
		}
		if teamID == "" {
			return Ref{}, withFix(notFoundf("the link to channel %s does not carry a team id", parsed.ChannelID),
				"pass --team <team>: a channel id alone does not say which team it belongs to")
		}
		return r.channelByID(ctx, parsed, teamID, parsed.ChannelID)
	case parsed.Kind == KindMessage && parsed.ChannelName == "" && parsed.MessageID != "":
		return Ref{}, usagef("a bare message id does not say which channel it is in")
	case parsed.ChannelName != "":
		team, err := r.teamByName(ctx, parsed, firstNonEmpty(parsed.TeamName, teamHint))
		if err != nil {
			return Ref{}, err
		}
		return r.channelByName(ctx, parsed, team.TeamID, parsed.ChannelName)
	case len(parsed.Path) == 2:
		team, err := r.teamByName(ctx, parsed, parsed.Path[0])
		if err != nil {
			return Ref{}, err
		}
		return r.channelByName(ctx, parsed, team.TeamID, parsed.Path[1])
	case parsed.Kind == KindUnknown && parsed.Raw != "":
		if strings.Contains(parsed.Raw, "/") {
			return Ref{}, usagef("%q is not a channel reference", raw)
		}
		teamID, err := r.teamIDFromHint(ctx, teamHint)
		if err != nil {
			return Ref{}, err
		}
		if teamID == "" {
			return Ref{}, withFix(usagef("%q does not say which team the channel is in", raw),
				"use Team/Channel, a Teams link, or pass --team")
		}
		return r.channelByName(ctx, parsed, teamID, firstNonEmpty(parsed.Target, parsed.Raw))
	default:
		return Ref{}, usagef("%q is not a channel; use Team/Channel or a Teams link", raw)
	}
}

// channelByID fills in a channel's display name and verifies the team/channel
// pair in one call.
func (r *Resolver) channelByID(ctx context.Context, parsed Ref, teamID, channelID string) (Ref, error) {
	channel, err := r.opts.Client.GetChannel(ctx, teamID, channelID)
	if err != nil {
		return Ref{}, err
	}
	parsed.Kind, parsed.TeamID, parsed.ChannelID = KindChannel, teamID, channel.ID
	if channel.DisplayName != "" {
		parsed.ChannelName = channel.DisplayName
	}
	if r.opts.Cache != nil && channel.DisplayName != "" {
		r.opts.Cache.PutChannel(teamID, channel.DisplayName, channel.ID)
	}
	return parsed, nil
}

// channelByName resolves a channel inside one team: cache, then the channel
// listing, matching case-insensitively and falling back to a substring match.
func (r *Resolver) channelByName(ctx context.Context, parsed Ref, teamID, name string) (Ref, error) {
	if !r.opts.Refresh && r.opts.Cache != nil {
		if id, ok := r.opts.Cache.Channel(teamID, name); ok {
			r.debugf("cache: channel %s/%s is %s", teamID, name, id)
			parsed.Kind, parsed.TeamID, parsed.ChannelID, parsed.ChannelName = KindChannel, teamID, id, name
			return parsed, nil
		}
	}
	channels, err := r.opts.Client.ListChannels(ctx, teamID)
	if err != nil {
		return Ref{}, err
	}
	candidates := make([]candidate, 0, len(channels))
	for _, channel := range channels {
		candidates = append(candidates, candidate{id: channel.ID, name: channel.DisplayName})
	}
	picked, err := r.pick("channel", name, candidates)
	if err != nil {
		return Ref{}, err
	}
	if r.opts.Cache != nil {
		r.opts.Cache.PutChannel(teamID, name, picked.id)
		if picked.name != "" {
			r.opts.Cache.PutChannel(teamID, picked.name, picked.id)
		}
	}
	parsed.Kind, parsed.TeamID, parsed.ChannelID, parsed.ChannelName = KindChannel, teamID, picked.id, picked.name
	return parsed, nil
}

// Chat resolves a chat reference: a chat id, a Teams chat link, a person (which
// means the 1:1 chat with them) or a chat topic.
func (r *Resolver) Chat(ctx context.Context, raw string) (Ref, error) {
	parsed, err := r.expand(raw)
	if err != nil {
		return Ref{}, err
	}
	switch {
	case parsed.ChatID != "":
		parsed.Kind = KindChat
		return parsed, nil
	case parsed.IsEmail || parsed.Kind == KindUser:
		return r.PersonChat(ctx, raw)
	case parsed.Kind == KindChat && parsed.ChatID == "" && parsed.User != "":
		// The compose form /l/chat/0/0?users=a@x names participants rather than a
		// chat, so it resolves to the 1:1 chat with that person
		// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md,
		// "Configure deep link to start a chat manually").
		return r.PersonChat(ctx, parsed.User)
	case parsed.Kind == KindUnknown && parsed.Raw != "":
		if isGUID(parsed.Raw) {
			return Ref{}, usagef("%q is not a chat id; a chat id starts with 19:", raw)
		}
		return r.chatByTopic(ctx, parsed, firstNonEmpty(parsed.Target, parsed.Raw))
	case len(parsed.Path) == 1:
		return r.chatByTopic(ctx, parsed, firstNonEmpty(parsed.Target, parsed.Path[0]))
	default:
		return Ref{}, usagef("%q is not a chat; use a Teams chat link, a topic name or @person", raw)
	}
}

// chatByTopic finds a chat by its topic, the only human-readable chat name the
// API offers.
func (r *Resolver) chatByTopic(ctx context.Context, parsed Ref, topic string) (Ref, error) {
	chats, err := r.opts.Client.ListChats(ctx, graph.ChatQuery{Top: graph.MaxTopChats})
	if err != nil {
		return Ref{}, err
	}
	candidates := make([]candidate, 0, len(chats))
	for _, chat := range chats {
		if chat.Topic == nil || *chat.Topic == "" {
			continue
		}
		candidates = append(candidates, candidate{id: chat.ID, name: *chat.Topic})
	}
	picked, err := r.pick("chat", topic, candidates)
	if err != nil {
		return Ref{}, err
	}
	parsed.Kind, parsed.ChatID = KindChat, picked.id
	if parsed.ChatTopic == "" {
		parsed.ChatTopic = picked.name
	}
	return parsed, nil
}

// Message resolves a message reference. A channel message needs its team and
// channel; a chat message needs its chat. A bare message id is rejected, because
// no Graph route can find the container from the id alone.
func (r *Resolver) Message(ctx context.Context, raw, teamHint string) (Ref, error) {
	parsed, err := r.expand(raw)
	if err != nil {
		return Ref{}, err
	}
	switch {
	case parsed.MessageID != "" && parsed.InChat:
		parsed.Kind = KindMessage
		return parsed, nil
	case parsed.MessageID != "" && parsed.ChannelID != "":
		teamID := firstNonEmpty(parsed.TeamID, teamHint)
		if teamID == "" {
			return Ref{}, withFix(notFoundf("the message link does not carry a team id"),
				"pass --team <team>: every channel message route needs the team id")
		}
		channel, err := r.channelByID(ctx, parsed, teamID, parsed.ChannelID)
		if err != nil {
			return Ref{}, err
		}
		channel.Kind, channel.MessageID = KindMessage, parsed.MessageID
		return channel, nil
	case len(parsed.Path) == 3:
		channel, err := r.Channel(ctx, parsed.Path[0]+"/"+parsed.Path[1], teamHint)
		if err != nil {
			return Ref{}, err
		}
		channel.Kind, channel.MessageID = KindMessage, parsed.Path[2]
		return channel, nil
	case parsed.Raw != "" && !parsed.IsEmail:
		return Ref{}, withFix(usagef("%q does not identify a message", raw),
			"use a Teams message link, Team/Channel/MessageId, or a chat reference for a chat message")
	default:
		return Ref{}, usagef("%q does not identify a message", raw)
	}
}

// teamIDFromHint turns a --team value into a team id. A GUID is used as-is; a
// name, a name path or a link goes through the same resolution as any other team
// reference, so `--team Engineering` works as well as `--team <guid>`.
func (r *Resolver) teamIDFromHint(ctx context.Context, hint string) (string, error) {
	if strings.TrimSpace(hint) == "" {
		return "", nil
	}
	if isGUID(hint) {
		return hint, nil
	}
	team, err := r.Team(ctx, hint)
	if err != nil {
		return "", err
	}
	return team.TeamID, nil
}

// candidate is one pickable name/id pair.
type candidate struct {
	id   string
	name string
}

// pick resolves a name against a candidate list: an exact case-insensitive
// match wins; otherwise a unique substring match does; several matches ask the
// user to choose on a terminal and are a usage error everywhere else.
func (r *Resolver) pick(what, query string, candidates []candidate) (candidate, error) {
	if len(candidates) == 0 {
		return candidate{}, withFix(notFoundf("no %s is available to match %q", what, query),
			"run `teams %s list` to see what exists", what)
	}
	exact := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		if strings.EqualFold(c.name, query) {
			exact = append(exact, c)
		}
	}
	switch len(exact) {
	case 1:
		return exact[0], nil
	case 0:
	default:
		return r.choose(what, query, exact)
	}
	fuzzy := make([]candidate, 0, len(candidates))
	for _, c := range candidates {
		if containsFold(c.name, query) {
			fuzzy = append(fuzzy, c)
		}
	}
	if n := countExactIDs(fuzzy); n == 1 {
		return fuzzy[0], nil
	}
	if len(fuzzy) == 0 {
		return candidate{}, withFix(notFoundf("no %s matches %q", what, query),
			"run `teams %s list` to see the available names", what)
	}
	return r.choose(what, query, fuzzy)
}

// choose asks the user to pick one of several candidates, or fails with the
// list when the session cannot prompt.
func (r *Resolver) choose(what, query string, candidates []candidate) (candidate, error) {
	labels := make([]string, 0, len(candidates))
	for _, c := range candidates {
		label := c.name
		if label == "" {
			label = c.id
		}
		labels = append(labels, label)
	}
	if r.opts.Chooser != nil && r.opts.Chooser.Interactive() {
		index, err := r.opts.Chooser.Choose(formatChoiceQuestion(what, query, labels), labels)
		if err != nil {
			return candidate{}, err
		}
		if index < 0 || index >= len(candidates) {
			return candidate{}, errorf("no %s was selected", what)
		}
		return candidates[index], nil
	}
	return candidate{}, withFix(usagef("%d %ss match %q: %s", len(candidates), what, query, strings.Join(labels, ", ")),
		"pass the exact name or an id")
}

// formatChoiceQuestion renders the prompt the CLI shows.
func formatChoiceQuestion(what, query string, options []string) string {
	var b strings.Builder
	if len(options) == 1 {
		b.WriteString("Use " + options[0] + "?")
		return b.String()
	}
	b.WriteString("Which " + what + " did you mean by " + query + "?")
	return b.String()
}

// countExactIDs counts distinct ids in a candidate list, so a duplicate display
// name that points at the same object is not treated as ambiguous.
func countExactIDs(candidates []candidate) int {
	seen := map[string]bool{}
	for _, c := range candidates {
		seen[c.id] = true
	}
	return len(seen)
}

// containsFold is a case-insensitive substring test.
func containsFold(haystack, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return false
	}
	return strings.Contains(strings.ToLower(haystack), needle)
}

// firstNonEmpty returns the first non-empty, trimmed value.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// isGUID reports whether s is a bare GUID, which Graph ids for teams, users and
// drives are.
func isGUID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return false
	}
	want := []int{8, 4, 4, 4, 12}
	for i, part := range parts {
		if len(part) != want[i] {
			return false
		}
		for _, r := range part {
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}
