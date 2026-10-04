package ref

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/clock"
	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/store"
	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// This file covers the resolver half of PLAN.md:164-171 ("Smart references"):
// teams, channels, messages, chats, people and their 1:1 chats, aliases and the
// entity cache. Every test runs the real internal/graph client against the
// fakegraph server (internal/testing/fakegraph) and a real store.EntityCache
// built with LoadEntities in a t.TempDir(), so no request leaves the process, no
// test sleeps, and every timestamp comes from an injected clock.

// resolverNow is the frozen instant every test injects, through both the
// resolver (ResolverOptions.Now) and the entity cache (SetClock).
var resolverNow = time.Date(2026, 1, 4, 15, 4, 5, 0, time.UTC)

// The documented channel and chat ids the fixtures use. @thread.tacv2 is the
// channel family and @thread.v2 the chat family
// (refs/msteams/msteams-platform/concepts/build-and-test/deep-link-teams.md;
// refs/graph/api-reference/v1.0/api/chat-list-messages.md:79).
const (
	resChannelID = "19:9be3de4e70874c71a608dee9ba803ed3@thread.tacv2"
	resChatID    = "19:253f5895-9a62-4362-8d38-43f0205c702c@thread.v2"
	// resOneOnOneChatID is the id of the seeded 1:1 chat with u-alice.
	resOneOnOneChatID = "19:alice-1on1@thread.v2"
	// resGroupTopicChatID is the one chat whose topic no other chat shares.
	resGroupTopicChatID = "19:topic-other@thread.v2"
)

// resChannelMessageLink is a documented channel message link, with the team id
// its groupId carries (PLAN.md:157, PLAN.md:164).
const resChannelMessageLink = "https://teams.microsoft.com/l/message/" + resChannelID +
	"/1648741500652?groupId=t-eng&teamName=Engineering&channelName=General&parentMessageId=1648741500652"

// resChatMessageLink is a documented chat message link: the same path,
// discriminated only by the query string (PLAN.md:158).
const resChatMessageLink = "https://teams.microsoft.com/l/message/" + resChatID +
	"/1563480968434?context=%7B%22contextType%22%3A%22chat%22%7D"

// resChannelLink is a documented channel link, which carries the team id in
// groupId (PLAN.md:159).
const resChannelLink = "https://teams.microsoft.com/l/channel/" + resChannelID +
	"/General?groupId=t-eng&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee"

// resChannelLinkNoGroup is a channel link with no groupId: a channel id alone
// does not say which team it belongs to and there is no channel-to-team lookup
// (PLAN.md:164).
const resChannelLinkNoGroup = "https://teams.microsoft.com/l/channel/" + resChannelID + "/General"

// resChannelMessageLinkNoGroup is a channel message link with no groupId.
const resChannelMessageLinkNoGroup = "https://teams.microsoft.com/l/message/" + resChannelID + "/1648741500652"

// resChatLink is a documented chat link (PLAN.md:161).
const resChatLink = "https://teams.microsoft.com/l/chat/" + resChatID + "/conversations"

// TestResolverTeam covers team resolution: a bare name, a case-insensitive name,
// a unique substring name, a Teams team link and a channel link, which carries the
// team id in groupId (PLAN.md:158, PLAN.md:160,
// internal/ref/resolve.go:91-115).
func TestResolverTeam(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		raw    string
		wantID string
	}{
		{"bare name", "Engineering", "t-eng"},
		{"case-insensitive name", "engineering", "t-eng"},
		{"substring name", "plat", "t-plat"},
		// A name path's first element is the team, whichever name path shape the
		// input had (internal/ref/resolve.go:99-100).
		{"name path first element", "Engineering/General", "t-eng"},
		{"team link carries the groupId", "https://teams.microsoft.com/l/team/" + resChannelID + "/conversations?groupId=t-eng&tenantId=aaaabbbb-0000-cccc-1111-dddd2222eeee", "t-eng"},
		{"channel link carries the team id in groupId", resChannelLink, "t-eng"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, _ := newTestResolver(t)
			got, err := r.Team(context.Background(), tc.raw)
			if err != nil {
				t.Fatalf("Team(%q) error = %v", tc.raw, err)
			}
			if got.Kind != KindTeam || got.TeamID != tc.wantID {
				t.Fatalf("Team(%q) = %+v, want kind %q with team id %s", tc.raw, got, KindTeam, tc.wantID)
			}
			if got.Raw != tc.raw {
				t.Fatalf("Team(%q) Raw = %q, want the input", tc.raw, got.Raw)
			}
		})
	}
}

// TestResolverTeamGUID pins the raw-id branch of Team(): a bare GUID is taken as
// a team id with no lookup at all (internal/ref/resolve.go:101-104, PLAN.md:169).
func TestResolverTeamGUID(t *testing.T) {
	t.Parallel()

	const guid = "11111111-2222-3333-4444-555555555555"
	r, srv := newTestResolver(t)
	got, err := r.Team(context.Background(), guid)
	if err != nil {
		t.Fatalf("Team(%s): %v", guid, err)
	}
	if got.Kind != KindTeam || got.TeamID != guid {
		t.Fatalf("Team(%s) = %+v, want kind %q with the id itself", guid, got, KindTeam)
	}
	if n := requestCount(srv, "GET", "/me/joinedTeams"); n != 0 {
		t.Fatalf("joinedTeams requests = %d, want 0: a raw id needs no lookup", n)
	}
}

// TestResolverTeamChannelLinkWithoutGroupID pins PLAN.md:164 for a channel link:
// the link names a channel, not a team, and every channel route needs the team
// id, so Team() cannot answer it and says so.
func TestResolverTeamChannelLinkWithoutGroupID(t *testing.T) {
	t.Parallel()

	r, _ := newTestResolver(t)
	got, err := r.Team(context.Background(), resChannelLinkNoGroup)
	if err == nil {
		t.Fatalf("Team(channel link without groupId) = %+v, want a usage error", got)
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Fatalf("exit code = %d, want 2: %q is not a team (internal/ref/resolve.go:112-113)", code, resChannelLinkNoGroup)
	}
	assertErrorNames(t, err, resChannelLinkNoGroup)
}

