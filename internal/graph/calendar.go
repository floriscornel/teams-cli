package graph

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// This file wraps the calendar reads of PLAN.md Phase 6a (`teams calendar
// list|show|search`, plans/calendar.md §4.3) and the two cheap lookups Phase 6b
// needs before it writes.
//
// The docs do not describe the behaviour these wrappers depend on; the handoff
// verified it against a live tenant on 2026-10-05 and every such fact is marked
// "verified live 2026-10-05 (handoff §3)" here and recorded in refs/INDEX.md
// under "Known gaps". Do not "tidy" one of them away without re-verifying.
//
// Refs: refs/graph/api-reference/v1.0/api/calendar-list-calendarview.md,
// refs/graph/api-reference/v1.0/api/calendar-getschedule.md,
// refs/graph/api-reference/v1.0/api/event-get.md,
// refs/graph/api-reference/v1.0/api/onlinemeeting-get.md,
// refs/graph/concepts/search-concept-events.md.

// CalendarViewTop is the page size every calendarView call sends. The documented
// default is 10, which is far too small for a day, and the live service accepted
// 1000 (handoff §3, F2). The list.go comment block records the other ceilings.
const CalendarViewTop = 100

// MaxCalendarViewDays is the largest calendarView range the live service
// accepts: 1826 days is a 400 ErrorInvalidRequest (handoff §3, F2). The CLI caps
// its own window lower so the one-day widening on each side still fits.
const MaxCalendarViewDays = 1825

// MaxScheduleDays is the largest getSchedule range: 63 days is a 400
// ErrorTimeIntervalTooBig (handoff §3, F7). It is not in the docs.
const MaxScheduleDays = 62

// MaxScheduleAddresses is the documented maximum number of schedules one
// getSchedule call takes (refs/graph/api-reference/v1.0/api/calendar-getschedule.md:40).
// GetSchedule chunks larger sets into calls of this size.
const MaxScheduleAddresses = 20

// DefaultScheduleInterval is the documented default availabilityView interval
// in minutes; the documented range is 5 to 1440
// (refs/graph/api-reference/v1.0/api/calendar-getschedule.md:44).
const (
	DefaultScheduleInterval = 30
	MinScheduleInterval     = 5
	MaxScheduleInterval     = 1440
)

// DateTimeTimeZone is microsoft.graph.dateTimeTimeZone, the {dateTime, timeZone}
// pair every calendar request and response carries
// (refs/graph/api-reference/v1.0/resources/datetimetimezone.md).
//
// Without a Prefer: outlook.timezone header Graph answers in UTC and the
// dateTime string carries no offset at all
// ("2026-10-06T01:00:00.0000000"), which is why UTC has its own layout
// (handoff §3, F6). The CLI never sends that header: an unknown zone is a hard
// 400 TimeZoneNotSupportedException rather than a fallback, so reading UTC and
// converting locally is both simpler and safer (decision D4).
type DateTimeTimeZone struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone,omitempty"`
}

// graphDateTimeLayout is the seven-digit fractional-second form Graph returns.
const graphDateTimeLayout = "2006-01-02T15:04:05.9999999"

// UTC converts the pair to an instant. The result is always UTC: callers convert
// to the display location themselves, because the CLI's own timezone rule
// (--tz, then $TZ, then /etc/localtime) is not Graph's.
func (d DateTimeTimeZone) UTC() (time.Time, error) {
	raw := strings.TrimSpace(d.DateTime)
	if raw == "" {
		return time.Time{}, fmt.Errorf("graph: an empty dateTime with timeZone %q", d.TimeZone)
	}
	// An explicit offset wins: Graph honours the offset in a request value and
	// a fixture or a future API revision may carry one back.
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC(), nil
	}
	loc, err := d.location()
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.ParseInLocation(graphDateTimeLayout, raw, loc)
	if err != nil {
		return time.Time{}, fmt.Errorf("graph: cannot parse dateTime %q (timeZone %q): %w", raw, d.TimeZone, err)
	}
	return t.UTC(), nil
}

