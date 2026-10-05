package fakegraph

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// This file serves the Phase 6b calendar writes: create, update, the four
// responses, cancel and delete (plans/calendar.md §4.7).
//
// Every rule below was verified live on 2026-10-05 (handoff §3, F10) and is
// recorded in refs/INDEX.md:
//
//   - a repeated POST with the same transactionId returns the same event;
//   - isOnlineMeeting is silently ignored when the mailbox cannot create Teams
//     meetings, so a 201 comes back with no joinUrl;
//   - responding to your own event is a 400 ErrorInvalidRequest;
//   - proposedNewTime with sendResponse:false is a 400 ErrorInvalidParameter;
//   - cancel as an attendee is a 400 ErrorInvalidRequest;
//   - decline moves the event to Deleted Items, and delete returns 204 and then
//     404.

// errCodeInvalidParameter is the 400 a proposed time with no response gets.
const errCodeInvalidParameter = "ErrorInvalidParameter"

// eventCreateBody is the POST /me/calendar/events body.
type eventCreateBody struct {
	Subject               string                `json:"subject"`
	Start                 *dateTimeTimeZoneBody `json:"start"`
	End                   *dateTimeTimeZoneBody `json:"end"`
	IsAllDay              bool                  `json:"isAllDay"`
	Attendees             []attendeeBody        `json:"attendees"`
	Location              *locationBody         `json:"location"`
	Body                  *itemBodyBody         `json:"body"`
	IsOnlineMeeting       bool                  `json:"isOnlineMeeting"`
	OnlineMeetingProvider string                `json:"onlineMeetingProvider"`
	TransactionID         string                `json:"transactionId"`
}

// dateTimeTimeZoneBody is microsoft.graph.dateTimeTimeZone on the wire.
type dateTimeTimeZoneBody struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// attendeeBody is one attendee of a create.
type attendeeBody struct {
	EmailAddress *struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
	Type string `json:"type"`
}

// locationBody is microsoft.graph.location on the wire.
type locationBody struct {
	DisplayName string `json:"displayName"`
}

// itemBodyBody is microsoft.graph.itemBody on the wire.
type itemBodyBody struct {
	Content     string `json:"content"`
	ContentType string `json:"contentType"`
}

// eventUpdateBody is the PATCH body. Every field is a pointer so the fake can
// tell "not sent" from "sent empty", which is what the CLI promises.
type eventUpdateBody struct {
	Subject  *string               `json:"subject"`
	Start    *dateTimeTimeZoneBody `json:"start"`
	End      *dateTimeTimeZoneBody `json:"end"`
	Location *locationBody         `json:"location"`
	Body     *itemBodyBody         `json:"body"`
}

// respondBody is the accept/tentativelyAccept/decline body.
type respondBody struct {
	Comment         string `json:"comment"`
	SendResponse    *bool  `json:"sendResponse"`
	ProposedNewTime *struct {
		Start *dateTimeTimeZoneBody `json:"start"`
		End   *dateTimeTimeZoneBody `json:"end"`
	} `json:"proposedNewTime"`
}

// cancelBody is the cancel body.
type cancelBody struct {
	Comment string `json:"comment"`
}

// handleCreateEvent serves POST /me/calendar/events
// (refs/graph/api-reference/v1.0/api/calendar-post-events.md).
func handleCreateEvent(c *handlerCtx) {
	var body eventCreateBody
	if !c.decodeBody(&body) {
		return
	}
	if strings.TrimSpace(body.Subject) == "" {
		c.fail(badRequestf("The request body must contain a subject."))
		return
	}
	start, serr := body.dateTime(body.Start)
	if serr != nil {
		c.fail(serr)
		return
	}
	end, eerr := body.dateTime(body.End)
	if eerr != nil {
		c.fail(eerr)
		return
	}
	if end.Before(start) {
		c.fail(badRequestf("The end time must not be before the start time."))
		return
	}

	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()

	// A repeated create with the same transactionId returns the same event, which
	// is what makes the call safe to retry (plans/calendar.md §3, F10).
	if body.TransactionID != "" {
		for _, ev := range st.calByOwner[st.me] {
			if ev.transactionID == body.TransactionID {
				c.json(http.StatusOK, renderEvent(ev, nil))
				return
			}
		}
	}

	rec := &eventRec{
		id:             fmt.Sprintf("event-created-%d", len(st.calByOwner[st.me])+1),
		ownerID:        st.me,
		subject:        body.Subject,
		organizerName:  st.displayNameOf(st.me),
		isOrganizer:    true,
		allowProposals: true,
		showAs:         defaultShowAs,
		eventType:      defaultEventType,
		response:       "organizer",
		start:          start.UTC(),
		end:            end.UTC(),
		allDay:         body.IsAllDay,
		transactionID:  body.TransactionID,
		generated:      true,
	}
	if body.IsAllDay {
		rec.allDayStart = start.Format("2006-01-02")
		days := int(end.Sub(start).Hours() / 24)
		if days < 1 {
			days = 1
		}
		rec.allDayEnd = end.AddDate(0, 0, days).Format("2006-01-02")
		rec.start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
		rec.end = time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC)
	}
	if body.Location != nil {
		rec.location = body.Location.DisplayName
	}
	for _, attendee := range body.Attendees {
		if attendee.EmailAddress == nil {
			continue
		}
		name := attendee.EmailAddress.Name
		if name == "" {
			name = attendee.EmailAddress.Address
		}
		rec.attendees = append(rec.attendees, attendeeRecord{
			name: name, address: attendee.EmailAddress.Address, kind: attendee.Type, response: "notResponded",
		})
	}
	// isOnlineMeeting is silently ignored when the mailbox cannot create Teams
	// meetings: a 201 comes back with no joinUrl. That is why the CLI checks
	// allowedOnlineMeetingProviders first (plans/calendar.md §3, F10).
	if body.IsOnlineMeeting && st.allowsTeamsMeetingsLocked() {
		rec.teams = true
		rec.joinURL = "https://teams.microsoft.com/l/meetup-join/" + rec.id
	}
	rec.webLink = "https://outlook.office.com/calendar/item/" + rec.id
	// The record is appended directly rather than through addEventLocked: a
	// created event carries the API-only fields (attendees, transactionId,
	// generated), and the seed type has no place for them.
	st.calEvents = append(st.calEvents, rec)
	st.calByOwner[rec.ownerID] = append(st.calByOwner[rec.ownerID], rec)
	c.json(http.StatusCreated, renderEvent(rec, nil))
}

