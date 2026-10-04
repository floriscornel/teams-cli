package ref

import (
	"context"
	"errors"
	"strings"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/store"
)

// This file resolves people. PLAN.md:167 fixes the order: alias (done by the
// caller), the entity cache, the members of the user's chats and teams, then
// GET /me/people and finally GET /users with a startswith filter. Membership
// alone needs no extra scope, which is why it is tried before the directory
// calls that need People.Read or User.ReadBasic.All.

// membershipPageLimit bounds the chats scanned for a person. Zero means every
// page, which is the documented way to list them: the listing pages with $top at
// the documented 50 (refs/graph/api-reference/v1.0/api/chat-list.md:33).
const membershipPageLimit = 0

// Person returns the user a person reference points at.
func (r *Resolver) Person(ctx context.Context, raw string) (Ref, error) {
	parsed, err := r.expand(raw)
	if err != nil {
		return Ref{}, err
	}
	if parsed.User == "" && parsed.Raw != "" {
		// A bare word (a display name, a mail local part or a raw id) is a person
		// reference for this command, which is what PLAN.md:169 means by "a raw
		// ID" being accepted everywhere.
		parsed.User = parsed.Raw
	}
	user, err := r.resolvePerson(ctx, parsed)
	if err != nil {
		return Ref{}, err
	}
	parsed.Kind = KindUser
	parsed.UserID = user.ID
	parsed.UserName = user.DisplayName
	parsed.UserMail = user.Address()
	return parsed, nil
}

// PersonID is a convenience for callers that only need the id.
func (r *Resolver) PersonID(ctx context.Context, raw string) (string, error) {
	resolved, err := r.Person(ctx, raw)
	if err != nil {
		return "", err
	}
	return resolved.UserID, nil
}

// resolvePerson implements the documented resolution order.
func (r *Resolver) resolvePerson(ctx context.Context, parsed Ref) (graph.User, error) {
	if parsed.User == "" {
		return graph.User{}, usagef("%q is not a person reference", parsed.Raw)
	}
	key := PersonKey(parsed.User)
	if cached, ok := r.cachedPerson(key); ok {
		return cached, nil
	}

	// An address or a user id is exact: Graph resolves it in one call.
	if parsed.IsEmail || isGUID(parsed.User) {
		user, err := r.opts.Client.GetUser(ctx, parsed.User)
		if err == nil {
			r.rememberPerson(key, user)
			return user, nil
		}
		if !parsed.IsEmail {
			return graph.User{}, err
		}
		// An e-mail may still be a personal address that the directory does not
		// index under that form, so fall through to the name search with the
		// local part.
	}

	query := parsed.User
	if parsed.IsEmail {
		if local, _, ok := strings.Cut(parsed.User, "@"); ok {
			query = local
		}
	}

	if user, ok, err := r.memberByName(ctx, query); err != nil {
		return graph.User{}, err
	} else if ok {
		r.rememberPerson(key, user)
		return user, nil
	}

	// GET /me/people needs People.Read and is relevance-ordered, so it is the
	// best directory call when it is granted; a missing scope shows up as a
	// 403, which we treat as "this source is unavailable" rather than a
	// failure, because the next source may still resolve the person.
	if people, err := r.opts.Client.ListPeople(ctx, query, defaultPeopleLimit); err == nil {
		if user, ok := pickPerson(people, query); ok {
			r.rememberPerson(key, user)
			return user, nil
		}
	} else if !isPermissionError(err) && !errors.Is(err, context.Canceled) {
		r.debugf("people search for %q failed: %v", query, err)
	}

	// GET /users with a startswith filter needs User.ReadBasic.All.
	if users, err := r.opts.Client.SearchUsers(ctx, query, defaultUserLimit); err == nil {
		if user, ok := pickUser(users, query); ok {
			r.rememberPerson(key, user)
			return user, nil
		}
	} else if !isPermissionError(err) {
		r.debugf("directory search for %q failed: %v", query, err)
	}

	// A raw id is the last resort, because GET /users/{id} is exact: it runs
	// after every name source, so resolving a display name never pays for a
	// wasted request.
	if !strings.Contains(parsed.User, " ") {
		if user, err := r.opts.Client.GetUser(ctx, parsed.User); err == nil {
			r.rememberPerson(key, user)
			return user, nil
		}
	}

	return graph.User{}, withFix(notFoundf("no person matches %q", parsed.Raw),
		"use an e-mail address, or run `teams user search %s` to see the candidates", query)
}