// location resolves the zone name. An empty or "UTC" zone is UTC; anything else
// goes through time.LoadLocation, which the handoff confirmed accepts IANA names
// such as "Asia/Tokyo" (handoff §3, F7).
func (d DateTimeTimeZone) location() (*time.Location, error) {
	switch name := strings.TrimSpace(d.TimeZone); {
	case name == "" || strings.EqualFold(name, "UTC"):
		return time.UTC, nil
	default:
		loc, err := time.LoadLocation(name)
		if err != nil {
			return nil, fmt.Errorf("graph: the response names time zone %q, which this machine does not know: %w", name, err)
		}
		return loc, nil
	}
}

// Date is the first ten characters of dateTime: the calendar date of a floating
// all-day event, which must never be time-converted (handoff §3, F5).
func (d DateTimeTimeZone) Date() string {
	raw := strings.TrimSpace(d.DateTime)
	if len(raw) < len("2006-01-02") {
		return ""
	}
	return raw[:len("2006-01-02")]
}

// EmailAddress is microsoft.graph.emailAddress, which an organizer, an attendee
// and a free/busy schedule address all use
// (refs/graph/api-reference/v1.0/resources/emailaddress.md).
type EmailAddress struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
}

// Recipient is microsoft.graph.recipient (an organizer)
// (refs/graph/api-reference/v1.0/resources/recipient.md).
type Recipient struct {
	EmailAddress *EmailAddress `json:"emailAddress,omitempty"`
}

// Name is the recipient's display name, falling back to the address.
func (r *Recipient) Name() string {
	if r == nil || r.EmailAddress == nil {
		return ""
	}
	if r.EmailAddress.Name != "" {
		return r.EmailAddress.Name
	}
	return r.EmailAddress.Address
}

// Address is the recipient's SMTP address, or "".
func (r *Recipient) Address() string {
	if r == nil || r.EmailAddress == nil {
		return ""
	}
	return r.EmailAddress.Address
}

// ResponseStatus is microsoft.graph.responseStatus
// (refs/graph/api-reference/v1.0/resources/responsestatus.md). The values are
// none, organizer, tentativelyAccepted, accepted, declined and notResponded.
type ResponseStatus struct {
	Response string     `json:"response,omitempty"`
	Time     *time.Time `json:"time,omitzero"`
}

// Location is the part of microsoft.graph.location the CLI renders
// (refs/graph/api-reference/v1.0/resources/location.md).
type Location struct {
	DisplayName string `json:"displayName,omitempty"`
}

// OnlineMeetingInfo is microsoft.graph.onlineMeetingInfo. Only joinUrl is
// rendered, but the rest of the documented shape stays so the --json object is
// the Graph object (refs/graph/api-reference/v1.0/resources/onlinemeetinginfo.md).
type OnlineMeetingInfo struct {
	ConferenceID   string   `json:"conferenceId,omitempty"`
	JoinURL        string   `json:"joinUrl,omitempty"`
	QuickDial      string   `json:"quickDial,omitempty"`
	TollFreeNumber []string `json:"tollFreeNumbers,omitempty"`
	TollNumber     string   `json:"tollNumber,omitempty"`
}

// Attendee is microsoft.graph.attendee
// (refs/graph/api-reference/v1.0/resources/attendee.md).
type Attendee struct {
	EmailAddress *EmailAddress   `json:"emailAddress,omitempty"`
	Status       *ResponseStatus `json:"status,omitempty"`
	Type         string          `json:"type,omitempty"`
}

// DisplayName is the attendee's name, falling back to the address.
func (a Attendee) DisplayName() string {
	if a.EmailAddress == nil {
		return ""
	}
	if a.EmailAddress.Name != "" {
		return a.EmailAddress.Name
	}
	return a.EmailAddress.Address
}

