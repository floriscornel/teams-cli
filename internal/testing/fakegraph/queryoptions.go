package fakegraph

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// This file is the query-option gate: the api-reference's answer to "which OData
// query options does this route accept?". A request that carries an option the
// route does not document is refused with the wording the live service uses, so a
// caller can never believe an option was applied when Graph would have refused it.
//
// It exists because $top=999 on /me/joinedTeams reached a live tenant: the page
// for that route says "This method doesn't currently support the OData query
// parameters", the tenant answered 400 "Query option 'Top' is not allowed", and
// nothing in the suite noticed - the OpenAPI description declares $top there (so
// the Layer 6 contract check blessed it) and the fake policed only the six options
// the spike had seen refused. Every row below is therefore an explicit decision,
// cited to the page it came from, and TestEveryRouteDeclaresItsQueryOptions fails
// when a route has no row.
//
// $skiptoken and $skip are always allowed: a next link the fake (or Graph)
// generated carries them, and following a next link verbatim is required
// (refs/graph/concepts/paging.md:91).
var documentedQueryOptions = map[string][]string{
	// Identity and teams.
	"GET /me":                      {"$select"},
	"GET /me/joinedTeams":          {},
	"GET /teams/{team-id}":         {"$select"},
	"GET /teams/{team-id}/members": {"$filter", "$select", "$top"},

	// Channels (refs/graph/api-reference/v1.0/api/channel-list.md:39,
	// channel-get.md, channel-list-members.md).
	"GET /teams/{team-id}/channels":                      {"$filter", "$select"},
	"GET /teams/{team-id}/channels/{channel-id}":         {"$filter", "$select"},
	"GET /teams/{team-id}/channels/{channel-id}/members": {"$filter", "$select", "$top"},

	// Channel messages: only $top (default 20, max 50) and $expand
	// (channel-list-messages.md:33-38); $filter is a 400, not a silent ignore.
	"GET /teams/{team-id}/channels/{channel-id}/messages":                                                     {"$top", "$expand"},
	"POST /teams/{team-id}/channels/{channel-id}/messages":                                                    {},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}":                                    {},
	"PATCH /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}":                                  {},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies":                            {"$top"},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies":                           {},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}":                 {},
	"PATCH /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}":               {},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/softDelete":                        {},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/softDelete":     {},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/setReaction":                       {},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/unsetReaction":                     {},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/setReaction":    {},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/unsetReaction":  {},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/hostedContents":                    {},
	"POST /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/hostedContents": {},

	// Inline images: "This operation doesn't support the OData query parameters"
	// (chatmessage-list-hostedcontents.md, chatmessagehostedcontent-get.md).
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/hostedContents":                                               {},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/hostedContents":                            {},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/hostedContents/{hosted-content-id}/$value":                    {},
	"GET /teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/hostedContents/{hosted-content-id}/$value": {},

	// Search, users, people.
	"POST /search/query":   {},
	"GET /users":           {"$count", "$expand", "$filter", "$orderby", "$search", "$select", "$top"},
	"GET /users/{user-id}": {"$select", "$expand"},
	"GET /me/people":       {"$filter", "$orderby", "$search", "$select", "$skip", "$top"},

	// Chats, keyed by the /chats form (chat-list.md:16-33, chat-get.md,
	// chat-list-members.md, chat-list-messages.md:32-34, chatmessage-get.md,
	// chatmessage-update.md, chatmessage-replywithquote.md, chatmessage-softdelete.md,
	// chatmessage-setreaction.md, chat-post.md, chat-post-members.md, chat-delete.md,
	// chat-markchatreadforuser.md, chat-markchatunreadforuser.md,
	// chatmessage-list-hostedcontents.md).
	"GET /chats":                                                                               {"$expand", "$top", "$filter", "$orderby"},
	"GET /chats/{chat-id}":                                                                     {"$select", "$expand"},
	"GET /chats/{chat-id}/members":                                                             {},
	"POST /chats/{chat-id}/members":                                                            {},
	"GET /chats/{chat-id}/messages":                                                            {"$top", "$orderby", "$filter"},
	"POST /chats/{chat-id}/messages":                                                           {},
	"POST /chats/{chat-id}/messages/replyWithQuote":                                            {},
	"GET /chats/{chat-id}/messages/{chatMessage-id}":                                           {},
	"PATCH /chats/{chat-id}/messages/{chatMessage-id}":                                         {},
	"POST /chats/{chat-id}/messages/{chatMessage-id}/softDelete":                               {},
	"POST /chats/{chat-id}/messages/{chatMessage-id}/setReaction":                              {},
	"POST /chats/{chat-id}/messages/{chatMessage-id}/unsetReaction":                            {},
	"GET /chats/{chat-id}/messages/{chatMessage-id}/hostedContents":                            {},
	"GET /chats/{chat-id}/messages/{chatMessage-id}/hostedContents/{hosted-content-id}/$value": {},
	"POST /chats/{chat-id}/messages/{chatMessage-id}/hostedContents":                           {},
	"POST /chats/{chat-id}/markChatReadForUser":                                                {},
	"POST /chats/{chat-id}/markChatUnreadForUser":                                              {},
	"POST /chats":             {},
	"DELETE /chats/{chat-id}": {},

	// Chats (chat-list.md:16-33, chat-get.md, chat-list-members.md,
	// chat-list-messages.md:32-34, chatmessage-get.md, chatmessage-update.md,
	// chatmessage-replywithquote.md, chatmessage-softdelete.md, chatmessage-setreaction.md,
	// chat-post.md, chat-post-members.md, chat-delete.md,
	// chat-markchatreadforuser.md, chat-markchatunreadforuser.md).
	"POST /chats/{chat-id}/softDelete":                 {},
	"POST /users/{user-id}/chats/{chat-id}/softDelete": {},

	// Files.
	"GET /teams/{team-id}/channels/{channel-id}/filesFolder":           {},
	"GET /drives/{drive-id}/items/{driveItem-id}":                      {"$expand", "$select"},
	"GET /drives/{drive-id}/items/{driveItem-id}/children":             {"$expand", "$select", "$top", "$orderby", "$skipToken"},
	"GET /drives/{drive-id}/items/{driveItem-id}/content":              {"$format"},
	"PUT /drives/{drive-id}/items/{driveItem-id}/content":              {},
	"POST /drives/{drive-id}/items/{driveItem-id}/createUploadSession": {},
	"POST /drives/{drive-id}/items/{driveItem-id}/createLink":          {},
	"GET /me/drive": {"$select"},

	// Calendar (PLAN.md Phase 6; refs/INDEX.md "Calendar"). calendarView takes
	// the two required window parameters plus $top, $select, $orderby and
	// $filter — $orderby=start/dateTime and $filter=isAllDay eq true both worked
	// live (plans/calendar.md §3, F2); the CLI sorts client-side anyway.
	"GET /me/calendarView":                       {"startDateTime", "endDateTime", "$top", "$select", "$orderby", "$filter"},
	"GET /users/{user-id}/calendar/calendarView": {"startDateTime", "endDateTime", "$top", "$select", "$orderby", "$filter"},
	"GET /users/{user-id}/calendarView":          {"startDateTime", "endDateTime", "$top", "$select", "$orderby", "$filter"},
	"POST /me/calendar/getSchedule":              {},
	"GET /me/events/{event-id}":                  {"$select", "$expand"},
	"GET /me/onlineMeetings":                     {"$filter", "$select", "$top", "$orderby", "$expand"},
	"GET /me/calendar":                           {"$select", "$expand"},
	// The colon-addressed upload routes take no query options. They are
	// fake-only (see routes.go): the api-reference documents the path form, and
	// Microsoft's OpenAPI description does not model it at all.
	"PUT /drives/{drive-id}/items/{parent-ref}:/{file-name}:/content":                            {},
	"PUT /drives/{drive-id}/items/{parent-ref}:/{folder-name}/{file-name}:/content":              {},
	"POST /drives/{drive-id}/items/{parent-ref}:/{file-name}:/createUploadSession":               {},
	"POST /drives/{drive-id}/items/{parent-ref}:/{folder-name}/{file-name}:/createUploadSession": {},

	// $batch takes no query options (json-batching.md).
	"POST /$batch": {},
}

