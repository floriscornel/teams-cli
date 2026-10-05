package fakegraph

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// This file serves the calendar routes of PLAN.md Phase 6: calendarView (own and
// another user's), the getSchedule free/busy fallback, the online-meetings
// lookup, event search and the mailbox's allowedOnlineMeetingProviders.
//
// Every quirk below was verified live on 2026-10-05 and is recorded in
// refs/INDEX.md under "Known gaps" and in plans/calendar.md §3. The docs do not
// describe most of them, so the fake reproduces the *observed* service rather
// than the tidy version a reader of the api-reference would guess:
//
//   - calendarView pages 10 at a time by default and refuses a window longer
//     than 1825 days with 400 ErrorInvalidRequest (F2);
//   - onlineMeeting is dropped unless isOnlineMeeting is in $select too (F3);
//   - /users/{id}/calendarView answers 403 ErrorAccessDenied for a calendar that
//     is not shared, 404 ErrorItemNotFound for one shared as free/busy only, and
//     404 MailboxNotEnabledForRESTAPI / ErrorInvalidUser for the mailbox cases
//     (F4);
//   - all-day events are floating: they are matched by date, and their dateTime
//     is always midnight of their own dates whatever zone was requested (F5);
//   - without a Prefer header every dateTime comes back as UTC with no offset
//     (F6);
//   - getSchedule caps at 62 days, answers a per-schedule "5016" error with a
//     null scheduleItems for a mailbox that cannot be resolved, and returns only
//     start, end and status for another user's unshared calendar (F7);
//   - a search hitId is the event id in standard base64 (F8);
//   - an onlineMeetings filter that matches nothing is a 400, not an empty list
//     (F9).

// DefaultCalendarViewTop is the calendarView page size when the caller sends no
// $top. The documented default is 10, which is why the CLI always sends
// $top=100 (plans/calendar.md §3, F2).
const DefaultCalendarViewTop = 10

// MaxCalendarViewTop is the largest $top the fake accepts, matching the live
// service's acceptance of 1000 (plans/calendar.md §3, F2).
const MaxCalendarViewTop = 1000

// maxCalendarViewDays is the longest calendarView window the live service
// accepted; a longer one is a 400 ErrorInvalidRequest (plans/calendar.md §3, F2).
const maxCalendarViewDays = 1825

// maxScheduleDays is the longest getSchedule window the live service accepted;
// 63 days is a 400 ErrorTimeIntervalTooBig (plans/calendar.md §3, F7).
const maxScheduleDays = 62

// Graph error codes the calendar routes answer with
// (plans/calendar.md §3, F4).
const (
	errCodeAccessDenied       = "ErrorAccessDenied"
	errCodeItemNotFound       = "ErrorItemNotFound"
	errCodeMailboxNotEnabled  = "MailboxNotEnabledForRESTAPI"
	errCodeInvalidUser        = "ErrorInvalidUser"
	errCodeInvalidRequest     = "ErrorInvalidRequest"
	errCodeTimeIntervalTooBig = "ErrorTimeIntervalTooBig"
	// errCodeNoChatMatch is what the onlineMeetings lookup answers when the
	// JoinWebUrl filter matches nothing. The live message starts with "1026"
	// (plans/calendar.md §3, F9).
	errCodeNoChatMatch = "BadRequest"
	// scheduleResponseCodeNoMailbox is the per-schedule error.responseCode the
	// live service returned for a mailbox it could not resolve
	// (plans/calendar.md §3, F7).
	scheduleResponseCodeNoMailbox = "5016"
)

// eventRec is a stored calendar event. It is derived from a seeded
// [CalendarEvent].
type eventRec struct {
	id             string
	ownerID        string
	subject        string
	organizerName  string
	isOrganizer    bool
	allowProposals bool
	location       string
	showAs         string
	isCancelled    bool
	allDay         bool
	// start and end are instants for a timed event; for an all-day event they
	// are midnight UTC of the event's own first and last day.
	start, end time.Time
	// allDayStart and allDayEnd are the event's own dates (YYYY-MM-DD), which is
	// what an all-day event renders and matches on.
	allDayStart string
	allDayEnd   string
	teams       bool
	joinURL     string
	response    string
	eventType   string
	seriesID    string
	webLink     string
}