// Event is the subset of microsoft.graph.event the calendar commands read
// (refs/graph/api-reference/v1.0/resources/event.md). The $select in
// calendarViewSelect names exactly these fields plus id.
type Event struct {
	ID          string           `json:"id"`
	Subject     string           `json:"subject,omitempty"`
	Start       DateTimeTimeZone `json:"start"`
	End         DateTimeTimeZone `json:"end"`
	IsAllDay    bool             `json:"isAllDay,omitempty"`
	IsCancelled bool             `json:"isCancelled,omitempty"`
	ShowAs      string           `json:"showAs,omitempty"`
	Organizer   *Recipient       `json:"organizer,omitempty"`
	IsOrganizer bool             `json:"isOrganizer,omitempty"`
	Location    *Location        `json:"location,omitempty"`
	// IsOnlineMeeting must stay in $select or calendarView omits
	// onlineMeeting entirely (verified live 2026-10-05, handoff §3, F3).
	IsOnlineMeeting       bool               `json:"isOnlineMeeting,omitempty"`
	OnlineMeeting         *OnlineMeetingInfo `json:"onlineMeeting,omitempty"`
	OnlineMeetingProvider string             `json:"onlineMeetingProvider,omitempty"`
	ResponseStatus        *ResponseStatus    `json:"responseStatus,omitempty"`
	WebLink               string             `json:"webLink,omitempty"`
	// Type is singleInstance, occurrence, exception or seriesMaster.
	Type string `json:"type,omitempty"`
	// SeriesMasterID is set when this instance belongs to a recurring series.
	SeriesMasterID string `json:"seriesMasterId,omitempty"`
	// Body and the write-only-ish pre-check fields are requested only by the
	// commands that need them (show, and the Phase 6b pre-checks).
	Body                       *ItemBody  `json:"body,omitempty"`
	Attendees                  []Attendee `json:"attendees,omitempty"`
	AllowNewTimeProposals      *bool      `json:"allowNewTimeProposals,omitzero"`
	ResponseRequested          *bool      `json:"responseRequested,omitzero"`
	OnlineMeetingProviderKnown bool       `json:"-"`
	TransactionID              string     `json:"transactionId,omitempty"`
	Occurrences                []Event    `json:"occurrences,omitempty"`
}

// SubjectLine is the event's subject, with the placeholder the CLI prints for an
// event Graph returned without one.
func (e Event) SubjectLine() string {
	if strings.TrimSpace(e.Subject) == "" {
		return "(no subject)"
	}
	return e.Subject
}

// IsTeamsMeeting reports whether the meeting is a Teams meeting. Graph can set
// isOnlineMeeting for a Skype meeting too, so the provider is checked as well
// (refs/graph/api-reference/v1.0/resources/event.md, "onlineMeetingProvider").
func (e Event) IsTeamsMeeting() bool {
	return e.IsOnlineMeeting && e.OnlineMeeting != nil &&
		strings.EqualFold(e.OnlineMeetingProvider, OnlineMeetingProviderTeams)
}

// JoinURL is the online meeting's join URL, or "".
func (e Event) JoinURL() string {
	if e.OnlineMeeting == nil {
		return ""
	}
	return e.OnlineMeeting.JoinURL
}

// OnlineMeetingProviderTeams is the documented provider value for a Teams
// meeting (refs/graph/api-reference/v1.0/resources/event.md).
const OnlineMeetingProviderTeams = "teamsForBusiness"

// calendarViewSelect is the $select every calendarView call sends. It is the
// handoff's list (plans/calendar.md §4.3).
//
// isOnlineMeeting must stay in this list: on calendarView Graph omits
// onlineMeeting when isOnlineMeeting is not selected as well, even though
// GET /me/events/{id} returns it either way (verified live 2026-10-05, handoff
// §3, F3).
const calendarViewSelect = "id,subject,start,end,isAllDay,isCancelled,showAs,organizer,isOrganizer," +
	"isOnlineMeeting,onlineMeeting,onlineMeetingProvider,location,responseStatus,webLink,type,seriesMasterId"

// eventPreCheckSelect is the pre-check read Phase 6b uses before it writes
// (plans/calendar.md §4.7).
const eventPreCheckSelect = "isOrganizer,allowNewTimeProposals,isOnlineMeeting,onlineMeeting,subject"