// TestResolverTeamAmbiguous covers the ambiguity rule of PLAN.md:165: an ambiguous
// name asks you to pick on a TTY and errors in non-interactive mode. A nil Chooser
// never prompts; an interactive one that picks returns that team
// (internal/ref/resolve.go:375-432).
func TestResolverTeamAmbiguous(t *testing.T) {
	t.Parallel()

	t.Run("nil chooser is a usage error naming the candidates", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		// "engineer" is a substring of both Engineering and Engineering Ops
		// (internal/ref/resolve.go:393-406).
		got, err := r.Team(context.Background(), "engineer")
		if err == nil {
			t.Fatalf("Team(engineer) = %+v, want a usage error: two teams match", got)
		}
		if code := exitCodeOf(t, err); code != 2 {
			t.Fatalf("exit code = %d, want 2: the resolver refuses to guess in non-interactive mode (PLAN.md:165)", code)
		}
		assertErrorNames(t, err, "Engineering", "Engineering Ops")
	})

	t.Run("an interactive chooser decides", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t, func(o *ResolverOptions) {
			o.Chooser = &fakeChooser{interactive: true, pick: 1}
		})
		got, err := r.Team(context.Background(), "engineer")
		if err != nil {
			t.Fatalf("Team(engineer) with a chooser: %v", err)
		}
		if got.TeamID != "t-eng-ops" {
			t.Fatalf("Team(engineer) = %+v, want the chosen team t-eng-ops", got)
		}
	})

	t.Run("a choice out of range is an error", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t, func(o *ResolverOptions) {
			o.Chooser = &fakeChooser{interactive: true, pick: 7}
		})
		if _, err := r.Team(context.Background(), "engineer"); err == nil {
			t.Fatal("Team(engineer) with an out-of-range choice = nil error, want an error")
		} else if code := exitCodeOf(t, err); code != 1 {
			t.Fatalf("exit code = %d, want 1 (internal/ref/resolve.go:425-427)", code)
		}
	})
}

// TestResolverTeamCache covers PLAN.md:170: names are resolved through the entity
// cache and --refresh bypasses it. The proof that a cached answer is served is
// re-seeding the fake with a different name for the same team id: a resolver that
// still called Graph would answer with the new name.
func TestResolverTeamCache(t *testing.T) {
	t.Parallel()

	r, srv := newTestResolver(t)
	ctx := context.Background()

	first, err := r.Team(ctx, "Engineering")
	if err != nil {
		t.Fatalf("first Team(Engineering): %v", err)
	}
	if first.TeamID != "t-eng" || first.TeamName != "Engineering" {
		t.Fatalf("first Team(Engineering) = %+v, want t-eng/Engineering", first)
	}
	if n := requestCount(srv, "GET", "/me/joinedTeams"); n != 1 {
		t.Fatalf("joinedTeams requests = %d, want 1", n)
	}
	if !cacheHasTeam(r.Cache(), "Engineering", "t-eng") {
		t.Fatal("the entity cache does not hold Engineering -> t-eng; PLAN.md:170 requires names to be cached")
	}

	// The same team, renamed on the side. The member list is repeated because a
	// joinable team must still list the signed-in user.
	srv.Seed(fakegraph.Model{
		Me: "u-me",
		Teams: []fakegraph.Team{{
			ID: "t-eng", DisplayName: "Platform Engineering",
			Members: []fakegraph.Member{{UserID: "u-me"}},
		}},
	})

	second, err := r.Team(ctx, "Engineering")
	if err != nil {
		t.Fatalf("second Team(Engineering): %v", err)
	}
	if n := requestCount(srv, "GET", "/me/joinedTeams"); n != 1 {
		t.Fatalf("joinedTeams requests after a cache hit = %d, want 1: a cache hit must not call Graph again", n)
	}
	if second.TeamID != "t-eng" || second.TeamName != "Engineering" {
		t.Fatalf("second Team(Engineering) = %+v, want the cached t-eng/Engineering, not the renamed team", second)
	}

	// --refresh bypasses the cache and resolves the name again. The renaming is
	// the only way to observe it: the cached answer above still says Engineering.
	refreshed := NewResolver(ResolverOptions{Client: r.Client(), Cache: r.Cache(), Refresh: true, Now: fakeClock().Now})
	renamed, err := refreshed.Team(ctx, "Platform Engineering")
	if err != nil {
		t.Fatalf("refreshed Team(Platform Engineering): %v", err)
	}
	if renamed.TeamID != "t-eng" {
		t.Fatalf("refreshed Team(Platform Engineering) = %+v, want t-eng", renamed)
	}
	if n := requestCount(srv, "GET", "/me/joinedTeams"); n != 2 {
		t.Fatalf("joinedTeams requests after --refresh = %d, want 2 (PLAN.md:170: --refresh bypasses the cache)", n)
	}
}

// TestResolverTeamUnknown pins the exit-4 case: a well-formed reference that
// nothing matches is a not-found error naming what was searched for
// (internal/ref/resolve.go:402-404, AGENTS.md "Exit codes").
func TestResolverTeamUnknown(t *testing.T) {
	t.Parallel()

	r, _ := newTestResolver(t)
	got, err := r.Team(context.Background(), "Nonexistent")
	if err == nil {
		t.Fatalf("Team(Nonexistent) = %+v, want a not-found error", got)
	}
	if code := exitCodeOf(t, err); code != 4 {
		t.Fatalf("exit code = %d, want 4 for a team that does not exist", code)
	}
	assertErrorNames(t, err, "Nonexistent")
}

// TestResolverChannel covers channel resolution (internal/ref/resolve.go:153-252):
// the Team/Channel name path, a channel link whose groupId supplies the team, a
// raw channel id plus a --team hint and a case-insensitive or substring name. Each
// case asserts the resolved team and channel ids.
func TestResolverChannel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		raw      string
		teamHint string
	}{
		{"name path Team/Channel", "Engineering/General", ""},
		{"case-insensitive name path", "engineering/general", ""},
		{"substring name path", "Engineering/ener", ""},
		{"channel link with a groupId", resChannelLink, ""},
		{"raw channel id with a team name hint", resChannelID, "Engineering"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, _ := newTestResolver(t)
			got, err := r.Channel(context.Background(), tc.raw, tc.teamHint)
			if err != nil {
				t.Fatalf("Channel(%q, %q) error = %v", tc.raw, tc.teamHint, err)
			}
			if got.Kind != KindChannel || got.TeamID != "t-eng" || got.ChannelID != resChannelID {
				t.Fatalf("Channel(%q, %q) = %+v, want kind %q, team t-eng and channel %s", tc.raw, tc.teamHint, got, KindChannel, resChannelID)
			}
			if got.ChannelName != "General" {
				t.Fatalf("Channel(%q, %q) ChannelName = %q, want General", tc.raw, tc.teamHint, got.ChannelName)
			}
		})
	}
}