// addEventLocked records one seeded event. Callers hold the write lock (or are
// constructing the store).
func (s *store) addEventLocked(ev CalendarEvent) {
	rec := &eventRec{
		id:             ev.ID,
		ownerID:        ev.OwnerID,
		subject:        ev.Subject,
		organizerName:  ev.OrganizerName,
		isOrganizer:    ev.IsOrganizer,
		allowProposals: ev.AllowNewTimeProposals == nil || *ev.AllowNewTimeProposals,
		location:       ev.Location,
		showAs:         ev.ShowAs,
		isCancelled:    ev.IsCancelled,
		allDay:         ev.Kind == CalendarEventAllDay,
		teams:          ev.Teams,
		joinURL:        ev.JoinURL,
		response:       ev.Response,
		eventType:      ev.Type,
		seriesID:       ev.SeriesMasterID,
		webLink:        ev.WebLink,
	}
	if rec.organizerName == "" {
		rec.organizerName = rec.ownerID
	}
	if rec.response == "" {
		rec.response = "organizer"
	}
	if rec.allDay {
		rec.allDayStart = ev.allDayStart
		rec.allDayEnd = ev.allDayEnd
		start, err := time.Parse("2006-01-02", ev.allDayStart)
		if err == nil {
			rec.start = start
		}
		end, err := time.Parse("2006-01-02", ev.allDayEnd)
		if err == nil {
			rec.end = end
		}
	} else {
		rec.start = ev.Start.UTC()
		rec.end = ev.End.UTC()
	}
	if rec.joinURL == "" && rec.teams {
		rec.joinURL = "https://teams.microsoft.com/l/meetup-join/" + rec.id
	}
	if rec.webLink == "" {
		rec.webLink = "https://outlook.office.com/calendar/item/" + rec.id
	}
	s.calEvents = append(s.calEvents, rec)
	s.calByOwner[rec.ownerID] = append(s.calByOwner[rec.ownerID], rec)
}

// ownerEventsLocked returns the events of one mailbox, in seed order.
func (s *store) ownerEventsLocked(ownerID string) []*eventRec {
	return s.calByOwner[ownerID]
}

// calendarWindow is a parsed startDateTime/endDateTime pair. The start and end
// days are the *local* days of the request, which is what an all-day event
// matches against (plans/calendar.md §3, F5).
type calendarWindow struct {
	start, end time.Time
	startDay   string
	endDay     string
}

// parseCalendarWindow reads and validates startDateTime/endDateTime. Both are
// required and must be RFC3339: the offset in the value is what Graph honours
// (refs/graph/api-reference/v1.0/api/calendar-list-calendarview.md:60).
func parseCalendarWindow(q url.Values) (calendarWindow, *apiError) {
	var w calendarWindow
	rawStart, rawEnd := q.Get("startDateTime"), q.Get("endDateTime")
	if rawStart == "" || rawEnd == "" {
		return w, badRequestf("The startDateTime and endDateTime query parameters are required.")
	}
	start, err := time.Parse(time.RFC3339, rawStart)
	if err != nil {
		return w, badRequestf("Invalid value for 'startDateTime': %q is not an ISO 8601 date-time.", rawStart)
	}
	end, err := time.Parse(time.RFC3339, rawEnd)
	if err != nil {
		return w, badRequestf("Invalid value for 'endDateTime': %q is not an ISO 8601 date-time.", rawEnd)
	}
	if end.Before(start) {
		return w, badRequestf("The endDateTime value must be later than the startDateTime value.")
	}
	if end.Sub(start) > maxCalendarViewDays*24*time.Hour {
		return w, &apiError{
			Status:  http.StatusBadRequest,
			Code:    errCodeInvalidRequest,
			Message: fmt.Sprintf("The time range exceeds the maximum of %d days.", maxCalendarViewDays),
		}
	}
	w.start, w.end = start, end
	w.startDay = start.Format("2006-01-02")
	w.endDay = end.Format("2006-01-02")
	return w, nil
}