// ScheduleInformation is microsoft.graph.scheduleInformation
// (refs/graph/api-reference/v1.0/resources/scheduleinformation.md).
//
// ScheduleItems is a pointer because Graph sets it to null, not to an empty
// list, when the per-schedule lookup failed: a user with no mailbox and a user
// that does not exist both answer error.responseCode "5016" with a null
// scheduleItems, and the two cannot be told apart (verified live 2026-10-05,
// handoff §3, F7).
type ScheduleInformation struct {
	ScheduleID       string          `json:"scheduleId,omitempty"`
	AvailabilityView string          `json:"availabilityView,omitempty"`
	ScheduleItems    *[]ScheduleItem `json:"scheduleItems"`
	Error            *FreeBusyError  `json:"error,omitempty"`
	WorkingHours     *WorkingHours   `json:"workingHours,omitempty"`
}

// Items returns the schedule items, treating a null scheduleItems as none.
func (s ScheduleInformation) Items() []ScheduleItem {
	if s.ScheduleItems == nil {
		return nil
	}
	return *s.ScheduleItems
}

// FreeBusyError is microsoft.graph.freeBusyError
// (refs/graph/api-reference/v1.0/resources/freebusyerror.md).
type FreeBusyError struct {
	Message      string `json:"message,omitempty"`
	ResponseCode string `json:"responseCode,omitempty"`
}

// NoMailboxResponseCode is the responseCode a schedule carries when the mailbox
// could not be resolved at all: no mailbox, inactive, on-premises, or no such
// user (verified live 2026-10-05, handoff §3, F7).
const NoMailboxResponseCode = "5016"

// WorkingHours is microsoft.graph.workingHours; only the time zone is read, and
// only to render it (refs/graph/api-reference/v1.0/resources/workinghours.md).
type WorkingHours struct {
	DaysOfWeek []string `json:"daysOfWeek,omitempty"`
	TimeZone   *struct {
		Name string `json:"name,omitempty"`
	} `json:"timeZone,omitempty"`
	StartTime string `json:"startTime,omitempty"`
	EndTime   string `json:"endTime,omitempty"`
}

// ScheduleItem is microsoft.graph.scheduleItem
// (refs/graph/api-reference/v1.0/resources/scheduleitem.md).
//
// Another user's unshared calendar returns only start, end and status: subject,
// location and isPrivate are absent, so they are omitempty and the CLI never
// assumes them (verified live 2026-10-05, handoff §3, F7).
type ScheduleItem struct {
	Start     DateTimeTimeZone `json:"start"`
	End       DateTimeTimeZone `json:"end"`
	Status    string           `json:"status,omitempty"`
	Subject   string           `json:"subject,omitempty"`
	Location  string           `json:"location,omitempty"`
	IsPrivate *bool            `json:"isPrivate,omitzero"`
}

// IsFree reports whether the item is a free slot. The live service returns
// status "free" items for your own calendar, which the CLI drops
// (plans/calendar.md §4.3).
func (s ScheduleItem) IsFree() bool { return strings.EqualFold(s.Status, "free") }

// ListCalendarView calls GET /me/calendarView (userID "") or
// GET /users/{userID}/calendarView and calls fn for every event, following
// @odata.nextLink. fn returns false to stop early.
//
// The window is sent as RFC3339 with an offset, which the live service honours
// (verified live 2026-10-05, handoff §3, F2). Callers must widen it by a day on
// each side and filter the result themselves: all-day events are floating and
// the server matches them to the window as UTC midnight to midnight (handoff §3,
// F5). No Prefer: outlook.timezone is sent (decision D4).
func (c *Client) ListCalendarView(ctx context.Context, userID string, start, end time.Time, fn func(Event) bool) error {
	if fn == nil {
		return errors.New("graph: ListCalendarView needs a callback")
	}
	if end.Before(start) {
		return usageError("graph: the calendar window ends before it starts")
	}
	// The two spellings are not interchangeable in the documentation, so each
	// is chosen deliberately:
	//
	//   - /me/calendarView is what the handoff verified live for the signed-in
	//     user (plans/calendar.md §3, F2). No api-reference page documents that
	//     exact shape (calendar-list-calendarview.md:30 lists
	//     /me/calendar/calendarView) and Microsoft's OpenAPI description does not
	//     declare it at all, which is why it is absent from the Layer 6 route
	//     list and exempted in fakegraph's contract hook.
	//   - /users/{id}/calendar/calendarView is the documented spelling for
	//     someone else's calendar (calendar-list-calendarview.md:33) and the
	//     description declares it, so the shared path is contract-validated.
	path := "/me/calendarView"
	if strings.TrimSpace(userID) != "" {
		path = "/users/" + segment(userID) + "/calendar/calendarView"
	}
	q := url.Values{
		"startDateTime": {rfc3339Offset(start)},
		"endDateTime":   {rfc3339Offset(end)},
		"$top":          {intParam(CalendarViewTop)},
		"$select":       {calendarViewSelect},
	}
	return EachPage[Event](ctx, c, path, q, func(page Page[Event]) (bool, error) {
		for _, ev := range page.Value {
			if !fn(ev) {
				return false, nil
			}
		}
		return true, nil
	})
}