// TestResolverChannelWithoutTeam covers the failures PLAN.md:164 requires around a
// channel id that arrives without its team: a bare channel id needs --team, and a
// link without groupId cannot be resolved at all.
func TestResolverChannelWithoutTeam(t *testing.T) {
	t.Parallel()

	t.Run("channel link without groupId and without a hint", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Channel(context.Background(), resChannelLinkNoGroup, "")
		if err == nil {
			t.Fatalf("Channel(channel link without groupId) = %+v, want a not-found error", got)
		}
		if code := exitCodeOf(t, err); code != 4 {
			t.Fatalf("exit code = %d, want 4 (PLAN.md:164, internal/ref/resolve.go:168-171)", code)
		}
		assertErrorNames(t, err, resChannelID)
		assertErrorHint(t, err, "--team")
	})

	t.Run("a bare channel name without a hint", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Channel(context.Background(), "General", "")
		if err == nil {
			t.Fatalf("Channel(General) = %+v, want a usage error", got)
		}
		if code := exitCodeOf(t, err); code != 2 {
			t.Fatalf("exit code = %d, want 2: a channel name alone is not resolvable (PLAN.md:164)", code)
		}
		assertErrorNames(t, err, "General")
		assertErrorHint(t, err, "--team")
	})

	t.Run("a channel link without groupId plus a --team hint resolves", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Channel(context.Background(), resChannelLinkNoGroup, "Engineering")
		if err != nil {
			t.Fatalf("Channel(%q, Engineering): %v", resChannelLinkNoGroup, err)
		}
		if got.TeamID != "t-eng" || got.ChannelID != resChannelID || got.ChannelName != "General" {
			t.Fatalf("Channel(channel link without groupId, Engineering) = %+v, want t-eng/%s (General)", got, resChannelID)
		}
	})

	t.Run("a GUID hint is used as-is", func(t *testing.T) {
		t.Parallel()
		// teamIDFromHint passes a GUID straight through
		// (internal/ref/resolve.go:352-358), so the channel lookup goes to a team
		// that does not exist and Graph answers 404.
		const guid = "11111111-2222-3333-4444-555555555555"
		r, _ := newTestResolver(t)
		got, err := r.Channel(context.Background(), resChannelID, guid)
		if err == nil {
			t.Fatalf("Channel(%q, %s) = %+v, want the Graph 404 for a team that does not exist", resChannelID, guid, got)
		}
	})

	t.Run("a hint that resolves to no team fails as a team error", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Channel(context.Background(), resChannelID, "Nonexistent")
		if err == nil {
			t.Fatalf("Channel(%q, Nonexistent) = %+v, want a not-found error", resChannelID, got)
		}
		if code := exitCodeOf(t, err); code != 4 {
			t.Fatalf("exit code = %d, want 4", code)
		}
		assertErrorNames(t, err, "Nonexistent")
	})
}

// TestResolverChannelUnknown pins the exit-4 case for a channel name that no
// channel of the team matches.
func TestResolverChannelUnknown(t *testing.T) {
	t.Parallel()

	r, _ := newTestResolver(t)
	got, err := r.Channel(context.Background(), "Engineering/Nonexistent", "")
	if err == nil {
		t.Fatalf("Channel(Engineering/Nonexistent) = %+v, want a not-found error", got)
	}
	if code := exitCodeOf(t, err); code != 4 {
		t.Fatalf("exit code = %d, want 4", code)
	}
	assertErrorNames(t, err, "Nonexistent")
}

// TestResolverChannelAmbiguous covers channel ambiguity (PLAN.md:165): two
// channels that match are a usage error without a chooser and the picked one with
// an interactive chooser, and a display name that two channels share is ambiguous
// too (internal/ref/resolve.go:380-391).
func TestResolverChannelAmbiguous(t *testing.T) {
	t.Parallel()

	t.Run("two channels share a display name", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Channel(context.Background(), "Engineering/Twin", "")
		if err == nil {
			t.Fatalf("Channel(Engineering/Twin) = %+v, want a usage error: two channels are called Twin", got)
		}
		if code := exitCodeOf(t, err); code != 2 {
			t.Fatalf("exit code = %d, want 2 (PLAN.md:165)", code)
		}
		assertErrorNames(t, err, "Twin")
	})

	t.Run("two channels match a substring and there is no chooser", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Channel(context.Background(), "Engineering/ps", "")
		if err == nil {
			t.Fatalf("Channel(Engineering/ps) = %+v, want a usage error", got)
		}
		if code := exitCodeOf(t, err); code != 2 {
			t.Fatalf("exit code = %d, want 2 (PLAN.md:165)", code)
		}
		assertErrorNames(t, err, "Ops", "Ops Archive")
	})

	t.Run("an interactive chooser decides", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t, func(o *ResolverOptions) {
			o.Chooser = &fakeChooser{interactive: true, pick: 0}
		})
		got, err := r.Channel(context.Background(), "Engineering/ps", "")
		if err != nil {
			t.Fatalf("Channel(Engineering/ps) with a chooser: %v", err)
		}
		if got.ChannelID != "c-ops-1" {
			t.Fatalf("Channel(Engineering/ps) = %+v, want the first candidate c-ops-1", got)
		}
	})
}

// TestResolverChannelCache pins PLAN.md:170 for channels: resolving a channel name
// fills the entity cache, and a second Channel call is served from it without
// another Graph call.
func TestResolverChannelCache(t *testing.T) {
	t.Parallel()

	r, srv := newTestResolver(t)
	ctx := context.Background()

	first, err := r.Channel(ctx, "Engineering/General", "")
	if err != nil {
		t.Fatalf("first Channel(Engineering/General): %v", err)
	}
	if first.ChannelID != resChannelID {
		t.Fatalf("first Channel = %+v, want %s", first, resChannelID)
	}
	if !cacheHasChannel(r.Cache(), "t-eng", "general", resChannelID) {
		t.Fatal("the entity cache does not hold t-eng/general -> the channel id")
	}

	second, err := r.Channel(ctx, "Engineering/General", "")
	if err != nil {
		t.Fatalf("second Channel(Engineering/General): %v", err)
	}
	if second.ChannelID != resChannelID {
		t.Fatalf("second Channel = %+v, want the cached %s", second, resChannelID)
	}
	if n := requestCount(srv, "GET", "/me/joinedTeams"); n != 1 {
		t.Fatalf("joinedTeams requests = %d, want 1: the second call must resolve from the cache", n)
	}
	if n := requestCount(srv, "GET", "/teams/t-eng/channels"); n != 1 {
		t.Fatalf("channel list requests = %d, want 1: the second call must resolve from the cache", n)
	}
}