// matches reports whether an event belongs in the window.
//
// A timed event is kept when it overlaps the window as instants. An all-day
// event is kept when its own date range intersects the window's local days: the
// live service matched all-day events as UTC midnight to midnight, so a Tokyo
// window for 07-04 also returned the 07-03 all-day event
// (plans/calendar.md §3, F5).
func (w calendarWindow) matches(ev *eventRec) bool {
	if ev.allDay {
		return ev.allDayStart < w.endDay && ev.allDayEnd > w.startDay
	}
	return ev.start.Before(w.end) && ev.end.After(w.start)
}

// handleCalendarView serves GET /me/calendarView and
// GET /users/{user-id}/calendarView
// (refs/graph/api-reference/v1.0/api/calendar-list-calendarview.md).
func handleCalendarView(c *handlerCtx) {
	ownerID := c.s.st.me
	if raw := c.param("user-id"); raw != "" && !strings.EqualFold(raw, "me") {
		st := c.s.st
		st.mu.RLock()
		u := st.lookupUser(raw)
		st.mu.RUnlock()
		if u == nil {
			c.fail(&apiError{
				Status: http.StatusNotFound, Code: errCodeInvalidUser,
				Message: fmt.Sprintf("The user %q was not found.", raw),
			})
			return
		}
		if u.noMailbox {
			c.fail(&apiError{
				Status: http.StatusNotFound, Code: errCodeMailboxNotEnabled,
				Message: fmt.Sprintf("The mailbox of %s is not enabled for the REST API.", u.id),
			})
			return
		}
		ownerID = u.id
		if fallback := c.s.calendarAccessDenied(ownerID); fallback != nil {
			c.fail(fallback)
			return
		}
	}

	window, ferr := parseCalendarWindow(c.query)
	if ferr != nil {
		c.fail(ferr)
		return
	}
	top, terr := parseCalendarViewTop(c.query)
	if terr != nil {
		c.fail(terr)
		return
	}
	selects := parseSelect(c.query.Get("$select"))

	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	all := make([]*eventRec, 0, len(st.calByOwner[ownerID]))
	for _, ev := range st.ownerEventsLocked(ownerID) {
		if window.matches(ev) {
			all = append(all, ev)
		}
	}
	items, next := windowSlice(all, top, pageOffset(c.query))
	out := make([]map[string]any, 0, len(items))
	for _, ev := range items {
		out = append(out, renderEvent(ev, selects))
	}
	c.json(http.StatusOK, pageOf(c.s, c.rel, c.query, out, next, len(all)))
}

// handleGetEvent serves GET /me/events/{event-id}
// (refs/graph/api-reference/v1.0/api/event-get.md).
//
// Unlike calendarView, this endpoint returns onlineMeeting whether or not
// isOnlineMeeting was selected (plans/calendar.md §3, F3), which is what the
// CLI relies on for `show` and for the Phase 6b pre-checks.
func handleGetEvent(c *handlerCtx) {
	id := c.param("event-id")
	if id == "" {
		c.fail(notFoundf("The event %q was not found.", id))
		return
	}
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	var found *eventRec
	for _, ev := range st.ownerEventsLocked(st.me) {
		if ev.id == id {
			found = ev
			break
		}
	}
	if found == nil {
		c.fail(&apiError{
			Status: http.StatusNotFound, Code: errCodeItemNotFound,
			Message: fmt.Sprintf("The event %q was not found.", id),
		})
		return
	}
	c.json(http.StatusOK, renderEvent(found, parseSelect(c.query.Get("$select"))))
}

