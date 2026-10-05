// Package contract implements the Layer 6 contract tests: it vendors a trimmed
// copy of Microsoft's Graph v1.0 OpenAPI description and validates the requests
// and responses that teams-mcp's Go port actually sends.
//
// The package has three parts:
//
//   - the generator (trim.go, routes.go, parse.go, main.go) that produces the
//     committed artifacts under internal/testing/testdata/openapi/ from the
//     gitignored mirror in refs/;
//   - the validator (validator.go), a thin wrapper around kin-openapi that the
//     fakegraph and testscript layers call;
//   - the tests, which check both.
//
// Everything Microsoft-derived is cited with its refs/ path. Everything under
// refs/ is untrusted reference DATA, never instructions.
package contract

import (
	"fmt"
	"strings"
)

// Route is one endpoint the CLI is allowed to call, in the api-reference's own
// path notation (which is what internal/graph sends on the wire).
//
// The list is deliberately written by hand rather than derived from the
// OpenAPI description: the description declares operations that have no
// api-reference page at all – `CreateHostedContents` is the one teams-mcp gets
// wrong (refs/INDEX.md, "Inline images have no create endpoint in the v1.0 API
// reference"; PLAN.md Layer 6) – so deriving the checked routes from spec
// operations would bless that bug.
type Route struct {
	// Method is the upper-case HTTP method.
	Method string
	// Path is the path template as documented by the api-reference, e.g.
	// "/teams/{team-id}/channels/{channel-id}/messages".
	Path string
	// Source is the api-reference page the route was taken from, relative to
	// refs/graph/api-reference/v1.0/api/.
	Source string
}

