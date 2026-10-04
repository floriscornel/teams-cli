package fakegraph

import (
	"net/http"
	"sort"
	"strings"
)

// This file serves /users, /users/{id} and /me/people.
//
// Two spike findings drive the shape:
//
//   - GET /users with a $filter and GET /users/{other} both answer 403 with
//     User.Read alone — "User.Read covers /me only" — which is why the CLI
//     needs User.ReadBasic.All (docs/spike/phase1.md:67);
//   - /me/people is "ordered by relevance … determined by the user's
//     communication and collaboration patterns" and supports a fuzzy $search
//     (refs/graph/api-reference/v1.0/api/user-list-people.md:12-16).

// handleListUsers serves GET /users. The documented filter surface is broad;
// the fake evaluates the eq and startswith forms the CLI builds, rejects a
// filter it cannot evaluate rather than silently returning everything, and
// escapes by doubling `'` as OData requires (PLAN.md "Escape `'` in OData
// $filter").
func handleListUsers(c *handlerCtx) {
	if err := rejectUnsupported(c.query, "$top", "$filter", "$search", "$select", "$orderby"); err != nil {
		c.fail(err)
		return
	}
	filter, ferr := parseUserFilter(c.query.Get("$filter"))
	if ferr != nil {
		c.fail(ferr)
		return
	}
	search := strings.Trim(c.query.Get("$search"), `"`)
	selects := parseSelect(c.query.Get("$select"))
	orderBy, oerr := parseUserOrderBy(c.query.Get("$orderby"))
	if oerr != nil {
		c.fail(oerr)
		return
	}
	p, perr := parsePage(c.query, defaultTopMembers, maxTopMembers)
	if perr != nil {
		c.fail(perr)
		return
	}

	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()

	all := make([]*userRec, 0, len(st.userOrder))
	for _, id := range st.userOrder {
		u := st.users[id]
		if !filter.matches(u) {
			continue
		}
		if search != "" && !userMatchesSearch(u, search) {
			continue
		}
		all = append(all, u)
	}
	switch orderBy {
	case "displayName":
		sort.SliceStable(all, func(i, j int) bool { return all[i].displayName < all[j].displayName })
	case "userPrincipalName":
		sort.SliceStable(all, func(i, j int) bool { return all[i].upn < all[j].upn })
	}
	items, next := window(all, p)
	out := make([]any, 0, len(items))
	for _, u := range items {
		out = append(out, st.renderUserSelected(u, selects))
	}
	c.json(http.StatusOK, pageOf(c.s, c.rel, c.query, out, next, len(all)))
}

// handleGetUser serves GET /users/{user-id|user-principal-name}.
func handleGetUser(c *handlerCtx) {
	key := c.param("user-id")
	if strings.EqualFold(key, "me") {
		handleMe(c)
		return
	}
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	u := st.lookupUser(key)
	if u == nil {
		c.fail(notFoundf("The user %q was not found.", key))
		return
	}
	selects := parseSelect(c.query.Get("$select"))
	c.json(http.StatusOK, st.renderUserSelected(u, selects))
}

// handlePeople serves GET /me/people. Results are relevance-ordered and support
// the documented fuzzy $search; $top defaults to 25 and pages
// (refs/graph/api-reference/v1.0/api/user-list-people.md:12-22).
func handlePeople(c *handlerCtx) {
	if err := rejectUnsupported(c.query, "$top", "$filter", "$search", "$select", "$orderby"); err != nil {
		c.fail(err)
		return
	}
	search := strings.Trim(c.query.Get("$search"), `"`)
	search = strings.TrimPrefix(strings.TrimSpace(search), "topic:")
	p, perr := parsePage(c.query, defaultTopPeople, maxTopMembers)
	if perr != nil {
		c.fail(perr)
		return
	}
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()

	all := make([]*userRec, 0, len(st.userOrder))
	for _, id := range st.userOrder {
		u := st.users[id]
		if u.id == st.me {
			// A person is somebody else; the signed-in user is not their own
			// relevant person.
			continue
		}
		if search != "" && !personMatchesSearch(u, search) {
			continue
		}
		all = append(all, u)
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].relevance != all[j].relevance {
			return all[i].relevance > all[j].relevance
		}
		return all[i].displayName < all[j].displayName
	})
	items, next := window(all, p)
	out := make([]personWire, 0, len(items))
	for _, u := range items {
		out = append(out, st.renderPerson(u))
	}
	c.json(http.StatusOK, pageOf(c.s, c.rel, c.query, out, next, len(all)))
}

// userFilter is the subset of OData `$filter` the fake evaluates.
type userFilter struct {
	prop  string
	op    string
	value string
}

// parseUserFilter parses "prop eq 'value'" and "startswith(prop,'value')".
func parseUserFilter(raw string) (userFilter, *apiError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return userFilter{}, nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "startswith(") && strings.HasSuffix(raw, ")") {
		inner := raw[len("startswith(") : len(raw)-1]
		prop, value, ok := splitTopLevelComma(inner)
		if !ok {
			return userFilter{}, badRequestf("The '$filter' value '%s' is not supported.", raw)
		}
		return userFilter{prop: strings.TrimSpace(prop), op: "startswith", value: unquoteOData(value)}, nil
	}
	fields := splitODataFields(raw)
	if len(fields) != 3 {
		return userFilter{}, badRequestf("The '$filter' value '%s' is not supported. Use \"<property> eq '<value>'\" or startswith(<property>,'<value>').", raw)
	}
	op := strings.ToLower(fields[1])
	if op != "eq" && op != "ne" {
		return userFilter{}, badRequestf("The '$filter' value '%s' is not supported. Only 'eq' and 'ne' are.", raw)
	}
	return userFilter{prop: fields[0], op: op, value: unquoteOData(fields[2])}, nil
}

