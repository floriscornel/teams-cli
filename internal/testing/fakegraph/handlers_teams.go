package fakegraph

import (
	"net/http"
)

// This file serves the team and channel endpoints. Their shapes and query
// limits come from refs/graph/api-reference/v1.0/api/{team-get,team-list-members,
// channel-list,channel-get,channel-list-members}.md.

// handleMe serves GET /me. The endpoint needs User.Read, which reaches only the
// signed-in user (refs/INDEX.md; docs/spike/phase1.md:67).
func handleMe(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	me := st.users[st.me]
	if me == nil {
		c.fail(notFoundf("The signed-in user is not seeded."))
		return
	}
	c.json(http.StatusOK, st.renderUser(me))
}

// handleJoinedTeams serves GET /me/joinedTeams. The list pages: the collection
// uses the directory window of 100 per page with a 999 maximum, and the spike
// confirmed that Teams collections do hand back @odata.nextLink
// (docs/spike/phase1.md:49-50).
func handleJoinedTeams(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()

	p, perr := parsePage(c.query, defaultTopMembers, maxTopMembers)
	if perr != nil {
		c.fail(perr)
		return
	}
	var all []teamWire
	for _, id := range st.teamOrder {
		t := st.teams[id]
		if !hasMember(t.members, st.me) {
			continue
		}
		all = append(all, st.renderTeam(t))
	}
	c.json(http.StatusOK, pageAndLink(c.s, c.rel, c.query, all, p))
}

// handleGetTeam serves GET /teams/{team-id}.
func handleGetTeam(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	t := st.teams[c.param("team-id")]
	if t == nil {
		c.fail(notFoundf("The team %q was not found.", c.param("team-id")))
		return
	}
	c.json(http.StatusOK, st.renderTeam(t))
}

// handleTeamMembers serves GET /teams/{team-id}/members. The documented page
// sizes are 100 default and 999 maximum, and — unlike what the earlier plan
// assumed — the collection does page: the spike saw @odata.nextLink at
// $top=5 (refs/graph/api-reference/v1.0/api/team-list-members.md:30;
// docs/spike/phase1.md:50).
func handleTeamMembers(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	t := st.teams[c.param("team-id")]
	if t == nil {
		c.fail(notFoundf("The team %q was not found.", c.param("team-id")))
		return
	}
	p, perr := parsePage(c.query, defaultTopMembers, maxTopMembers)
	if perr != nil {
		c.fail(perr)
		return
	}
	all := st.renderMembers(t.members)
	c.json(http.StatusOK, pageAndLink(c.s, c.rel, c.query, all, p))
}

// handleListChannels serves GET /teams/{team-id}/channels.
func handleListChannels(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	t := st.teams[c.param("team-id")]
	if t == nil {
		c.fail(notFoundf("The team %q was not found.", c.param("team-id")))
		return
	}
	p, perr := parsePage(c.query, defaultTopMembers, maxTopMembers)
	if perr != nil {
		c.fail(perr)
		return
	}
	all := make([]channelWire, 0, len(t.channelOrder))
	for _, id := range t.channelOrder {
		all = append(all, st.renderChannel(t.channels[id]))
	}
	c.json(http.StatusOK, pageAndLink(c.s, c.rel, c.query, all, p))
}

// handleGetChannel serves GET /teams/{team-id}/channels/{channel-id}. The CLI
// reads membershipType to tell standard, private and shared channels apart
// (refs/INDEX.md "teams channel show").
func handleGetChannel(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	t := st.teams[c.param("team-id")]
	if t == nil {
		c.fail(notFoundf("The team %q was not found.", c.param("team-id")))
		return
	}
	ch := t.channels[c.param("channel-id")]
	if ch == nil {
		c.fail(notFoundf("The channel %q was not found.", c.param("channel-id")))
		return
	}
	c.json(http.StatusOK, st.renderChannel(ch))
}

// handleChannelMembers serves GET /teams/{team-id}/channels/{channel-id}/members
// with the same 100/999 window as team members, because the api-reference gives
// channel members the same page sizes
// (refs/graph/api-reference/v1.0/api/channel-list-members.md:29).
func handleChannelMembers(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	ch, ok := st.channel(c.param("team-id"), c.param("channel-id"))
	if !ok {
		c.fail(notFoundf("The channel %q was not found.", c.param("channel-id")))
		return
	}
	p, perr := parsePage(c.query, defaultTopMembers, maxTopMembers)
	if perr != nil {
		c.fail(perr)
		return
	}
	all := st.renderMembers(ch.members)
	c.json(http.StatusOK, pageAndLink(c.s, c.rel, c.query, all, p))
}

// renderTeam converts a team record to its wire shape.
func (s *store) renderTeam(t *teamRec) teamWire {
	return teamWire{
		ID:              t.id,
		DisplayName:     t.displayName,
		Description:     t.description,
		Visibility:      t.visibility,
		CreatedDateTime: graphTime(t.created),
		WebURL:          "https://teams.microsoft.com/l/team/" + t.id + "/conversations",
		TenantID:        s.tenant,
	}
}

// renderChannel converts a channel record to its wire shape.
func (s *store) renderChannel(ch *channelRec) channelWire {
	return channelWire{
		ID:              ch.id,
		DisplayName:     ch.displayName,
		Description:     ch.description,
		MembershipType:  ch.membershipType,
		CreatedDateTime: graphTime(ch.created),
		WebURL:          "https://teams.microsoft.com/l/channel/" + ch.id + "/" + ch.displayName,
	}
}

// channel resolves a channel and reports whether it exists.
func (s *store) channel(teamID, channelID string) (*channelRec, bool) {
	t := s.teams[teamID]
	if t == nil {
		return nil, false
	}
	ch := t.channels[channelID]
	if ch == nil {
		return nil, false
	}
	return ch, true
}

// hasMember reports whether a member list contains a user.
func hasMember(members []memberRec, userID string) bool {
	for _, m := range members {
		if m.userID == userID {
			return true
		}
	}
	return false
}