// TestResolverMessage pins the message shapes PLAN.md:157-158 and
// internal/ref/resolve.go:313-347 produce: a channel link carries TeamID,
// ChannelID and MessageID, a chat link carries InChat, ChatID and MessageID, and a
// Team/Channel/MessageId name path resolves its container.
func TestResolverMessage(t *testing.T) {
	t.Parallel()

	t.Run("channel message link", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Message(context.Background(), resChannelMessageLink, "")
		if err != nil {
			t.Fatalf("Message(channel link): %v", err)
		}
		if got.Kind != KindMessage || got.TeamID != "t-eng" || got.ChannelID != resChannelID ||
			got.MessageID != "1648741500652" || got.InChat {
			t.Fatalf("Message(channel link) = %+v, want a channel message in t-eng/%s", got, resChannelID)
		}
		if got.ChannelName != "General" {
			t.Fatalf("Message(channel link) ChannelName = %q, want General (channelByID fills it in)", got.ChannelName)
		}
	})

	t.Run("chat message link", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Message(context.Background(), resChatMessageLink, "")
		if err != nil {
			t.Fatalf("Message(chat link): %v", err)
		}
		if !got.InChat || got.ChatID != resChatID || got.MessageID != "1563480968434" || got.Kind != KindMessage {
			t.Fatalf("Message(chat link) = %+v, want a chat message in %s", got, resChatID)
		}
		if got.TeamID != "" || got.ChannelID != "" {
			t.Fatalf("Message(chat link) = %+v, want no team or channel", got)
		}
	})

	t.Run("Team/Channel/MessageId name path", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		raw := "Engineering/General/1758011222333"
		got, err := r.Message(context.Background(), raw, "")
		if err != nil {
			t.Fatalf("Message(%s): %v", raw, err)
		}
		if got.Kind != KindMessage || got.TeamID != "t-eng" || got.ChannelID != resChannelID || got.MessageID != "1758011222333" {
			t.Fatalf("Message(%s) = %+v, want t-eng/%s/1758011222333", raw, got, resChannelID)
		}
	})
}

// TestResolverMessageFailures covers the two rejections PLAN.md:164 and
// internal/ref/resolve.go:322-326, 341-346 require.
func TestResolverMessageFailures(t *testing.T) {
	t.Parallel()

	t.Run("a bare message id cannot find its container", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Message(context.Background(), "1758011222333", "")
		if err == nil {
			t.Fatalf("Message(bare id) = %+v, want an error: no Graph route finds the container from the id alone", got)
		}
		if code := exitCodeOf(t, err); code != 2 {
			t.Fatalf("exit code = %d, want 2 (internal/ref/resolve.go:341-346)", code)
		}
		assertErrorNames(t, err, "1758011222333")
	})

	t.Run("a channel message link without groupId and without a hint", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Message(context.Background(), resChannelMessageLinkNoGroup, "")
		if err == nil {
			t.Fatalf("Message(channel link without groupId) = %+v, want an error", got)
		}
		if code := exitCodeOf(t, err); code != 4 {
			t.Fatalf("exit code = %d, want 4: PLAN.md:164 makes an ungrouped channel message link unresolvable", code)
		}
		assertErrorHint(t, err, "--team")
	})
}

// TestResolverChat covers chat resolution (internal/ref/resolve.go:256-308): a
// chat link, a raw chat id, a chat matched by topic and the exit-2 case of a GUID
// that is not a chat id.
func TestResolverChat(t *testing.T) {
	t.Parallel()

	t.Run("chat link", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Chat(context.Background(), resChatLink)
		if err != nil {
			t.Fatalf("Chat(chat link): %v", err)
		}
		if got.Kind != KindChat || got.ChatID != resChatID {
			t.Fatalf("Chat(chat link) = %+v, want %s", got, resChatID)
		}
	})

	t.Run("raw chat id", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Chat(context.Background(), resChatID)
		if err != nil {
			t.Fatalf("Chat(raw chat id): %v", err)
		}
		if got.Kind != KindChat || got.ChatID != resChatID {
			t.Fatalf("Chat(raw chat id) = %+v, want %s", got, resChatID)
		}
	})

	t.Run("chat matched by topic", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Chat(context.Background(), "Standup")
		if err != nil {
			t.Fatalf("Chat(Standup): %v", err)
		}
		if got.Kind != KindChat || got.ChatID != "19:group-standup@thread.v2" || got.ChatTopic != "Standup" {
			t.Fatalf("Chat(Standup) = %+v, want the chat whose topic is Standup", got)
		}
	})

	t.Run("a chat topic that matches nothing is not found", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Chat(context.Background(), "No Such Topic")
		if err == nil {
			t.Fatalf("Chat(No Such Topic) = %+v, want a not-found error", got)
		}
		if code := exitCodeOf(t, err); code != 4 {
			t.Fatalf("exit code = %d, want 4", code)
		}
		assertErrorNames(t, err, "No Such Topic")
	})

	t.Run("a GUID is not a chat id", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Chat(context.Background(), "11111111-2222-3333-4444-555555555555")
		if err == nil {
			t.Fatalf("Chat(guid) = %+v, want a usage error", got)
		}
		if code := exitCodeOf(t, err); code != 2 {
			t.Fatalf("exit code = %d, want 2: a chat id starts with 19: (internal/ref/resolve.go:273-276)", code)
		}
		assertErrorNames(t, err, "11111111-2222-3333-4444-555555555555")
	})
}

// TestResolverChatAmbiguous covers the ambiguity rule for topics: two chats that
// share a topic are a usage error without a chooser and the picked one with an
// interactive chooser (PLAN.md:165, internal/ref/resolve.go:299-306).
func TestResolverChatAmbiguous(t *testing.T) {
	t.Parallel()

	t.Run("nil chooser is a usage error naming the candidates", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.Chat(context.Background(), "Release train")
		if err == nil {
			t.Fatalf("Chat(Release train) = %+v, want a usage error: two chats share the topic", got)
		}
		if code := exitCodeOf(t, err); code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
		assertErrorNames(t, err, "Release train")
	})

	t.Run("an interactive chooser decides", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t, func(o *ResolverOptions) {
			o.Chooser = &fakeChooser{interactive: true, pick: 0}
		})
		got, err := r.Chat(context.Background(), "Release train")
		if err != nil {
			t.Fatalf("Chat(Release train) with a chooser: %v", err)
		}
		if got.ChatID != "19:topic-one@thread.v2" {
			t.Fatalf("Chat(Release train) = %+v, want the first chat", got)
		}
	})
}