// GetEvent returns one event by id (GET /me/events/{event-id},
// refs/graph/api-reference/v1.0/api/event-get.md). selectFields "" means the
// full event; the Phase 6b pre-check passes eventPreCheckSelect to keep the read
// cheap.
//
// Unlike calendarView, this endpoint returns onlineMeeting without
// isOnlineMeeting being selected (verified live 2026-10-05, handoff §3, F3).
func (c *Client) GetEvent(ctx context.Context, eventID, selectFields string) (Event, error) {
	var ev Event
	if strings.TrimSpace(eventID) == "" {
		return ev, usageError("graph: an event id is required")
	}
	opts := []RequestOption(nil)
	if strings.TrimSpace(selectFields) != "" {
		opts = append(opts, WithQuery(url.Values{"$select": {selectFields}}))
	}
	err := c.Get(ctx, "/me/events/"+segment(eventID), &ev, opts...)
	return ev, err
}

// getScheduleRequest is the documented POST body
// (refs/graph/api-reference/v1.0/api/calendar-getschedule.md:56-80).
type getScheduleRequest struct {
	Schedules                []string         `json:"schedules"`
	StartTime                DateTimeTimeZone `json:"startTime"`
	EndTime                  DateTimeTimeZone `json:"endTime"`
	AvailabilityViewInterval int              `json:"availabilityViewInterval"`
}

type getScheduleResponse struct {
	Value []ScheduleInformation `json:"value"`
}

// GetSchedule calls POST /me/calendar/getSchedule for up to
// MaxScheduleAddresses addresses at a time and returns the schedules in the
// order they were asked for.
//
// start and end are rendered as a wall clock in zone plus the zone's own name,
// which is the form the live service accepted ("Asia/Tokyo" worked); the
// response comes back in UTC regardless (verified live 2026-10-05, handoff §3,
// F7). An interval outside the documented 5..1440 is clamped.
func (c *Client) GetSchedule(ctx context.Context, mails []string, start, end time.Time, zone string, interval int) ([]ScheduleInformation, error) {
	addresses := dedupeStrings(mails)
	if len(addresses) == 0 {
		return nil, usageError("graph: getSchedule needs at least one address")
	}
	if end.Before(start) {
		return nil, usageError("graph: the free/busy window ends before it starts")
	}
	loc, err := zoneLocation(zone)
	if err != nil {
		return nil, err
	}
	if interval <= 0 {
		interval = DefaultScheduleInterval
	}
	if interval < MinScheduleInterval {
		interval = MinScheduleInterval
	}
	if interval > MaxScheduleInterval {
		interval = MaxScheduleInterval
	}
	startPair := wallClock(start, loc, zone)
	endPair := wallClock(end, loc, zone)

	out := make([]ScheduleInformation, 0, len(addresses))
	for chunk := range chunkStrings(addresses, MaxScheduleAddresses) {
		body := getScheduleRequest{
			Schedules:                chunk,
			StartTime:                startPair,
			EndTime:                  endPair,
			AvailabilityViewInterval: interval,
		}
		var resp getScheduleResponse
		if err := c.Post(ctx, "/me/calendar/getSchedule", body, &resp); err != nil {
			return out, err
		}
		out = append(out, resp.Value...)
	}
	return out, nil
}