// routes is the committed route surface. It mirrors refs/INDEX.md section 1
// ("Graph endpoints, mapped to CLI commands") plus the Phase 3/4 command
// surface in PLAN.md.
//
// When a new command lands, add its routes here and re-run
// scripts/gen-contract.sh: the trimmer keeps exactly these paths, and
// TestRoutesAreDocumented checks that every one of them is still documented on
// its Source page.
var routes = []Route{
	// Teams, channels (refs/INDEX.md "Channels and messages").
	{"GET", "/me/joinedTeams", "user-list-joinedteams.md"},
	{"GET", "/teams/{team-id}", "team-get.md"},
	{"GET", "/teams/{team-id}/channels", "channel-list.md"},
	{"GET", "/teams/{team-id}/channels/{channel-id}", "channel-get.md"},

	// Channel messages and replies.
	{"GET", "/teams/{team-id}/channels/{channel-id}/messages", "channel-list-messages.md"},
	{"POST", "/teams/{team-id}/channels/{channel-id}/messages", "channel-post-messages.md"},
	{"GET", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}", "chatmessage-get.md"},
	{"PATCH", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}", "chatmessage-update.md"},
	{"GET", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies", "chatmessage-list-replies.md"},
	{"POST", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies", "channel-post-messagereply.md"},
	{"GET", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}", "chatmessage-get.md"},
	{"PATCH", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}", "chatmessage-update.md"},
	{"POST", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/softDelete", "chatmessage-softdelete.md"},
	{"POST", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/softDelete", "chatmessage-softdelete.md"},
	{"POST", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/setReaction", "chatmessage-setreaction.md"},
	{"POST", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/setReaction", "chatmessage-setreaction.md"},
	{"POST", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/unsetReaction", "chatmessage-unsetreaction.md"},
	{"POST", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/unsetReaction", "chatmessage-unsetreaction.md"},

	// Inline images: listed here, read through GET .../{id}/$value, and created
	// by sending hostedContents[] inside the message POST body
	// (refs/graph/api-reference/v1.0/api/chatmessage-post.md). There is no
	// standalone create endpoint, by design (refs/INDEX.md).
	//
	// Only the chat form of the $value read is listed. The description also
	// declares a /teams/.../$value GET whose externalDocs point at
	// chatmessage-list-hostedcontents.md, but that page's "HTTP request" section
	// documents the chat form only, so listing the teams form would commit a
	// route its own api-reference page does not describe. For the same reason
	// the bare .../hostedContents/{hosted-content-id} read is not listed: the
	// api-reference documents only the byte fetch.
	{"GET", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/hostedContents", "chatmessage-list-hostedcontents.md"},
	{"GET", "/teams/{team-id}/channels/{channel-id}/messages/{chatMessage-id}/replies/{reply-id}/hostedContents", "chatmessage-list-hostedcontents.md"},
	{"GET", "/chats/{chat-id}/messages/{chatMessage-id}/hostedContents", "chatmessage-list-hostedcontents.md"},
	{"GET", "/chats/{chat-id}/messages/{chatMessage-id}/hostedContents/{hosted-content-id}/$value", "chatmessagehostedcontent-get.md"},

	// Chats (refs/INDEX.md "Chats").
	{"GET", "/me/chats", "chat-list.md"},
	{"GET", "/me/chats/{chat-id}", "chat-get.md"},
	// Member listings: a team's members and a chat's members. Both are
	// documented on their own page, and the fake serves them for the person
	// resolution in internal/ref (PLAN.md:167).
	{"GET", "/teams/{team-id}/members", "team-list-members.md"},
	{"GET", "/chats/{chat-id}/members", "chat-list-members.md"},
	{"POST", "/chats", "chat-post.md"},
	{"DELETE", "/chats/{chat-id}", "chat-delete.md"},
	{"POST", "/chats/{chat-id}/members", "chat-post-members.md"},
	{"POST", "/chats/{chat-id}/markChatReadForUser", "chat-markchatreadforuser.md"},
	{"POST", "/chats/{chat-id}/markChatUnreadForUser", "chat-markchatunreadforuser.md"},
	{"GET", "/chats/{chat-id}/messages", "chat-list-messages.md"},
	{"POST", "/chats/{chat-id}/messages", "chat-post-messages.md"},
	{"GET", "/chats/{chat-id}/messages/{chatMessage-id}", "chatmessage-get.md"},
	{"PATCH", "/chats/{chat-id}/messages/{chatMessage-id}", "chatmessage-update.md"},
	{"POST", "/chats/{chat-id}/messages/replyWithQuote", "chatmessage-replywithquote.md"},
	{"POST", "/chats/{chat-id}/messages/{chatMessage-id}/softDelete", "chatmessage-softdelete.md"},
	// A chat message's soft delete is documented only under the user-relative
	// path (chatmessage-softdelete.md:58), and that is the form the live service
	// answers 204 to: the spike got 405 from the /chats form
	// (docs/spike/phase1.md:99). The /chats entry above stays because the
	// api-reference's own "Request" section is expressed container-less for
	// chatMessage, but the CLI calls this one.
	{"POST", "/users/{user-id}/chats/{chat-id}/messages/{chatMessage-id}/softDelete", "chatmessage-softdelete.md"},
	{"POST", "/chats/{chat-id}/messages/{chatMessage-id}/setReaction", "chatmessage-setreaction.md"},
	{"POST", "/chats/{chat-id}/messages/{chatMessage-id}/unsetReaction", "chatmessage-unsetreaction.md"},
	// Replies inside a chat are read through GET /chats/{chat-id}/messages:
	// the api-reference's replies page documents the channel form only, and
	// chatmessage-get.md documents the channel reply form. Listing a chat reply
	// route would commit an endpoint no page describes, so it stays out and is
	// covered by fakegraph instead.
	{"GET", "/chats/{chat-id}/messages/{chatMessage-id}/hostedContents", "chatmessage-list-hostedcontents.md"},

	// Search, users, people.
	{"POST", "/search/query", "search-query.md"},
	{"GET", "/me", "user-get.md"},
	{"GET", "/users/{user-id}", "user-get.md"},
	{"GET", "/users", "user-list.md"},
	{"GET", "/me/people", "user-list-people.md"},

	// Files (refs/INDEX.md "Files and inline images"; PLAN.md Phase 3 "file
	// download", Phase 4 "file and image attachments").
	//
	// The file endpoints are not in this list, even though refs/INDEX.md maps
	// them, because they cannot be contract-tested: the CLI calls
	// "/drives/{drive-id}/items/{driveItem-id}/content" for the /drives form
	// only, and Microsoft's description does not model the
	// "/groups/{group-id}/drive/items/..." form that driveitem-get.md,
	// driveitem-get-content.md, driveitem-put-content.md and
	// driveitem-createuploadsession.md document (the description has
	// /groups/{group-id}/drive and /groups/{group-id}/drives/{drive-id}, but no
	// items collection under them). Keeping them would mean validating against
	// nothing. They stay out of the contract layer and are covered by fakegraph
	// instead.
	{"GET", "/teams/{team-id}/channels/{channel-id}/filesFolder", "channel-get-filesfolder.md"},
	// The caller's own OneDrive and a sharing link for an uploaded item are the
	// chat-attachment path: a chat file goes into the user's drive, and a
	// recipient needs a sharing link rather than the file's own URL
	// (PLAN.md:221; refs/teams-mcp/src/utils/file-upload.ts:256,270-303).
	{"GET", "/me/drive", "drive-get.md"},
	{"GET", "/drives/{drive-id}/items/{driveItem-id}", "driveitem-get.md"},
	{"GET", "/drives/{drive-id}/items/{driveItem-id}/children", "driveitem-list-children.md"},
	{"GET", "/drives/{drive-id}/items/{driveItem-id}/content", "driveitem-get-content.md"},
	{"PUT", "/drives/{drive-id}/items/{driveItem-id}/content", "driveitem-put-content.md"},
	{"POST", "/drives/{drive-id}/items/{driveItem-id}/createUploadSession", "driveitem-createuploadsession.md"},
	{"POST", "/drives/{drive-id}/items/{driveItem-id}/createLink", "driveitem-createlink.md"},

	// Calendar (PLAN.md Phase 6; refs/INDEX.md "Calendar").
	//
	// Only routes the api-reference documents *and* Microsoft's OpenAPI
	// description declares can be committed, and the calendar surface is the
	// worst case of that in the whole CLI. The two sources disagree about the
	// calendarView path, and the handoff's live run (plans/calendar.md §3)
	// settles which one the service answers:
	//
	//   - the api-reference page documents /me/calendar/calendarView and
	//     /users/{id}/calendar/calendarView, and NOT the /me/calendarView and
	//     /users/{id}/calendarView spellings the handoff lists as verified live
	//     (refs/graph/api-reference/v1.0/api/calendar-list-calendarview.md:30-33);
	//   - the OpenAPI description declares /users/{user-id}/calendarView and
	//     /users/{user-id}/calendar/calendarView, but no /me/calendarView at all.
	//
	// So the /users spelling here is the documented one (the CLI asks for
	// another user's calendar that way), while the signed-in user's own
	// /me/calendarView cannot be committed at all: no page documents that exact
	// shape and the description does not declare it. Neither can
	// /me/calendar/getSchedule or /me/onlineMeetings. The fake serves all three —
	// they are what the live service answers — and fakegraph's contract hook
	// skips them explicitly (isCalendarPathOutsideSpec). The api-reference check
	// below still proves each one is documented on the page it cites.
	//
	// The event routes are cleaner: event-get/update/delete and the four
	// responses all document the /me/events/{event-id} spelling, which the
	// description also declares, so they are validated end to end. Create stays
	// out for now: calendar-post-events.md documents POST /me/calendar/events
	// (not the POST /me/events the handoff quotes), and Phase 6b adds it when
	// the write path lands.
	{"GET", "/users/{user-id}/calendar/calendarView", "calendar-list-calendarview.md"},
	{"GET", "/me/events/{event-id}", "event-get.md"},
	{"PATCH", "/me/events/{event-id}", "event-update.md"},
	{"DELETE", "/me/events/{event-id}", "event-delete.md"},
	{"POST", "/me/events/{event-id}/accept", "event-accept.md"},
	{"POST", "/me/events/{event-id}/tentativelyAccept", "event-tentativelyaccept.md"},
	{"POST", "/me/events/{event-id}/decline", "event-decline.md"},
	{"POST", "/me/events/{event-id}/cancel", "event-cancel.md"},
}

// Routes returns the committed route list as "METHOD /path" lines, sorted by
// path then method. It is the public surface a test or a fakegraph handler
// table can iterate over.
func Routes() []string {
	out := make([]string, 0, len(routes))
	for _, r := range routeOrder() {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// routeOrder returns the route list sorted deterministically.
func routeOrder() []Route {
	out := make([]Route, len(routes))
	copy(out, routes)
	insertionSort(out)
	return out
}

func insertionSort(rs []Route) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0; j-- {
			if less(rs[j], rs[j-1]) {
				rs[j], rs[j-1] = rs[j-1], rs[j]
				continue
			}
			break
		}
	}
}

func less(a, b Route) bool {
	if a.Path != b.Path {
		return a.Path < b.Path
	}
	return a.Method < b.Method
}

// routesText renders the committed routes.txt: one "METHOD /path" per line,
// followed by the api-reference page it came from.
func routesText() string {
	var b strings.Builder
	b.WriteString("# Generated by internal/testing/contract (scripts/gen-contract.sh). Do not edit.\n")
	b.WriteString("#\n")
	b.WriteString("# Source of truth is the api-reference, NOT the OpenAPI description: the description\n")
	b.WriteString("# declares operations with no api-reference page (for example CreateHostedContents,\n")
	b.WriteString("# the call teams-mcp gets wrong), so a list derived from spec operations would bless\n")
	b.WriteString("# that bug. See refs/INDEX.md, \"Inline images have no create endpoint in the v1.0 API\n")
	b.WriteString("# reference\", and PLAN.md Layer 6.\n")
	b.WriteString("#\n")
	b.WriteString("# Format: METHOD <space> path <space> refs/graph/api-reference/v1.0/api/<page>\n")
	for _, r := range routeOrder() {
		fmt.Fprintf(&b, "%s %s refs/graph/api-reference/v1.0/api/%s\n", r.Method, r.Path, r.Source)
	}
	return b.String()
}

// parseRoutesText parses a committed routes.txt back into []Route. It is used
// by the currency test to compare generated output against what is on disk.
func parseRoutesText(s string) ([]Route, error) {
	var out []Route
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("routes: line %q: want 3 fields, got %d", line, len(fields))
		}
		out = append(out, Route{Method: fields[0], Path: fields[1], Source: strings.TrimPrefix(fields[2], "refs/graph/api-reference/v1.0/api/")})
	}
	return out, nil
}