// cachedPerson reads the entity cache. A cached person is trusted for its TTL,
// which is what makes repeated @name references cheap.
func (r *Resolver) cachedPerson(key string) (graph.User, bool) {
	if r.opts.Refresh || r.opts.Cache == nil {
		return graph.User{}, false
	}
	p, ok := r.opts.Cache.Person(key)
	if !ok || p.UserID == "" {
		return graph.User{}, false
	}
	r.debugf("cache: %s is %s", key, p.UserID)
	return graph.User{ID: p.UserID, DisplayName: p.DisplayName, Mail: p.Mail}, true
}

// rememberPerson caches a resolution and every spelling of the name, so the
// next lookup - including one for a different spelling - is a cache hit.
func (r *Resolver) rememberPerson(key string, user graph.User) {
	if r.opts.Cache == nil || user.ID == "" {
		return
	}
	entry := store.Person{UserID: user.ID, DisplayName: user.DisplayName, Mail: user.Address()}
	r.opts.Cache.PutPerson(key, entry)
	if user.DisplayName != "" {
		r.opts.Cache.PutPerson(PersonKey(user.DisplayName), entry)
	}
	if user.Address() != "" {
		r.opts.Cache.PutPerson(PersonKey(user.Address()), entry)
	}
}

// memberByName looks for the person among the members of the user's chats and
// teams. It is the only source that works without a directory scope.
func (r *Resolver) memberByName(ctx context.Context, query string) (graph.User, bool, error) {
	members, err := r.members(ctx)
	if err != nil {
		return graph.User{}, false, err
	}
	if user, ok := pickMember(members, query); ok {
		return user, true, nil
	}
	return graph.User{}, false, nil
}

// members lists the people the signed-in user shares a chat or a team with,
// memoized for the lifetime of the Resolver so one command does not rescan.
func (r *Resolver) members(ctx context.Context) ([]graph.User, error) {
	if r.membersLoaded {
		return r.membersCache, nil
	}
	seen := map[string]bool{}
	var out []graph.User
	add := func(u graph.User) {
		if u.ID == "" || seen[u.ID] {
			return
		}
		seen[u.ID] = true
		out = append(out, u)
	}

	chats, err := r.opts.Client.ListChats(ctx, graph.ChatQuery{Members: true, Limit: membershipPageLimit})
	if err != nil && !isPermissionError(err) {
		return nil, err
	}
	for _, chat := range chats {
		for _, m := range chat.Members {
			add(graph.User{ID: m.UserID, DisplayName: m.DisplayName, Mail: m.Email})
		}
	}

	// The team scan is one call per team, so it only runs when the chat scan
	// found nobody: the chats of an active user usually cover the people they
	// talk to, which is exactly what PLAN.md:167 relies on.
	if len(out) == 0 {
		teams, terr := r.opts.Client.ListJoinedTeams(ctx)
		if terr != nil && !isPermissionError(terr) {
			return nil, terr
		}
		for _, team := range teams {
			members, merr := r.opts.Client.ListTeamMembers(ctx, team.ID)
			if merr != nil {
				continue
			}
			for _, m := range members {
				add(graph.User{ID: m.UserID, DisplayName: m.DisplayName, Mail: m.Email})
			}
		}
	}

	r.membersCache, r.membersLoaded = out, true
	return out, nil
}

// PersonChat returns the 1:1 chat id with a person. PLAN.md:166 documents why
// this is a scan and not a lookup: there is no person-to-chat API. The result
// is cached for a week, so the scan runs once per person.
func (r *Resolver) PersonChat(ctx context.Context, raw string) (Ref, error) {
	parsed, err := r.expand(raw)
	if err != nil {
		return Ref{}, err
	}
	if parsed.User == "" && parsed.Raw != "" {
		// A bare word is a person reference here too, exactly as in Person()
		// (PLAN.md:169 accepts a raw id, and a name is resolved the same way).
		parsed.User = parsed.Raw
	}
	key := PersonKey(parsed.User)
	if key == "" {
		return Ref{}, usagef("%q is not a person reference", raw)
	}
	if !r.opts.Refresh && r.opts.Cache != nil {
		if chatID, ok := r.opts.Cache.PersonChat(key); ok {
			r.debugf("cache: %s is chat %s", key, chatID)
			parsed.ChatID = chatID
			return parsed, nil
		}
	}
	user, err := r.resolvePerson(ctx, parsed)
	if err != nil {
		return Ref{}, err
	}
	chatID, err := r.findOneOnOneChat(ctx, user.ID)
	if err != nil {
		return Ref{}, err
	}
	if r.opts.Cache != nil {
		r.opts.Cache.PutPersonChat(key, chatID)
	}
	parsed.Kind = KindChat
	parsed.ChatID = chatID
	parsed.UserID = user.ID
	parsed.UserName = user.DisplayName
	return parsed, nil
}

