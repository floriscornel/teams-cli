package ref

import (
	"errors"
	"strings"
)

// Parse classifies a reference string without touching the network. It is the
// first half of PLAN.md's "Smart references": the resolver then turns the
// classification into ids. The classification is deliberately loose, because
// the same string means different things to different commands: a bare
// 19:...@thread.v2 is a chat id to `chat read` and nothing useful to
// `team show`.
func Parse(raw string) (Ref, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return Ref{}, usagef("empty reference")
	}
	if r, ok, err := ParseURL(s); ok {
		if err != nil {
			// A link the CLI refuses to open (an app, entity, task, call or
			// meeting link) is a usage error, not a generic failure (PLAN.md:164).
			var refErr *Error
			if errors.As(err, &refErr) {
				return Ref{}, err
			}
			return Ref{}, usagef("%s", err.Error())
		}
		r.Raw = s
		return r, nil
	}
	switch {
	case LooksLikeChannelID(s):
		return Ref{Raw: s, Kind: KindChannel, ChannelID: s}, nil
	case LooksLikeChatID(s):
		return Ref{Raw: s, Kind: KindChat, ChatID: s}, nil
	case strings.HasPrefix(s, "@"):
		name := strings.TrimSpace(strings.TrimPrefix(s, "@"))
		if name == "" {
			return Ref{}, usagef("@ needs a name, for example @alice")
		}
		return personRef(s, name, looksLikeEmail(name)), nil
	case looksLikeEmail(s):
		return personRef(s, s, true), nil
	case strings.Contains(s, "/"):
		parts := strings.Split(s, "/")
		for i, p := range parts {
			parts[i] = strings.TrimSpace(p)
			if parts[i] == "" {
				return Ref{}, usagef("name path %q has an empty segment", s)
			}
		}
		if len(parts) > 3 {
			return Ref{}, usagef("name path %q has %d segments; want Team, Team/Channel or Team/Channel/Message", s, len(parts))
		}
		r := Ref{Raw: s, Path: parts}
		switch len(parts) {
		case 1:
			r.Kind, r.TeamName = KindTeam, parts[0]
		case 2:
			r.Kind, r.TeamName, r.ChannelName = KindChannel, parts[0], parts[1]
		default:
			r.Kind, r.TeamName, r.ChannelName, r.MessageID = KindMessage, parts[0], parts[1], parts[2]
		}
		return r, nil
	default:
		// A raw id, a team name, a chat topic or a person's name: which one it is
		// depends on the command, so it stays unclassified until the resolver
		// asks for a specific kind. PLAN.md accepts all of them.
		return Ref{Raw: s, Kind: KindUnknown}, nil
	}
}

// personRef builds the @name, e-mail and bare-name forms of a person
// reference.
func personRef(raw, name string, email bool) Ref {
	user := name
	if i := strings.IndexFunc(name, func(r rune) bool { return r == '@' }); i > 0 {
		// An e-mail or a UPN; keep it whole, because Graph accepts both in the
		// path of GET /users/{id}.
		user = name
	}
	return Ref{Raw: raw, Kind: KindUser, User: user, IsEmail: email}
}

// looksLikeEmail reports whether s can be an e-mail address or a user
// principal name, which Graph resolves directly through GET /users/{id}. A
// leading @ is the person form, not an address.
func looksLikeEmail(s string) bool {
	// A Graph id carries an @ too (19:...@thread.tacv2), so anything with a
	// colon is not an address.
	if strings.HasPrefix(s, "@") || strings.ContainsAny(s, "/ :") {
		return false
	}
	i := strings.Index(s, "@")
	return i > 0 && i < len(s)-1 && strings.Contains(s[i+1:], ".") && !strings.Contains(s[i+1:], "@")
}

// PersonKey is the cache key for a person reference: the typed text, lowercased
// and without the leading @, so @Yuki, yuki and yuki@x.com stay distinct while
// casing does not. Distinct forms are intentional: the entity cache records
// which form resolved to whom.
func PersonKey(raw string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(raw, "@")))
}