func (f userFilter) matches(u *userRec) bool {
	if f.prop == "" {
		return true
	}
	value, ok := userProperty(u, f.prop)
	if !ok {
		// An unknown property makes the filter useless; the caller gets an
		// empty page rather than a silently unfiltered list.
		return false
	}
	switch f.op {
	case "eq":
		return strings.EqualFold(value, f.value)
	case "ne":
		return !strings.EqualFold(value, f.value)
	case "startswith":
		return strings.HasPrefix(strings.ToLower(value), strings.ToLower(f.value))
	default:
		return false
	}
}

// userProperty reads the filterable properties.
func userProperty(u *userRec, prop string) (string, bool) {
	switch prop {
	case "id":
		return u.id, true
	case "displayName":
		return u.displayName, true
	case "userPrincipalName":
		return u.upn, true
	case "mail":
		return u.mail, true
	case "givenName":
		return u.givenName, true
	case "surname":
		return u.surname, true
	case "jobTitle":
		return u.jobTitle, true
	case "department":
		return u.department, true
	default:
		return "", false
	}
}

// parseUserOrderBy accepts the two property names the CLI sorts by.
func parseUserOrderBy(raw string) (string, *apiError) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "", nil
	}
	prop := fields[0]
	if prop != "displayName" && prop != "userPrincipalName" {
		return "", badRequestf("The '$orderby' value '%s' is not supported.", raw)
	}
	return prop, nil
}

// parseSelect splits `$select` into property names.
func parseSelect(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// renderUserSelected returns the documented default property set, or just the
// requested properties when $select asked for them
// (refs/graph/api-reference/v1.0/api/user-list.md:39).
func (s *store) renderUserSelected(u *userRec, selects []string) any {
	full := s.renderUser(u)
	if len(selects) == 0 {
		return full
	}
	out := map[string]any{}
	for _, name := range selects {
		switch name {
		case "id":
			out["id"] = full.ID
		case "displayName":
			out["displayName"] = full.DisplayName
		case "givenName":
			out["givenName"] = full.GivenName
		case "surname":
			out["surname"] = full.Surname
		case "userPrincipalName":
			out["userPrincipalName"] = full.UserPrincipalName
		case "mail":
			out["mail"] = full.Mail
		case "jobTitle":
			out["jobTitle"] = full.JobTitle
		case "department":
			out["department"] = full.Department
		case "officeLocation":
			out["officeLocation"] = full.OfficeLocation
		case "preferredLanguage":
			out["preferredLanguage"] = full.PreferredLanguage
		case "mobilePhone":
			out["mobilePhone"] = full.MobilePhone
		case "businessPhones":
			out["businessPhones"] = full.BusinessPhones
		}
	}
	return out
}

// renderPerson converts a user record to a person object.
func (s *store) renderPerson(u *userRec) personWire {
	p := personWire{
		ID:                u.id,
		DisplayName:       u.displayName,
		GivenName:         u.givenName,
		Surname:           u.surname,
		JobTitle:          u.jobTitle,
		Department:        u.department,
		OfficeLocation:    u.officeLocation,
		UserPrincipalName: u.upn,
	}
	if u.mail != "" || u.upn != "" {
		p.ScoredEmailAddresses = []scoredEmailAddressWire{{
			Address:        firstNonEmpty(u.mail, u.upn),
			RelevanceScore: u.relevance,
		}}
	}
	return p
}

// userMatchesSearch implements the $search form, which is a substring match on
// the person's names and addresses.
func userMatchesSearch(u *userRec, search string) bool {
	term := strings.ToLower(search)
	for _, field := range []string{u.displayName, u.upn, u.mail, u.givenName, u.surname, u.jobTitle, u.department} {
		if field != "" && strings.Contains(strings.ToLower(field), term) {
			return true
		}
	}
	return false
}

// personMatchesSearch is userMatchesSearch plus the documented "displayName:"
// qualifier used by /me/people examples.
func personMatchesSearch(u *userRec, search string) bool {
	if prop, value, ok := strings.Cut(search, ":"); ok && !strings.Contains(prop, " ") {
		if strings.EqualFold(strings.TrimSpace(prop), "displayName") {
			return strings.Contains(strings.ToLower(u.displayName), strings.ToLower(strings.TrimSpace(value)))
		}
	}
	return userMatchesSearch(u, search)
}

// splitTopLevelComma splits "a,'b'" at the first comma outside quotes.
func splitTopLevelComma(s string) (string, string, bool) {
	inQuote := false
	for i, r := range s {
		switch r {
		case '\'':
			inQuote = !inQuote
		case ',':
			if !inQuote {
				return s[:i], s[i+1:], true
			}
		}
	}
	return "", "", false
}

// unquoteOData trims quotes and undoes the OData `”` escape, which the docs
// require and teams-mcp never does (PLAN.md "Escape `'` in OData `$filter`").
func unquoteOData(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "'")
	return strings.ReplaceAll(raw, "''", "'")
}