// TestResolverPersonOrder pins the resolution order PLAN.md:167 fixes: alias (the
// caller), the entity cache, the members of the chats and teams, then
// GET /me/people and finally GET /users with a startswith filter. The recorded
// requests prove which sources were consulted, and the fixture is built so each
// source is the only one that can answer for one person.
func TestResolverPersonOrder(t *testing.T) {
	t.Parallel()

	t.Run("membership resolves the person without a directory call", func(t *testing.T) {
		t.Parallel()
		r, srv := newTestResolver(t)
		got, err := r.Person(context.Background(), "@Priya")
		if err != nil {
			t.Fatalf("Person(@Priya): %v", err)
		}
		if got.UserID != "u-priya" || got.UserName != "Priya Member" {
			t.Fatalf("Person(@Priya) = %+v, want u-priya/Priya Member", got)
		}
		if got.Kind != KindUser || got.UserMail != "priya@contoso.example" {
			t.Fatalf("Person(@Priya) = %+v, want the user form with its address", got)
		}
		if n := requestCount(srv, "GET", "/me/chats"); n != 1 {
			t.Fatalf("chat member scan requests = %d, want 1", n)
		}
		if n := requestCount(srv, "GET", "/me/joinedTeams"); n != 0 {
			t.Fatalf("joinedTeams requests = %d, want 0: the chat scan already found the person", n)
		}
		if n := requestCount(srv, "GET", "/me/people"); n != 0 {
			t.Fatalf("me/people requests = %d, want 0: membership answered first (PLAN.md:167)", n)
		}
		if n := requestCount(srv, "GET", "/users"); n != 0 {
			t.Fatalf("directory search requests = %d, want 0: membership answered first (PLAN.md:167)", n)
		}
	})

	t.Run("me/people answers when membership does not", func(t *testing.T) {
		t.Parallel()
		r, srv := newTestResolver(t)
		got, err := r.Person(context.Background(), "@Zed")
		if err != nil {
			t.Fatalf("Person(@Zed): %v", err)
		}
		if got.UserID != "u-zed" || got.UserName != "Zed Directory" {
			t.Fatalf("Person(@Zed) = %+v, want u-zed/Zed Directory", got)
		}
		if n := requestCount(srv, "GET", "/me/people"); n != 1 {
			t.Fatalf("me/people requests = %d, want 1", n)
		}
		if n := requestCount(srv, "GET", "/users"); n != 0 {
			t.Fatalf("directory search requests = %d, want 0: /me/people is the better source and comes first (PLAN.md:167)", n)
		}
	})

	t.Run("a 403 from /me/people still lets the /users startswith search answer", func(t *testing.T) {
		t.Parallel()
		// People.Read is not granted, so /me/people answers 403. PLAN.md:167
		// requires that to mean "this source is unavailable" rather than a failure:
		// the next source, GET /users with a startswith filter, must still get its
		// turn. u-quinn is not a member of any chat, so membership cannot answer.
		srv := newFakeGraphServer(t, func(o *fakegraph.Options) {
			o.Scopes = []string{"Chat.ReadBasic", "User.ReadBasic.All"}
		})
		r := NewResolver(ResolverOptions{Client: newGraphClient(t, srv), Cache: newTestCache(t), Now: fakeClock().Now})
		got, err := r.Person(context.Background(), "@Quinn")
		if err != nil {
			t.Fatalf("Person(@Quinn) with People.Read withheld: %v", err)
		}
		if got.UserID != "u-quinn" || got.UserName != "Quinn Fallback" {
			t.Fatalf("Person(@Quinn) = %+v, want u-quinn/Quinn Fallback from the startswith search", got)
		}
		if n := requestCount(srv, "GET", "/me/people"); n != 1 {
			t.Fatalf("me/people requests = %d, want 1: the source is tried and its 403 ignored", n)
		}
		users := requestsFor(srv, "GET", "/users")
		if len(users) == 0 {
			t.Fatal("the /users startswith search was never called")
		}
		if filter := users[0].Query.Get("$filter"); !strings.Contains(filter, "startswith") {
			t.Fatalf("$filter = %q, want the documented startswith filter (PLAN.md:167)", filter)
		}
	})

	t.Run("a raw user id is the last resort", func(t *testing.T) {
		t.Parallel()
		r, srv := newTestResolver(t)
		got, err := r.Person(context.Background(), "user-raw")
		if err != nil {
			t.Fatalf("Person(user-raw): %v", err)
		}
		if got.UserID != "user-raw" || got.UserName != "Rita Rawid" {
			t.Fatalf("Person(user-raw) = %+v, want user-raw/Rita Rawid", got)
		}
		if n := requestCount(srv, "GET", "/users/user-raw"); n != 1 {
			t.Fatalf("GET /users/user-raw requests = %d, want 1: a bare id is resolved exactly (PLAN.md:169)", n)
		}
		if n := requestCount(srv, "GET", "/me/people"); n != 1 {
			t.Fatalf("me/people requests = %d, want 1: every name source is tried before the exact id", n)
		}
		if n := len(requestsFor(srv, "GET", "/users")); n == 0 {
			t.Fatal("the /users startswith search was never tried")
		}
	})
}

// TestResolverPersonPermissionFallback pins the rest of PLAN.md:167: a directory
// call that needs an ungranted scope answers 403, which must not fail the lookup
// while another source can still answer, and when every source fails the error
// still names the query and points at the user search command.
func TestResolverPersonPermissionFallback(t *testing.T) {
	t.Parallel()

	t.Run("membership answers while the directory is forbidden", func(t *testing.T) {
		t.Parallel()
		srv := newFakeGraphServer(t, func(o *fakegraph.Options) {
			o.Scopes = []string{"Chat.ReadBasic", "Team.ReadBasic.All"}
		})
		r := NewResolver(ResolverOptions{Client: newGraphClient(t, srv), Cache: newTestCache(t), Now: fakeClock().Now})
		got, err := r.Person(context.Background(), "@Priya")
		if err != nil {
			t.Fatalf("Person(@Priya) with People.Read and User.ReadBasic.All withheld: %v", err)
		}
		if got.UserID != "u-priya" {
			t.Fatalf("Person(@Priya) = %+v, want u-priya from membership", got)
		}
		if n := requestCount(srv, "GET", "/me/people"); n != 0 {
			t.Fatalf("me/people requests = %d, want 0: membership answered first", n)
		}
	})

	t.Run("every source fails and the error names the query", func(t *testing.T) {
		t.Parallel()
		srv := newFakeGraphServer(t, func(o *fakegraph.Options) {
			o.Scopes = []string{"Chat.ReadBasic"}
		})
		r := NewResolver(ResolverOptions{Client: newGraphClient(t, srv), Cache: newTestCache(t), Now: fakeClock().Now})
		got, err := r.Person(context.Background(), "@Nobody")
		if err == nil {
			t.Fatalf("Person(@Nobody) = %+v, want an error: nobody matches and every source is forbidden", got)
		}
		if code := exitCodeOf(t, err); code != 4 {
			t.Fatalf("exit code = %d, want 4", code)
		}
		assertErrorNames(t, err, "Nobody")
		// The message names the searchable form of the query and the fix line
		// points at the command that lists the candidates
		// (internal/ref/person.go:127-128).
		assertErrorHint(t, err, "user search")
	})
}