// calendarAccessDenied returns the error another user's calendarView answers
// with, or nil when the signed-in user may read it. The signed-in user's own
// calendar is always readable.
//
// The two codes are the live service's: a calendar that is not shared at all is
// 403 ErrorAccessDenied, and one shared as free/busy only answered 404
// ErrorItemNotFound (plans/calendar.md §3, F4).
func (s *Server) calendarAccessDenied(ownerID string) *apiError {
	if ownerID == s.st.me {
		return nil
	}
	switch s.st.calAccess[ownerID] {
	case CalendarAccessRead:
		return nil
	case CalendarAccessFreeBusy:
		return &apiError{
			Status: http.StatusNotFound, Code: errCodeItemNotFound,
			Message: "The specified object was not found in the store.",
		}
	default:
		return &apiError{
			Status: http.StatusForbidden, Code: errCodeAccessDenied,
			Message: "You do not have permission to read this calendar.",
		}
	}
}

// parseCalendarViewTop reads $top for calendarView, defaulting to the
// documented 10 and refusing anything above the observed ceiling.
func parseCalendarViewTop(q url.Values) (int, *apiError) {
	raw := q.Get("$top")
	if raw == "" {
		return DefaultCalendarViewTop, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, badRequestf("Invalid value for '$top': %q is not an integer.", raw)
	}
	if n < 1 {
		return 0, badRequestf("The '$top' value must be at least 1, got %d.", n)
	}
	if n > MaxCalendarViewTop {
		return 0, badRequestf("The '$top' value is greater than the maximum allowed value. The maximum allowed value is %d.", MaxCalendarViewTop)
	}
	return n, nil
}

// renderEvent builds the wire event, honouring $select.
//
// The one rule that is not mechanical is F3: on calendarView Graph omits
// onlineMeeting unless isOnlineMeeting was selected too, even though
// GET /me/events/{id} always returns it. That is reproduced here rather than in
// a comment so a regression in the CLI's $select fails a test.
func renderEvent(ev *eventRec, selectFields []string) map[string]any {
	want := selectSet(selectFields)
	out := map[string]any{"id": ev.id}
	put := func(key string, value any) {
		if want == nil || want[key] {
			out[key] = value
		}
	}
	put("subject", ev.subject)
	put("isAllDay", ev.allDay)
	put("isCancelled", ev.isCancelled)
	put("showAs", ev.showAs)
	put("isOrganizer", ev.isOrganizer)
	put("type", ev.eventType)
	put("isOnlineMeeting", ev.teams)
	put("webLink", ev.webLink)
	if ev.seriesID != "" {
		put("seriesMasterId", ev.seriesID)
	}
	if ev.allDay {
		// An all-day event is always midnight to midnight of its own dates,
		// whatever zone was requested (plans/calendar.md §3, F5).
		put("start", dateTimeTimeZoneWire(ev.allDayStart+"T00:00:00.0000000", "UTC"))
		put("end", dateTimeTimeZoneWire(ev.allDayEnd+"T00:00:00.0000000", "UTC"))
	} else {
		put("start", dateTimeTimeZoneWire(ev.start.Format(graphDateFormat), "UTC"))
		put("end", dateTimeTimeZoneWire(ev.end.Format(graphDateFormat), "UTC"))
	}
	if want == nil || want["organizer"] {
		out["organizer"] = recipientWire(ev.organizerName, ev.ownerID)
	}
	if ev.location != "" && (want == nil || want["location"]) {
		out["location"] = map[string]any{"displayName": ev.location}
	}
	if want == nil || want["responseStatus"] {
		out["responseStatus"] = map[string]any{"response": ev.response}
	}
	if ev.teams && (want == nil || want["onlineMeetingProvider"]) {
		out["onlineMeetingProvider"] = "teamsForBusiness"
	}
	// F3: onlineMeeting only travels when isOnlineMeeting travelled with it.
	onlineMeetingWanted := want == nil || (want["onlineMeeting"] && want["isOnlineMeeting"])
	if ev.teams && onlineMeetingWanted {
		out["onlineMeeting"] = map[string]any{"joinUrl": ev.joinURL}
	}
	if want == nil || want["allowNewTimeProposals"] {
		out["allowNewTimeProposals"] = ev.allowProposals
	}
	if want == nil || want["attendees"] {
		// Attendees are empty: Phase 6 reads the collection but no seeded event
		// carries one yet, and `show` renders an empty list rather than failing.
		out["attendees"] = []any{}
	}
	return out
}