// FindMeetingChatID resolves a Teams meeting's chat thread id from the join URL
// of an event (GET /me/onlineMeetings with a JoinWebUrl filter,
// refs/graph/api-reference/v1.0/api/onlinemeeting-get.md).
//
// The filter value must be the joinUrl exactly as Graph returned it, percent
// escapes included: it is quoted with ODataQuote and url.Values does the
// encoding (handoff §3, F9).
//
// A join URL that matches nothing is NOT an empty list: Graph answers 400 with
// code BadRequest and a message starting "1026" (verified live 2026-10-05,
// handoff §3, F9). That is "no chat found", not a failure, so this returns
// ("", nil) for any 400 and logs the reason under -v.
func (c *Client) FindMeetingChatID(ctx context.Context, joinURL string) (string, error) {
	if strings.TrimSpace(joinURL) == "" {
		return "", nil
	}
	q := url.Values{
		"$filter": {"JoinWebUrl eq " + ODataQuote(joinURL)},
		"$select": {"id,chatInfo"},
	}
	var page Page[onlineMeetingRecord]
	err := c.Get(ctx, "/me/onlineMeetings", &page, WithQuery(q))
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest {
			c.logf("onlineMeetings lookup answered 400 %s for %s; treating it as no chat", apiErr.Code, joinURL)
			return "", nil
		}
		return "", err
	}
	for _, m := range page.Value {
		if m.ChatInfo != nil && m.ChatInfo.ThreadID != "" {
			return m.ChatInfo.ThreadID, nil
		}
	}
	return "", nil
}

// onlineMeetingRecord is the part of microsoft.graph.onlineMeeting the chat
// lookup needs (refs/graph/api-reference/v1.0/resources/onlinemeeting.md).
type onlineMeetingRecord struct {
	ID       string `json:"id,omitempty"`
	ChatInfo *struct {
		ThreadID     string `json:"threadId,omitempty"`
		MessageID    string `json:"messageId,omitempty"`
		ReplyChainID string `json:"replyChainMessageId,omitempty"`
	} `json:"chatInfo,omitempty"`
}

// SearchEvents runs one event search and returns hits whose ID is already the
// REST id (POST /search/query with entityTypes ["event"]).
//
// Search covers the signed-in user's primary calendar only, takes at most 25
// results per page, does not sort, and its total counts the page, not the
// matches (refs/graph/concepts/search-concept-events.md:97-98).
func (c *Client) SearchEvents(ctx context.Context, query string, from, size int) (SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return SearchResult{}, usageError("graph: event search needs a query string")
	}
	if from < 0 {
		return SearchResult{}, usageError("graph: search offset must not be negative")
	}
	if size <= 0 || size > MaxTopSearch {
		size = DefaultTopSearch
	}
	req := searchEnvelope{Requests: []searchRequest{{
		EntityTypes: []string{"event"},
		From:        &from,
		Size:        &size,
	}}}
	req.Requests[0].Query.QueryString = query

	var wire searchResultWire
	if err := c.Post(ctx, "/search/query", req, &wire); err != nil {
		return SearchResult{}, err
	}
	out := SearchResult{}
	if len(wire.Value) == 0 {
		return out, nil
	}
	resp := wire.Value[0]
	out.SearchTerms = resp.SearchTerms
	if len(resp.HitsContainers) == 0 {
		return out, nil
	}
	container := resp.HitsContainers[0]
	out.Total = container.Total
	out.MoreResultsAvailable = container.MoreResultsAvailable
	out.Hits = make([]SearchHit, 0, len(container.Hits))
	for _, h := range container.Hits {
		hit := SearchHit{HitID: h.HitID, Rank: h.Rank, Summary: h.Summary}
		if id, ok := EventIDFromHitID(h.HitID); ok {
			hit.HitID = id
			hit.Resource.ID = id
		}
		out.Hits = append(out.Hits, hit)
	}
	return out, nil
}