// TestResolverPersonEmailResolution pins the fast path PLAN.md:167 allows for an
// address: GET /users/{user-id} accepts a user principal name, so an e-mail is one
// exact call (internal/ref/person.go:66-78,
// refs/graph/api-reference/v1.0/api/user-get.md).
func TestResolverPersonEmailResolution(t *testing.T) {
	t.Parallel()

	r, srv := newTestResolver(t)
	got, err := r.Person(context.Background(), "alice@contoso.example")
	if err != nil {
		t.Fatalf("Person(alice@contoso.example): %v", err)
	}
	if got.UserID != "u-alice" || got.UserName != "Alice Example" || !got.IsEmail {
		t.Fatalf("Person(email) = %+v, want u-alice/Alice Example with IsEmail", got)
	}
	if n := requestCount(srv, "GET", "/users/alice@contoso.example"); n != 1 {
		t.Fatalf("GET /users/{upn} requests = %d, want 1", n)
	}
	if n := requestCount(srv, "GET", "/me/people"); n != 0 {
		t.Fatalf("me/people requests = %d, want 0: an address resolves in one exact call", n)
	}
}

// TestResolverPersonCache pins the person half of PLAN.md:170: a resolved person
// is cached for a week, the display name and the address are stored as separate
// keys so a second lookup in either spelling is a hit, and the entry is stamped by
// the injected clock (internal/ref/person.go:147-159).
func TestResolverPersonCache(t *testing.T) {
	t.Parallel()

	r, srv := newTestResolver(t)
	ctx := context.Background()

	first, err := r.Person(ctx, "alice@contoso.example")
	if err != nil {
		t.Fatalf("first Person(alice@contoso.example): %v", err)
	}
	if first.UserID != "u-alice" {
		t.Fatalf("first Person = %+v, want u-alice", first)
	}
	if n := requestCount(srv, "GET", "/users/alice@contoso.example"); n != 1 {
		t.Fatalf("GET /users/{upn} requests = %d, want 1", n)
	}

	entry, ok := r.Cache().Person("alice@contoso.example")
	if !ok || entry.UserID != "u-alice" || entry.DisplayName != "Alice Example" || entry.Mail != "alice@contoso.example" {
		t.Fatalf("cache person (address key) = %+v, ok %v; want the user id, the display name and the mail", entry, ok)
	}
	byName, ok := r.Cache().Person("alice example")
	if !ok || byName.UserID != "u-alice" {
		t.Fatalf("cache person (display-name key) = %+v, ok %v; want the same person under the name key", byName, ok)
	}

	// The second lookup uses the display-name spelling and is a cache hit: no
	// request, in either direction.
	second, err := r.Person(ctx, "@Alice Example")
	if err != nil {
		t.Fatalf("second Person(@Alice Example): %v", err)
	}
	if second.UserID != "u-alice" {
		t.Fatalf("second Person = %+v, want the cached u-alice", second)
	}
	if n := requestCount(srv, "GET", "/users/alice@contoso.example"); n != 1 {
		t.Fatalf("GET /users/{upn} requests after the second lookup = %d, want 1: it is a cache hit", n)
	}
	if n := requestCount(srv, "GET", "/me/people"); n != 0 {
		t.Fatalf("me/people requests = %d, want 0: the second lookup is a cache hit", n)
	}

	// The entry is stamped by the injected clock: pruning at the instant the
	// resolver was handed leaves it alone, a week later drops it (PLAN.md:170).
	if gone := r.Cache().Prune(resolverNow.Add(time.Hour)); gone != 0 {
		t.Fatalf("Prune(now+1h) dropped %d entries; a person entry is fresh for a week", gone)
	}
	if gone := r.Cache().Prune(resolverNow.Add(8 * 24 * time.Hour)); gone == 0 {
		t.Fatal("Prune(now+8d) dropped nothing; PersonTTL is 7 days (PLAN.md:170)")
	}
}

// TestResolverPersonChat pins PLAN.md:166: there is no person-to-chat lookup, so
// the 1:1 chat is found by scanning GET /me/chats?$expand=members, the result is
// cached, and a person without one gets a fix line pointing at chat create.
func TestResolverPersonChat(t *testing.T) {
	t.Parallel()

	t.Run("the 1:1 chat is found by the member scan", func(t *testing.T) {
		t.Parallel()
		r, srv := newTestResolver(t)
		got, err := r.PersonChat(context.Background(), "@Alice")
		if err != nil {
			t.Fatalf("PersonChat(@Alice): %v", err)
		}
		if got.Kind != KindChat || got.ChatID != resOneOnOneChatID || got.UserID != "u-alice" {
			t.Fatalf("PersonChat(@Alice) = %+v, want the 1:1 chat with u-alice", got)
		}
		chats := requestsFor(srv, "GET", "/me/chats")
		if len(chats) == 0 {
			t.Fatal("no chat scan was recorded")
		}
		if expand := chats[0].Query.Get("$expand"); !strings.Contains(expand, "members") {
			t.Fatalf("$expand = %q, want members: the scan needs the roster (PLAN.md:166)", expand)
		}
	})

	t.Run("the scan result is cached", func(t *testing.T) {
		t.Parallel()
		r, srv := newTestResolver(t)
		ctx := context.Background()
		// Resolve the person first, so the person cache is warm and the second
		// call does not need the membership scan either.
		if _, err := r.Person(ctx, "@Alice"); err != nil {
			t.Fatalf("Person(@Alice): %v", err)
		}
		srv.ResetRequests()

		if _, err := r.PersonChat(ctx, "@Alice"); err != nil {
			t.Fatalf("first PersonChat(@Alice): %v", err)
		}
		if n := requestCount(srv, "GET", "/me/chats"); n == 0 {
			t.Fatal("the first PersonChat made no chat scan")
		}
		before := len(srv.Requests())
		got, err := r.PersonChat(ctx, "@Alice")
		if err != nil {
			t.Fatalf("second PersonChat(@Alice): %v", err)
		}
		if got.ChatID != resOneOnOneChatID {
			t.Fatalf("second PersonChat = %+v, want the cached 1:1 chat", got)
		}
		if after := len(srv.Requests()); after != before {
			t.Fatalf("requests after the second call = %d, want %d: the scan runs once per person (PLAN.md:166)", after, before)
		}
	})

	t.Run("no 1:1 chat suggests chat create", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		got, err := r.PersonChat(context.Background(), "@Quinn")
		if err == nil {
			t.Fatalf("PersonChat(@Quinn) = %+v, want a not-found error", got)
		}
		if code := exitCodeOf(t, err); code != 4 {
			t.Fatalf("exit code = %d, want 4 (internal/ref/person.go:283-284)", code)
		}
		assertErrorHint(t, err, "chat create --with")
	})
}