// graphDateFormat is the seven-digit fractional-second form Graph returns.
const graphDateFormat = "2006-01-02T15:04:05.0000000"

// selectSet turns a parsed $select into a lookup set; nil means "everything",
// which is what an absent $select asks for.
func selectSet(fields []string) map[string]bool {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]bool, len(fields))
	for _, f := range fields {
		out[f] = true
	}
	return out
}

func dateTimeTimeZoneWire(value, zone string) map[string]any {
	return map[string]any{"dateTime": value, "timeZone": zone}
}

func recipientWire(name, address string) map[string]any {
	return map[string]any{"emailAddress": map[string]any{"name": name, "address": address}}
}

// pageOffset reads the offset a page request carries, in either spelling the
// api-reference uses ($skip for calendarView, $skiptoken for the fakes'
// generated next links).
func pageOffset(q url.Values) int {
	for _, key := range []string{"$skip", "$skiptoken"} {
		if raw := q.Get(key); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
				return n
			}
		}
	}
	return 0
}

// windowSlice returns the page of items plus the next offset.
func windowSlice(items []*eventRec, top, offset int) ([]*eventRec, int) {
	if offset >= len(items) {
		return nil, len(items)
	}
	end := offset + top
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end], end
}

// ---- getSchedule -----------------------------------------------------------

type getScheduleBody struct {
	Schedules                []string `json:"schedules"`
	AvailabilityViewInterval int      `json:"availabilityViewInterval"`
}

// handleGetSchedule serves POST /me/calendar/getSchedule
// (refs/graph/api-reference/v1.0/api/calendar-getschedule.md).
func handleGetSchedule(c *handlerCtx) {
	var body getScheduleBody
	if !c.decodeBody(&body) {
		return
	}
	if len(body.Schedules) == 0 {
		c.fail(badRequestf("The schedules collection must not be empty."))
		return
	}
	if len(body.Schedules) > 20 {
		c.fail(badRequestf("The maximum number of schedules per request is 20."))
		return
	}
	if body.AvailabilityViewInterval != 0 &&
		(body.AvailabilityViewInterval < 5 || body.AvailabilityViewInterval > 1440) {
		c.fail(badRequestf("The availabilityViewInterval must be between 5 and 1440."))
	}

	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]map[string]any, 0, len(body.Schedules))
	for _, address := range body.Schedules {
		out = append(out, st.scheduleForLocked(c.s, address))
	}
	c.json(http.StatusOK, map[string]any{"value": out})
}

// scheduleForLocked builds one scheduleInformation.
//
// A mailbox that cannot be resolved (no mailbox, or no such user) answers a
// "5016" error with a null scheduleItems: the two cases are indistinguishable in
// the live service, which is why scheduleItems is emitted as an explicit null
// (plans/calendar.md §3, F7).
//
// Another user's unshared calendar yields items carrying only start, end and
// status, and your own calendar yields subject and location as well.
func (s *store) scheduleForLocked(srv *Server, address string) map[string]any {
	u := s.lookupUser(address)
	if u == nil || u.noMailbox {
		return map[string]any{
			"scheduleId":    address,
			"scheduleItems": nil,
			"error": map[string]any{
				"message":      "Proxy web request failed.",
				"responseCode": scheduleResponseCodeNoMailbox,
			},
		}
	}
	full := u.id == s.me || s.calAccess[u.id] == CalendarAccessRead
	items := make([]map[string]any, 0, 8)
	for _, ev := range s.ownerEventsLocked(u.id) {
		status := ev.showAs
		if ev.isCancelled {
			status = "free"
		}
		// The live service returns "free" items too; they are kept here so the
		// CLI's own dropping of them is exercised.
		item := map[string]any{
			"start":  dateTimeTimeZoneWire(ev.start.Format(graphDateFormat), "UTC"),
			"end":    dateTimeTimeZoneWire(ev.end.Format(graphDateFormat), "UTC"),
			"status": status,
		}
		if full {
			item["subject"] = ev.subject
			if ev.location != "" {
				item["location"] = ev.location
			}
		}
		items = append(items, item)
	}
	return map[string]any{
		"scheduleId":       u.mail,
		"availabilityView": "",
		"scheduleItems":    items,
	}
}