// dateTime parses a dateTimeTimeZone body value into an instant.
func (b eventCreateBody) dateTime(pair *dateTimeTimeZoneBody) (time.Time, *apiError) {
	if pair == nil || strings.TrimSpace(pair.DateTime) == "" {
		return time.Time{}, badRequestf("The request body must contain both a start and an end.")
	}
	loc, err := time.LoadLocation(orUTC(pair.TimeZone))
	if err != nil {
		resp := &apiError{
			Status: http.StatusBadRequest, Code: "TimeZoneNotSupportedException",
			Message: fmt.Sprintf("The time zone %q is not supported.", pair.TimeZone),
		}
		return time.Time{}, resp
	}
	parsed, perr := time.ParseInLocation("2006-01-02T15:04:05", pair.DateTime, loc)
	if perr != nil {
		if parsed, perr = time.Parse(time.RFC3339, pair.DateTime); perr != nil {
			return time.Time{}, badRequestf("Invalid value for 'dateTime': %q.", pair.DateTime)
		}
	}
	return parsed, nil
}

// orUTC maps an empty or missing zone name to UTC, which is what Graph does.
func orUTC(zone string) string {
	if strings.TrimSpace(zone) == "" {
		return "UTC"
	}
	return zone
}

// allowsTeamsMeetingsLocked reports whether the mailbox can create Teams
// meetings, from its allowedOnlineMeetingProviders.
func (s *store) allowsTeamsMeetingsLocked() bool {
	providers := DefaultOnlineMeetingProviders
	if me := s.users[s.me]; me != nil && len(me.onlineMeetingProviders) > 0 {
		providers = me.onlineMeetingProviders
	}
	for _, p := range providers {
		if strings.EqualFold(p, "teamsForBusiness") {
			return true
		}
	}
	return false
}

// displayNameOf returns a user's display name, falling back to the id.
func (s *store) displayNameOf(id string) string {
	if u := s.users[id]; u != nil && u.displayName != "" {
		return u.displayName
	}
	return id
}

// eventByIDLocked finds an event by id across every calendar the fake holds.
//
// A real user can only act on their own events, but the fake serves one mailbox's
// worth of writes and a seeded event may live in another owner's calendar (a
// shared calendar the viewer can read). Looking it up across owners keeps the
// seed expressive without pretending the service is more permissive: the
// organizer checks in the handlers are what enforce the real rules.
func (s *store) eventByIDLocked(id string) *eventRec {
	for _, ev := range s.calEvents {
		if ev.id == id {
			return ev
		}
	}
	return nil
}