// TestResolverAlias pins PLAN.md:168: an alias is expanded before parsing, with or
// without its leading "@", and it works for a person, a channel name path and a
// chat (internal/ref/resolve.go:71-87).
func TestResolverAlias(t *testing.T) {
	t.Parallel()

	aliases := map[string]string{
		"boss":    "alice@colorkrew.com",
		"standup": "Engineering/General",
		"lounge":  resGroupTopicChatID,
	}

	t.Run("an alias for a person expands with and without the @", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{"@boss", "boss"} {
			r, srv := newTestResolver(t, func(o *ResolverOptions) { o.Aliases = aliases })
			got, err := r.Person(context.Background(), raw)
			if err != nil {
				t.Fatalf("Person(%q): %v", raw, err)
			}
			if got.UserID != "u-alice" {
				t.Fatalf("Person(%q) = %+v, want u-alice through the alias", raw, got)
			}
			if got.Raw != raw {
				t.Fatalf("Person(%q) Raw = %q, want the name the user typed", raw, got.Raw)
			}
			// The alias target is mail-shaped, so the exact GET /users/{upn} path
			// is used: proof that the target, not the alias name, was parsed.
			if n := requestCount(srv, "GET", "/users/alice@colorkrew.com"); n != 1 {
				t.Fatalf("Person(%q): GET /users/alice@colorkrew.com requests = %d, want 1", raw, n)
			}
		}
	})

	t.Run("an alias for a channel name path", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t, func(o *ResolverOptions) { o.Aliases = aliases })
		got, err := r.Channel(context.Background(), "@standup", "")
		if err != nil {
			t.Fatalf("Channel(@standup): %v", err)
		}
		if got.TeamID != "t-eng" || got.ChannelID != resChannelID {
			t.Fatalf("Channel(@standup) = %+v, want t-eng/%s", got, resChannelID)
		}
	})

	t.Run("an alias for a chat", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t, func(o *ResolverOptions) { o.Aliases = aliases })
		got, err := r.Chat(context.Background(), "@lounge")
		if err != nil {
			t.Fatalf("Chat(@lounge): %v", err)
		}
		if got.Kind != KindChat || got.ChatID != resGroupTopicChatID {
			t.Fatalf("Chat(@lounge) = %+v, want the chat %s", got, resGroupTopicChatID)
		}
	})

	t.Run("an empty alias table parses the reference itself", func(t *testing.T) {
		t.Parallel()
		r, _ := newTestResolver(t)
		if _, err := r.Chat(context.Background(), "@lounge"); err == nil {
			t.Fatal("Chat(@lounge) without aliases = nil error, want no person called lounge")
		}
	})
}

// TestResolverRawID pins PLAN.md:169 for the raw-id forms the resolver accepts: a
// raw channel id with a team hint, and a raw chat id, both of which need no name
// lookup beyond the team the hint names (internal/ref/parse.go:26-29).
func TestResolverRawID(t *testing.T) {
	t.Parallel()

	t.Run("a raw channel id with a --team name hint resolves", func(t *testing.T) {
		t.Parallel()
		r, srv := newTestResolver(t)
		got, err := r.Channel(context.Background(), resChannelID, "Engineering")
		if err != nil {
			t.Fatalf("Channel(%q, Engineering) error = %v", resChannelID, err)
		}
		if got.TeamID != "t-eng" || got.ChannelID != resChannelID || got.ChannelName != "General" {
			t.Fatalf("Channel(%q, Engineering) = %+v, want t-eng/%s (General)", resChannelID, got, resChannelID)
		}
		// The hint is a name, so it is resolved through the joined-teams listing
		// first (internal/ref/resolve.go:352-364).
		if n := requestCount(srv, "GET", "/me/joinedTeams"); n != 1 {
			t.Fatalf("joinedTeams requests = %d, want 1: a name hint is resolved as a team", n)
		}
	})

	t.Run("a raw chat id resolves to itself", func(t *testing.T) {
		t.Parallel()
		r, srv := newTestResolver(t)
		got, err := r.Chat(context.Background(), resChatID)
		if err != nil {
			t.Fatalf("Chat(%q) error = %v", resChatID, err)
		}
		if got.Kind != KindChat || got.ChatID != resChatID {
			t.Fatalf("Chat(%q) = %+v, want the id back", resChatID, got)
		}
		if n := len(srv.Requests()); n != 0 {
			t.Fatalf("requests = %d, want 0: a chat id needs no Graph call", n)
		}
	})
}

// fakeChooser is a ref.Chooser whose answer and interactivity a test sets. It
// stands in for the CLI picker (internal/cli/read.go:81-101).
type fakeChooser struct {
	interactive bool
	pick        int
	question    string
	options     []string
}

// Interactive reports whether the session can prompt.
func (f *fakeChooser) Interactive() bool { return f.interactive }

// Choose records the question and returns the configured index.
func (f *fakeChooser) Choose(question string, options []string) (int, error) {
	f.question, f.options = question, options
	return f.pick, nil
}

// fakeClock is a frozen clock at resolverNow, the same instant the entity cache is
// given. It keeps every test deterministic.
func fakeClock() *clock.Fake { return clock.NewFake(resolverNow) }

// Fixture timestamps: constants, so a failing assertion is readable from the
// source (the seed DSL never reads the wall clock).
var (
	tJan1 = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	tJan2 = time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	tJan3 = time.Date(2026, 1, 3, 9, 0, 0, 0, time.UTC)
)