// ---- allowedOnlineMeetingProviders ----------------------------------------

// handleCalendar serves GET /me/calendar
// (refs/graph/api-reference/v1.0/api/calendar-get.md). Only the
// allowedOnlineMeetingProviders list is rendered, which is all Phase 6b reads.
func handleCalendar(c *handlerCtx) {
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	providers := DefaultOnlineMeetingProviders
	if me := st.users[st.me]; me != nil && len(me.onlineMeetingProviders) > 0 {
		providers = me.onlineMeetingProviders
	}
	out := map[string]any{"allowedOnlineMeetingProviders": providers}
	if want := selectSet(parseSelect(c.query.Get("$select"))); want != nil {
		filtered := map[string]any{}
		for key := range want {
			if v, ok := out[key]; ok {
				filtered[key] = v
			}
		}
		c.json(http.StatusOK, filtered)
		return
	}
	c.json(http.StatusOK, out)
}

// ---- onlineMeetings lookup -------------------------------------------------

// handleOnlineMeetings serves GET /me/onlineMeetings with a JoinWebUrl filter
// (refs/graph/api-reference/v1.0/api/onlinemeeting-get.md).
//
// A filter that matches nothing answers 400 with a message starting "1026", not
// an empty list, and that is what the CLI treats as "no chat found"
// (plans/calendar.md §3, F9).
func handleOnlineMeetings(c *handlerCtx) {
	filter := c.query.Get("$filter")
	want := oDataFilterLiteral(filter, "JoinWebUrl")
	if want == "" {
		// No usable filter: the live service refuses it the same way.
		c.fail(&apiError{
			Status: http.StatusBadRequest, Code: errCodeNoChatMatch,
			Message: "1026: The filter supplied is invalid.",
		})
		return
	}
	matches := c.s.meetingChatsFor(want)
	if len(matches) == 0 {
		c.fail(&apiError{
			Status: http.StatusBadRequest, Code: errCodeNoChatMatch,
			Message: "1026: The JoinWebUrl supplied does not match any online meeting.",
		})
		return
	}
	c.json(http.StatusOK, collectionWire[map[string]any]{Value: matches})
}

// meetingChatsFor returns the onlineMeeting records whose join URL matches, as
// chatInfo.threadId values. Phase 6 seeds them through the event's own join URL,
// so the thread id is derived the way the CLI's own meeting chats are.
func (s *Server) meetingChatsFor(joinURL string) []map[string]any {
	st := s.st
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, ev := range st.calEvents {
		if !ev.teams || ev.joinURL != joinURL || seen[ev.id] {
			continue
		}
		seen[ev.id] = true
		out = append(out, map[string]any{
			"id":       "meeting-" + ev.id,
			"chatInfo": map[string]any{"threadId": MeetingThreadID(ev.id)},
		})
	}
	return out
}

// MeetingThreadID derives the deterministic chat thread id the fake serves for a
// meeting, in the documented 19:meeting_…@thread.v2 form
// (refs/graph/api-reference/v1.0/resources/chatinfo.md).
func MeetingThreadID(eventID string) string {
	return "19:meeting_" + eventID + "@thread.v2"
}

