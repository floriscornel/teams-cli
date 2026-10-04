package graph

import (
	"context"
	"net/url"
	"strings"
)

// This file wraps the people and directory reads of PLAN.md Phase 3
// ("user search/show" and the person resolution behind @name).
//
// The resolution order PLAN.md:167 prescribes is: alias, entity cache, the
// members of the user's chats and teams, GET /me/people, then GET /users with a
// startswith filter. The first two live in internal/store and internal/ref;
// this file provides the last two.

// GetUser returns one user by id, user principal name or mail
// (GET /users/{user-id}, refs/graph/api-reference/v1.0/api/user-get.md).
func (c *Client) GetUser(ctx context.Context, idOrUPN string) (User, error) {
	var user User
	err := c.Get(ctx, "/users/"+segment(idOrUPN), &user)
	return user, err
}

// UserFilterDisplayName builds the documented startswith filter on displayName.
// The literal is escaped by doubling single quotes
// (refs/graph/concepts/query-parameters.md, "Filter parameter").
func UserFilterDisplayName(prefix string) string {
	return "startswith(displayName," + ODataQuote(prefix) + ")"
}

// UserFilterUserPrincipalName builds the startswith filter on userPrincipalName.
func UserFilterUserPrincipalName(prefix string) string {
	return "startswith(userPrincipalName," + ODataQuote(prefix) + ")"
}

// UserFilterMail builds the startswith filter on mail.
func UserFilterMail(prefix string) string {
	return "startswith(mail," + ODataQuote(prefix) + ")"
}

// ListUsers returns directory users matching a raw OData $filter
// (GET /users, refs/graph/api-reference/v1.0/api/user-list.md). limit of 0
// pages everything.
func (c *Client) ListUsers(ctx context.Context, filter string, limit int) ([]User, error) {
	q := url.Values{"$top": {intParam(topValue(0, DefaultTopMembers, MaxTopMembers))}}
	if filter != "" {
		q.Set("$filter", filter)
	}
	return ListAll[User](ctx, c, "/users", q, limit)
}

// SearchUsers runs the two-step directory search the CLI exposes as
// "teams user search": a startswith filter on displayName first, then on
// userPrincipalName and mail when the first attempt found nothing. Graph
// supports one property per startswith filter and OR-combining them is not
// universally accepted, so the fallback is explicit rather than clever.
func (c *Client) SearchUsers(ctx context.Context, query string, limit int) ([]User, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, usageError("graph: user search needs a query")
	}
	filters := []string{
		UserFilterDisplayName(query),
		UserFilterUserPrincipalName(query),
		UserFilterMail(query),
	}
	var out []User
	for _, filter := range filters {
		users, err := c.ListUsers(ctx, filter, limit)
		if err != nil {
			return nil, err
		}
		if len(users) > 0 {
			return users, nil
		}
		out = users
	}
	return out, nil
}

// ListPeople returns relevance-ranked people (GET /me/people,
// refs/graph/api-reference/v1.0/api/user-list-people.md). The $search value is
// quoted because that is the documented form, and it is a fuzzy match, which is
// what makes "@yuki" mean the Yuki you work with.
func (c *Client) ListPeople(ctx context.Context, search string, limit int) ([]Person, error) {
	q := url.Values{"$top": {intParam(topValue(0, DefaultTopPeople, MaxTopPeople))}}
	if s := strings.TrimSpace(search); s != "" {
		q.Set("$search", "\""+strings.ReplaceAll(s, "\"", "")+"\"")
	}
	return ListAll[Person](ctx, c, "/me/people", q, limit)
}