// resolverModel is the seed every resolver test starts from. It is built so each
// source of the person order has somebody only it can answer for:
//   - u-priya and u-alice are chat members, which membership alone knows;
//   - u-zed is in no chat and no team, so only /me/people can answer;
//   - u-quinn is in no chat and no team, so the /users startswith search answers
//     when /me/people is unavailable;
//   - user-raw is in no chat and no team and no name matches its id, so only
//     GET /users/user-raw can answer.
//
// Two teams share the "engineer" substring so a name can be ambiguous, and two
// chats share the "Release train" topic for the same reason.
func resolverModel() fakegraph.Model {
	return fakegraph.Model{
		Me: "u-me",
		Users: []fakegraph.User{
			{ID: "u-me", DisplayName: "Me Myself", UserPrincipalName: "me@contoso.example", Mail: "me@contoso.example", Relevance: 1},
			{ID: "u-alice", DisplayName: "Alice Example", UserPrincipalName: "alice@contoso.example", Mail: "alice@contoso.example", Relevance: 9},
			{ID: "u-priya", DisplayName: "Priya Member", UserPrincipalName: "priya@contoso.example", Mail: "priya@contoso.example"},
			{ID: "u-zed", DisplayName: "Zed Directory", UserPrincipalName: "zed@contoso.example", Mail: "zed@contoso.example", Relevance: 5},
			{ID: "u-quinn", DisplayName: "Quinn Fallback", UserPrincipalName: "quinn@contoso.example", Mail: "quinn@contoso.example"},
			{ID: "user-raw", DisplayName: "Rita Rawid", UserPrincipalName: "rita@contoso.example", Mail: "rita@contoso.example"},
		},
		Teams: []fakegraph.Team{
			{
				ID: "t-eng", DisplayName: "Engineering", Created: tJan1,
				Members: []fakegraph.Member{{UserID: "u-me"}, {UserID: "u-alice"}},
				Channels: []fakegraph.Channel{
					{ID: resChannelID, DisplayName: "General", Created: tJan1},
					{ID: "c-ops-1", DisplayName: "Ops", Created: tJan2},
					{ID: "c-ops-2", DisplayName: "Ops Archive", Created: tJan2},
					{ID: "c-twin-1", DisplayName: "Twin", Created: tJan3},
					{ID: "c-twin-2", DisplayName: "Twin", Created: tJan3},
				},
			},
			{
				ID: "t-eng-ops", DisplayName: "Engineering Ops", Created: tJan1,
				Members:  []fakegraph.Member{{UserID: "u-me"}},
				Channels: []fakegraph.Channel{{ID: "c-ops-only", DisplayName: "Only", Created: tJan1}},
			},
			{
				ID: "t-plat", DisplayName: "Platform Team", Created: tJan1,
				Members:  []fakegraph.Member{{UserID: "u-me"}},
				Channels: []fakegraph.Channel{{ID: "c-plaza", DisplayName: "Plaza", Created: tJan1}},
			},
		},
		Chats: []fakegraph.Chat{
			{
				ID: "19:priya-1on1@thread.v2", ChatType: fakegraph.ChatTypeOneOnOne, Created: tJan1,
				Members: []fakegraph.Member{{UserID: "u-me"}, {UserID: "u-priya"}},
			},
			{
				ID: resOneOnOneChatID, ChatType: fakegraph.ChatTypeOneOnOne, Created: tJan1,
				Members: []fakegraph.Member{{UserID: "u-me"}, {UserID: "u-alice"}},
			},
			{
				ID: "19:topic-one@thread.v2", ChatType: fakegraph.ChatTypeGroup, Topic: "Release train", Created: tJan2,
				Members: []fakegraph.Member{{UserID: "u-me"}, {UserID: "u-alice"}},
			},
			{
				ID: "19:topic-two@thread.v2", ChatType: fakegraph.ChatTypeGroup, Topic: "Release train", Created: tJan3,
				Members: []fakegraph.Member{{UserID: "u-me"}, {UserID: "u-priya"}},
			},
			{
				ID: resGroupTopicChatID, ChatType: fakegraph.ChatTypeGroup, Topic: "Design review", Created: tJan3,
				Members: []fakegraph.Member{{UserID: "u-me"}},
			},
			{
				ID: "19:group-standup@thread.v2", ChatType: fakegraph.ChatTypeGroup, Topic: "Standup", Created: tJan2,
				Members: []fakegraph.Member{{UserID: "u-me"}},
			},
		},
	}
}

// newFakeGraphServer starts a fakegraph server seeded with resolverModel and a
// frozen clock. mutate changes the server options, which is how the permission
// tests withhold scopes.
func newFakeGraphServer(t *testing.T, mutate ...func(*fakegraph.Options)) *fakegraph.Server {
	t.Helper()
	opts := fakegraph.Options{Model: resolverModel(), Clock: fakeClock()}
	for _, m := range mutate {
		m(&opts)
	}
	return fakegraph.New(t, opts)
}

// newGraphClient builds the real internal/graph client against the fake, with
// retries off and a no-op sleeper, so a test never waits.
func newGraphClient(t *testing.T, srv *fakegraph.Server) *graph.Client {
	t.Helper()
	client, err := graph.New(graph.Options{
		BaseURL:    srv.URL(),
		HTTPClient: srv.Client(),
		Token:      graph.TokenSourceFunc(func(context.Context) (string, error) { return "fakegraph-token", nil }),
		Sleeper:    func(context.Context, time.Duration) error { return nil },
		MaxRetries: -1,
	})
	if err != nil {
		t.Fatalf("graph.New: %v", err)
	}
	return client
}

// newTestCache builds the entity cache the way the CLI does: loaded from a
// per-test path and stamped by the injected clock (internal/cli/read.go:35-45).
func newTestCache(t *testing.T) *store.EntityCache {
	t.Helper()
	cache, err := store.LoadEntities(t.TempDir() + "/entities.json")
	if err != nil {
		t.Fatalf("store.LoadEntities: %v", err)
	}
	cache.SetClock(fakeClock().Now)
	return cache
}

// newTestResolver wires a Resolver against a fresh fakegraph server, entity cache
// and frozen clock. mutate changes the ResolverOptions, which is how a test
// installs a Chooser or an alias table.
func newTestResolver(t *testing.T, mutate ...func(*ResolverOptions)) (*Resolver, *fakegraph.Server) {
	t.Helper()
	srv := newFakeGraphServer(t)
	opts := ResolverOptions{
		Client:  newGraphClient(t, srv),
		Cache:   newTestCache(t),
		Aliases: map[string]string{},
		Now:     fakeClock().Now,
	}
	for _, m := range mutate {
		m(&opts)
	}
	return NewResolver(opts), srv
}

// requestsFor returns the recorded requests with exactly this method and Graph
// path, whatever their query string was.
func requestsFor(srv *fakegraph.Server, method, path string) []*fakegraph.RecordedRequest {
	return srv.RequestsFor(method, path)
}

// requestCount counts the recorded requests with this method and Graph path.
func requestCount(srv *fakegraph.Server, method, path string) int {
	return len(srv.RequestsFor(method, path))
}

// cacheHasTeam reports whether the cache holds name -> id.
func cacheHasTeam(cache *store.EntityCache, name, id string) bool {
	got, ok := cache.Team(name)
	return ok && got == id
}

// cacheHasChannel reports whether the cache holds (team id, channel name) -> id.
func cacheHasChannel(cache *store.EntityCache, teamID, name, id string) bool {
	got, ok := cache.Channel(teamID, name)
	return ok && got == id
}

// assertErrorNames fails unless every name appears in the error message, which is
// how the ambiguity and not-found errors list their candidates.
func assertErrorNames(t *testing.T, err error, names ...string) {
	t.Helper()
	for _, name := range names {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not name %q", err.Error(), name)
		}
	}
}

// assertErrorHint fails unless every substring appears in the fix line the CLI
// prints under the error, which is where the resolver puts its guidance
// (internal/ref/errors.go:31-32).
func assertErrorHint(t *testing.T, err error, substrings ...string) {
	t.Helper()
	var refErr *Error
	if !errors.As(err, &refErr) {
		t.Fatalf("error %v (%T) carries no hint; want a *ref.Error", err, err)
	}
	for _, s := range substrings {
		if !strings.Contains(refErr.Hint(), s) {
			t.Fatalf("error hint = %q does not mention %q (message %q)", refErr.Hint(), s, refErr.Error())
		}
	}
}