// findOneOnOneChat scans the signed-in user's chats for the 1:1 chat with a
// person. A chat with exactly the two members is the one-on-one chat: the API
// documents that only one exists between two members
// (refs/graph/api-reference/v1.0/api/chat-post.md).
func (r *Resolver) findOneOnOneChat(ctx context.Context, userID string) (string, error) {
	chats, err := r.opts.Client.ListChats(ctx, graph.ChatQuery{Members: true, Top: graph.MaxTopChats, Limit: membershipPageLimit})
	if err != nil {
		return "", err
	}
	for _, chat := range chats {
		if chat.ChatType != "oneOnOne" {
			continue
		}
		found := false
		for _, m := range chat.Members {
			if m.UserID == userID {
				found = true
			}
		}
		if found {
			return chat.ID, nil
		}
	}
	return "", withFix(notFoundf("no 1:1 chat with %s exists yet", userID),
		"start the conversation first: `teams chat create --with <user>`")
}

// pickMember matches a query against conversation members: exact display name
// first (case-insensitive), then a substring match.
func pickMember(members []graph.User, query string) (graph.User, bool) {
	if u, ok := exactUser(members, query); ok {
		return u, true
	}
	var matches []graph.User
	for _, m := range members {
		if matchesName(m, query) {
			matches = append(matches, m)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return graph.User{}, false
}

// pickPerson matches a query against relevance-ranked people.
func pickPerson(people []graph.Person, query string) (graph.User, bool) {
	var fuzzy []graph.Person
	for _, p := range people {
		if strings.EqualFold(p.DisplayName, query) || strings.EqualFold(p.UserPrincipalName, query) {
			return graph.User{ID: p.ID, DisplayName: p.DisplayName, Mail: p.Address()}, true
		}
		if matchesName(graph.User{DisplayName: p.DisplayName, UserPrincipalName: p.UserPrincipalName, Mail: p.Address()}, query) {
			fuzzy = append(fuzzy, p)
		}
	}
	// /me/people is relevance-ordered, so the first match is the best one.
	if len(fuzzy) > 0 {
		p := fuzzy[0]
		return graph.User{ID: p.ID, DisplayName: p.DisplayName, Mail: p.Address()}, true
	}
	return graph.User{}, false
}

// pickUser matches a query against directory users.
func pickUser(users []graph.User, query string) (graph.User, bool) {
	if u, ok := exactUser(users, query); ok {
		return u, true
	}
	if len(users) == 1 {
		return users[0], true
	}
	var matches []graph.User
	for _, u := range users {
		if matchesName(u, query) {
			matches = append(matches, u)
		}
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	return graph.User{}, false
}

// exactUser finds the single user whose display name, address or UPN equals the
// query.
func exactUser(users []graph.User, query string) (graph.User, bool) {
	for _, u := range users {
		if strings.EqualFold(u.DisplayName, query) || strings.EqualFold(u.Address(), query) ||
			strings.EqualFold(u.UserPrincipalName, query) || strings.EqualFold(u.ID, query) {
			return u, true
		}
	}
	return graph.User{}, false
}

// matchesName is the fuzzy test: a display name, an address or a UPN that
// contains the query.
func matchesName(u graph.User, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return false
	}
	for _, field := range []string{u.DisplayName, u.Address(), u.UserPrincipalName, u.GivenName, u.Surname} {
		if field != "" && strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// isPermissionError reports whether an error means "this source is not granted"
// rather than "this source has no answer".
func isPermissionError(err error) bool {
	return graph.IsForbidden(err) || graph.IsUnauthorized(err)
}

// defaultPeopleLimit and defaultUserLimit bound the directory calls a single
// resolution makes.
const (
	defaultPeopleLimit = 25
	defaultUserLimit   = 25
)