// normalizeChatPattern rewrites the /me/chats and /users/{user-id}/chats forms to
// the /chats form the table is keyed by, so the chat family (which is registered
// under three prefixes) needs one row per route rather than three, and no row can
// be skipped by accident.
func normalizeChatPattern(pattern string) string {
	switch {
	case strings.HasPrefix(pattern, "/me/chats"):
		return strings.TrimPrefix(pattern, "/me")
	case strings.HasPrefix(pattern, "/users/{user-id}/chats"):
		return strings.TrimPrefix(pattern, "/users/{user-id}")
	default:
		return pattern
	}
}

// rejectUndeclaredQueryOptions refuses a request that carries a $ option the route
// does not document. The wording separates the two validation layers Graph uses,
// both observed live: the OData attribute layer answers "Parameter 'Filter' not
// supported" (docs/spike/phase1.md:53) and the query-option policy answers
// "Query option 'Top' is not allowed." (the 400 a tenant returned for $top on
// /me/joinedTeams).
func rejectUndeclaredQueryOptions(route routeDef, q url.Values) *apiError {
	allowed := documentedQueryOptions[route.method+" "+normalizeChatPattern(route.pattern)]
	// $skiptoken is how a next link expresses the offset, so a caller following
	// one verbatim always sends it (refs/graph/concepts/paging.md:91). $skip is a
	// query option a caller chooses, and only the routes that document it accept
	// it (user-list.md says $skip is not supported on /users).
	ok := map[string]bool{"$skiptoken": true}
	for _, name := range allowed {
		ok[name] = true
	}
	names := make([]string, 0, len(q))
	for name := range q {
		if strings.HasPrefix(name, "$") && !ok[name] {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	// Sorted, so a request with several offenders always names the same one.
	sort.Strings(names)
	return unsupportedQueryOption(names[0])
}

// unsupportedQueryOption renders the live error for one refused option.
func unsupportedQueryOption(name string) *apiError {
	label := strings.TrimPrefix(name, "$")
	pretty := strings.ToUpper(label[:1]) + label[1:]
	if _, known := spikeRefusedOptions[name]; known {
		return badRequestf("Parameter '%s' not supported", pretty)
	}
	return badRequestf("Query option '%s' is not allowed.", pretty)
}

// spikeRefusedOptions are the options the live service refused in the way
// docs/spike/phase1.md:52-53 recorded, which is the wording those refusals keep.
var spikeRefusedOptions = map[string]struct{}{
	"$filter": {}, "$orderby": {}, "$search": {}, "$count": {}, "$select": {}, "$expand": {},
}

// queryOptionsFor reports the documented options of a route, for tests and for the
// completeness check. It is exported to the package's own tests only.
func queryOptionsFor(method, pattern string) ([]string, bool) {
	options, ok := documentedQueryOptions[method+" "+normalizeChatPattern(pattern)]
	return options, ok
}

// describeRoute renders a route for an error message.
func describeRoute(route routeDef) string {
	return fmt.Sprintf("%s %s", route.method, route.pattern)
}