// StandardBase64 encodes an event id the way the search service reports a hitId:
// standard base64, so the result can contain "/" and "+". The CLI maps those two
// characters back to the URL-safe alphabet to recover an id the REST endpoints
// accept (plans/calendar.md §3, F8).
func StandardBase64(id string) string {
	return base64.StdEncoding.EncodeToString([]byte(id))
}

// oDataFilterLiteral extracts the single-quoted literal of "PROPERTY eq '…'".
// OData escapes a quote by doubling it, so the doubled pair is unescaped here.
func oDataFilterLiteral(filter, property string) string {
	fields := splitODataFields(filter)
	for i := 0; i+2 < len(fields); i += 3 {
		if !strings.EqualFold(fields[i], property) || !strings.EqualFold(fields[i+1], "eq") {
			continue
		}
		literal := strings.TrimSpace(fields[i+2])
		if len(literal) < 2 || literal[0] != '\'' || literal[len(literal)-1] != '\'' {
			return ""
		}
		return strings.ReplaceAll(literal[1:len(literal)-1], "''", "'")
	}
	return ""
}

// ---- event search ----------------------------------------------------------

// handleEventSearch answers a POST /search/query whose only entity type is
// "event" (refs/graph/concepts/search-concept-events.md).
//
// The hitId is the event id in standard base64 — the live service's form, which
// the CLI maps back to the REST id by swapping "/"→"-" and "+"→"_", and which
// is why a search hit can be passed straight to `teams calendar show`
// (plans/calendar.md §3, F8).
func handleEventSearch(c *handlerCtx, req searchRequestWire) (handled bool) {
	if len(req.EntityTypes) != 1 || !strings.EqualFold(req.EntityTypes[0], "event") {
		return false
	}
	size := 25
	if req.Size != nil {
		size = *req.Size
	}
	if size < 1 || size > 25 {
		c.fail(badRequestf("The 'size' value must be between 1 and 25 for entityTypes 'event', got %d.", size))
		return true
	}
	st := c.s.st
	st.mu.RLock()
	defer st.mu.RUnlock()

	query := strings.ToLower(strings.TrimSpace(req.Query.QueryString))
	// Search covers the signed-in user's primary calendar only.
	all := st.ownerEventsLocked(st.me)
	matches := make([]*eventRec, 0, len(all))
	for _, ev := range all {
		if query == "" || strings.Contains(strings.ToLower(ev.subject), query) {
			matches = append(matches, ev)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].id < matches[j].id })
	from := 0
	if req.From != nil {
		from = *req.From
	}
	if from < 0 {
		from = 0
	}
	if from > len(matches) {
		from = len(matches)
	}
	end := from + size
	if end > len(matches) {
		end = len(matches)
	}
	page := matches[from:end]
	hits := make([]map[string]any, 0, len(page))
	for _, ev := range page {
		hits = append(hits, map[string]any{
			// The hitId is the event's REST id in standard base64, which is the
			// live service's form and the reason the CLI maps "/"→"-" and "+"→"_"
			// to recover an id the REST endpoints accept (plans/calendar.md §3, F8).
			"hitId":   StandardBase64(ev.id),
			"rank":    1,
			"summary": "",
			"resource": map[string]any{
				"start": dateTimeTimeZoneWire(ev.start.Format(graphDateFormat), "UTC"),
				"end":   dateTimeTimeZoneWire(ev.end.Format(graphDateFormat), "UTC"),
			},
		})
	}
	c.json(http.StatusOK, map[string]any{
		"value": []any{map[string]any{
			"searchTerms": []string{req.Query.QueryString},
			"hitsContainers": []any{map[string]any{
				"hits": hits,
				// total counts the page, not the matches
				// (refs/graph/concepts/search-concept-events.md:97).
				"total":                len(hits),
				"moreResultsAvailable": end < len(matches),
			}},
		}},
	})
	return true
}