// EventIDFromHitID converts a search hitId into the event id the REST endpoints
// accept.
//
// The two differ only in the base64 alphabet: the search service reports the id
// in the standard alphabet, where "/" and "+" are illegal in a URL path, and the
// REST endpoints are addressed with the URL-safe alphabet. So the conversion is
// exactly two character substitutions — the value is NOT base64-decoded, because
// the id *is* the encoded string.
//
// This is observed behaviour, not documented: it was verified live on 2026-10-05
// (handoff §3, F8). Getting it wrong is quiet rather than loud — a decoded id
// becomes a URL with NUL bytes in it, Graph answers 400, and every hit is
// dropped — which is why the live run of a real tenant found it and the fake,
// which encoded the id the same wrong way, did not.
//
// The bool reports whether the value is usable as an event id at all: an empty
// hitId is not.
func EventIDFromHitID(hitID string) (string, bool) {
	s := strings.TrimSpace(hitID)
	if s == "" {
		return "", false
	}
	// A hitId in standard base64 can contain "/" or "+" (both illegal in a path
	// segment); the URL-safe alphabet replaces them with "-" and "_". "=" padding
	// is legal in a path segment and is left alone.
	if !strings.ContainsAny(s, "/+") {
		return s, true
	}
	return strings.NewReplacer("/", "-", "+", "_").Replace(s), true
}

// AllowedOnlineMeetingProviders returns the mailbox's
// GET /me/calendar -> allowedOnlineMeetingProviders values
// (refs/graph/api-reference/v1.0/api/calendar-get.md).
//
// Phase 6b checks it before `create --teams`: a mailbox without
// teamsForBusiness accepts isOnlineMeeting: true with a 201 and silently drops
// the meeting, so the CLI refuses up front instead (verified live 2026-10-05,
// handoff §3, F10).
func (c *Client) AllowedOnlineMeetingProviders(ctx context.Context) ([]string, error) {
	var cal struct {
		AllowedOnlineMeetingProviders []string `json:"allowedOnlineMeetingProviders"`
	}
	if err := c.Get(ctx, "/me/calendar?$select=allowedOnlineMeetingProviders", &cal); err != nil {
		return nil, err
	}
	return cal.AllowedOnlineMeetingProviders, nil
}

// AllowsTeamsMeetings reports whether a provider list contains teamsForBusiness.
func AllowsTeamsMeetings(providers []string) bool {
	for _, p := range providers {
		if strings.EqualFold(strings.TrimSpace(p), OnlineMeetingProviderTeams) {
			return true
		}
	}
	return false
}

// rfc3339Offset renders an instant with its own offset, which is the form
// startDateTime/endDateTime require: the offset in the value is what Graph
// interprets, not the Prefer header
// (refs/graph/api-reference/v1.0/api/calendar-list-calendarview.md:60).
func rfc3339Offset(t time.Time) string { return t.Format(time.RFC3339) }

// zoneLocation resolves the IANA zone name a request should use.
func zoneLocation(zone string) (*time.Location, error) {
	name := strings.TrimSpace(zone)
	if name == "" || strings.EqualFold(name, "UTC") {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, usageError(fmt.Sprintf("graph: %q is not a known IANA time zone: %v", name, err))
	}
	return loc, nil
}

// wallClock renders a time as the documented dateTimeTimeZone pair: the local
// wall clock without an offset, plus the zone's own name. That is the shape the
// live getSchedule accepted (verified live 2026-10-05, handoff §3, F7).
func wallClock(t time.Time, loc *time.Location, zone string) DateTimeTimeZone {
	name := strings.TrimSpace(zone)
	if name == "" {
		name = "UTC"
	}
	return DateTimeTimeZone{
		DateTime: t.In(loc).Format("2006-01-02T15:04:05"),
		TimeZone: name,
	}
}

// chunkStrings yields successive slices of at most size items.
func chunkStrings(items []string, size int) func(yield func([]string) bool) {
	return func(yield func([]string) bool) {
		for start := 0; start < len(items); start += size {
			end := start + size
			if end > len(items) {
				end = len(items)
			}
			if !yield(items[start:end]) {
				return
			}
		}
	}
}

// dedupeStrings removes empty values and duplicates, keeping the input order.
func dedupeStrings(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, item := range items {
		key := strings.ToLower(strings.TrimSpace(item))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, strings.TrimSpace(item))
	}
	return out
}