// handleUpdateEvent serves PATCH /me/events/{event-id}
// (refs/graph/api-reference/v1.0/api/event-update.md).
//
// As a non-organizer the live service accepts the patch and changes only the
// caller's copy, which the CLI refuses unless --local-copy was passed
// (plans/calendar.md §3, F10); the fake changes the event either way, because
// that is what the service does to *this* mailbox's copy.
func handleUpdateEvent(c *handlerCtx) {
	var body eventUpdateBody
	if !c.decodeBody(&body) {
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	ev := st.eventByIDLocked(c.param("event-id"))
	if ev == nil {
		c.fail(notFoundEvent(c.param("event-id")))
		return
	}
	if body.Subject != nil {
		ev.subject = *body.Subject
	}
	if body.Start != nil {
		start, serr := eventCreateBody{}.dateTime(body.Start)
		if serr != nil {
			c.fail(serr)
			return
		}
		ev.start = start.UTC()
	}
	if body.End != nil {
		end, eerr := eventCreateBody{}.dateTime(body.End)
		if eerr != nil {
			c.fail(eerr)
			return
		}
		ev.end = end.UTC()
	}
	if body.Location != nil {
		ev.location = body.Location.DisplayName
	}
	// A body property is stored and echoed but never interpreted: rewriting the
	// body can drop the Teams meeting blob, which is exactly why the CLI only
	// sends it when --body was passed (plans/calendar.md §4.7).
	c.json(http.StatusOK, renderEvent(ev, nil))
}

// handleRespondEvent serves the three responses
// (refs/graph/api-reference/v1.0/api/event-accept.md and its siblings).
func handleRespondEvent(c *handlerCtx) {
	var body respondBody
	if !c.decodeBody(&body) {
		return
	}
	// The four responses share one handler, so the action is the last segment of
	// the path rather than a captured parameter.
	action := lastPathSegment(c.rel)
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	ev := st.eventByIDLocked(c.param("event-id"))
	if ev == nil {
		c.fail(notFoundEvent(c.param("event-id")))
		return
	}
	if ev.isOrganizer {
		// "You can't respond to this meeting because you're the meeting
		// organizer." (plans/calendar.md §3, F10)
		c.fail(&apiError{
			Status: http.StatusBadRequest, Code: errCodeInvalidRequest,
			Message: "You can't respond to this meeting because you're the meeting organizer.",
		})
		return
	}
	if body.ProposedNewTime != nil && body.SendResponse != nil && !*body.SendResponse {
		// proposedNewTime with sendResponse:false is a 400 ErrorInvalidParameter
		// (plans/calendar.md §3, F10).
		c.fail(&apiError{
			Status: http.StatusBadRequest, Code: errCodeInvalidParameter,
			Message: "proposedNewTime cannot be set when sendResponse is false.",
		})
		return
	}
	switch action {
	case "accept":
		ev.response = "accepted"
	case "tentativelyAccept":
		ev.response = "tentativelyAccepted"
	case "decline":
		// Declining moves the event to Deleted Items, so it leaves the calendar
		// (refs/graph/api-reference/v1.0/api/event-decline.md).
		ev.response = "declined"
		st.removeEventLocked(ev.id)
	default:
		c.fail(notFoundEvent(c.param("event-id")))
		return
	}
	// The OpenAPI description declares 204 for all four responses and the live
	// service answers 202 (plans/calendar.md §3, F10). The fake follows the
	// description, because that is what the Layer 6 contract test validates; the
	// CLI only requires a 2xx, so it works against either.
	c.noContent()
}

// handleCancelEvent serves POST /me/events/{event-id}/cancel
// (refs/graph/api-reference/v1.0/api/event-cancel.md).
func handleCancelEvent(c *handlerCtx) {
	var body cancelBody
	if !c.decodeBody(&body) {
		return
	}
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	ev := st.eventByIDLocked(c.param("event-id"))
	if ev == nil {
		c.fail(notFoundEvent(c.param("event-id")))
		return
	}
	if !ev.isOrganizer {
		// "You need to be an organizer to cancel a meeting."
		// (plans/calendar.md §3, F10)
		c.fail(&apiError{
			Status: http.StatusBadRequest, Code: errCodeInvalidRequest,
			Message: "You need to be an organizer to cancel a meeting.",
		})
		return
	}
	ev.isCancelled = true
	ev.showAs = "free"
	// 204, not the live 202: see handleRespondEvent.
	c.noContent()
}

// handleDeleteEvent serves DELETE /me/events/{event-id}
// (refs/graph/api-reference/v1.0/api/event-delete.md): 204, then 404 on a second
// delete.
func handleDeleteEvent(c *handlerCtx) {
	st := c.s.st
	st.mu.Lock()
	defer st.mu.Unlock()
	ev := st.eventByIDLocked(c.param("event-id"))
	if ev == nil {
		c.fail(notFoundEvent(c.param("event-id")))
		return
	}
	st.removeEventLocked(ev.id)
	c.noContent()
}

// removeEventLocked drops an event from the store, so a second delete is a 404.
// It leaves every owner's list, because a seeded event may sit in another
// mailbox's calendar (see eventByIDLocked).
func (s *store) removeEventLocked(id string) {
	kept := s.calEvents[:0]
	for _, ev := range s.calEvents {
		if ev.id != id {
			kept = append(kept, ev)
		}
	}
	s.calEvents = kept
	for owner, events := range s.calByOwner {
		remaining := events[:0]
		for _, ev := range events {
			if ev.id != id {
				remaining = append(remaining, ev)
			}
		}
		s.calByOwner[owner] = remaining
	}
}

// lastPathSegment returns the final segment of a Graph path.
func lastPathSegment(rel string) string {
	trimmed := strings.TrimRight(rel, "/")
	if i := strings.LastIndexByte(trimmed, '/'); i >= 0 {
		return trimmed[i+1:]
	}
	return trimmed
}

// notFoundEvent is the 404 every write answers for an unknown id.
func notFoundEvent(id string) *apiError {
	return &apiError{
		Status: http.StatusNotFound, Code: errCodeItemNotFound,
		Message: fmt.Sprintf("The event %q was not found.", id),
	}
}
