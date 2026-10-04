package graph

import (
	"context"
	"net/url"
)

// This file wraps the team and channel reads of PLAN.md Phase 3
// ("team/channel list", "channel show"). Every route is in the Layer 6 route
// list (internal/testing/contract/routes.go) and documented by the api-reference
// page named on the wrapper.

// ListJoinedTeams returns the teams the signed-in user is a member of
// (GET /me/joinedTeams, refs/graph/api-reference/v1.0/api/user-list-joinedteams.md).
//
// No query parameters. The page says "This method doesn't currently support the
// OData query parameters to customize the response"
// (user-list-joinedteams.md:39), and a live tenant answers 400 "Query option
// 'Top' is not allowed" for exactly that. Paging still follows @odata.nextLink,
// which is what PLAN.md:230 requires.
func (c *Client) ListJoinedTeams(ctx context.Context) ([]Team, error) {
	return ListAll[Team](ctx, c, "/me/joinedTeams", nil, 0)
}

// GetTeam returns one team (GET /teams/{team-id},
// refs/graph/api-reference/v1.0/api/team-get.md).
func (c *Client) GetTeam(ctx context.Context, teamID string) (Team, error) {
	var team Team
	err := c.Get(ctx, "/teams/"+segment(teamID), &team)
	return team, err
}

// ListChannels returns a team's channels (GET /teams/{team-id}/channels,
// refs/graph/api-reference/v1.0/api/channel-list.md).
func (c *Client) ListChannels(ctx context.Context, teamID string) ([]Channel, error) {
	return ListAll[Channel](ctx, c, "/teams/"+segment(teamID)+"/channels", nil, 0)
}

// GetChannel returns one channel (GET /teams/{team-id}/channels/{channel-id},
// refs/graph/api-reference/v1.0/api/channel-get.md).
func (c *Client) GetChannel(ctx context.Context, teamID, channelID string) (Channel, error) {
	var channel Channel
	err := c.Get(ctx, "/teams/"+segment(teamID)+"/channels/"+segment(channelID), &channel)
	return channel, err
}

// ListTeamMembers returns a team's members. The docs imply no paging here, but
// the spike saw @odata.nextLink at $top=5, so the shared pager is used and is
// harmless when no link comes back (PLAN.md:230, docs/spike/phase1.md:50). The
// documented default and maximum page sizes are 100 and 999
// (refs/graph/api-reference/v1.0/api/team-list-members.md:45).
func (c *Client) ListTeamMembers(ctx context.Context, teamID string) ([]ConversationMember, error) {
	q := url.Values{"$top": {intParam(DefaultTopMembers)}}
	return ListAll[ConversationMember](ctx, c, "/teams/"+segment(teamID)+"/members", q, 0)
}
