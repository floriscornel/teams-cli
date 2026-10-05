package graph

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/testing/fakegraph"
)

// Tests for the Phase 6 calendar wrappers: internal/graph/calendar.go.
//
// They go through the real client against the real fake Graph server, exactly
// like the read and write tests, and the fake's contract hook validates every
// request and response against the vendored OpenAPI subset. The four calendar
// calls whose paths Microsoft's OpenAPI description does not declare
// (/me/calendarView, /me/calendar/getSchedule, /me/onlineMeetings, /me/calendar)
// are exempted there for the reason internal/testing/contract/routes.go records.

// Calendar seed ids. The events sit in the 2026-01-01..05 window the other
// fixtures use, and the frozen clock is 2026-01-06T12:00Z (readFrozen).
const (
	calMe    = "u-me"
	calAlice = "u-alice"
	calBob   = "u-bob"
	calCarol = "u-carol"
	calDave  = "u-dave"

	calStandup       = "ev-standup"
	calHoliday       = "ev-holiday"
	calAliceOneOnOne = "ev-alice-1on1"
	calBobBusy       = "ev-bob-busy"

	calJoinURL = "https://teams.microsoft.com/l/meetup-join/19%3ameeting_abc%40thread.v2/0?context=%7b%22Tid%22%3a%22x%22%7d"
)

var (
	calJan1 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calJan2 = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	calJan3 = time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)
	calJan6 = time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	calJan7 = time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
)

// calendarModel seeds the users and calendars the calendar tests need.
//
//   - the signed-in user's own calendar carries the events that are asserted;
//   - u-alice's calendar is shared in full (CalendarAccessRead);
//   - u-bob's is shared as free/busy only, which the live service answered with
//     404 ErrorItemNotFound on calendarView (plans/calendar.md §3, F4);
//   - u-carol's is not shared at all (403 ErrorAccessDenied);
//   - u-dave has no mailbox (404 MailboxNotEnabledForRESTAPI, and a 5016
//     schedule error).
func calendarModel() fakegraph.Model {
	return fakegraph.Model{
		Me: calMe,
		Users: []fakegraph.User{
			{ID: calMe, DisplayName: "Me Myself", Mail: "me@contoso.example"},
			{ID: calAlice, DisplayName: "Alice Example", Mail: "alice@contoso.example"},
			{ID: calBob, DisplayName: "Bob Builder", Mail: "bob@contoso.example"},
			{ID: calCarol, DisplayName: "Carol Jones", Mail: "carol@contoso.example"},
			{ID: calDave, DisplayName: "Dave Kim", Mail: "dave@contoso.example", NoMailbox: true},
		},
		CalendarEvents: []fakegraph.CalendarEvent{
			{
				ID: calStandup, Subject: "Daily standup",
				Start: calJan2.Add(10 * time.Hour), End: calJan2.Add(10*time.Hour + 30*time.Minute),
				ShowAs: "busy", Teams: true, JoinURL: calJoinURL, IsOrganizer: true, Location: "Teams",
			},
			{
				ID: calHoliday, Subject: "Company holiday", Kind: fakegraph.CalendarEventAllDay,
				Start: calJan2, Days: 1, ShowAs: "oof", IsOrganizer: true,
			},
			{
				ID: calAliceOneOnOne, OwnerID: calAlice, Subject: "Alice 1:1",
				Start: calJan2.Add(time.Hour), End: calJan2.Add(time.Hour + 30*time.Minute),
				OrganizerName: "Alice Example", IsOrganizer: true,
			},
			{
				ID: calBobBusy, OwnerID: calBob, Subject: "Bob planning",
				Start: calJan2.Add(2 * time.Hour), End: calJan2.Add(3 * time.Hour), ShowAs: "busy",
			},
		},
		CalendarAccess: map[string]string{
			calAlice: fakegraph.CalendarAccessRead,
			calBob:   fakegraph.CalendarAccessFreeBusy,
		},
	}
}

// calendarSetup is the common pair: a contract-checked server seeded with
// calendarModel and the real client.
func calendarSetup(t *testing.T) (*fakegraph.Server, *Client) {
	t.Helper()
	srv := readServer(t, calendarModel())
	return srv, readClient(t, srv)
}

// ---- DateTimeTimeZone -------------------------------------------------------

func TestDateTimeTimeZoneUTC(t *testing.T) {
	cases := map[string]struct {
		in   DateTimeTimeZone
		want string
	}{
		// No Prefer header means Graph answers UTC with no offset: the live
		// form has seven fractional digits (plans/calendar.md §3, F6).
		"graph utc form":    {DateTimeTimeZone{DateTime: "2026-10-06T01:00:00.0000000", TimeZone: "UTC"}, "2026-10-06T01:00:00Z"},
		"no zone means utc": {DateTimeTimeZone{DateTime: "2026-10-06T01:00:00.0000000"}, "2026-10-06T01:00:00Z"},
		"lower case utc":    {DateTimeTimeZone{DateTime: "2026-10-06T01:00:00.0000000", TimeZone: "utc"}, "2026-10-06T01:00:00Z"},
		"no fraction":       {DateTimeTimeZone{DateTime: "2026-10-06T01:00:00", TimeZone: "UTC"}, "2026-10-06T01:00:00Z"},
		"iana zone":         {DateTimeTimeZone{DateTime: "2026-10-07T10:00:00.0000000", TimeZone: "Asia/Tokyo"}, "2026-10-07T01:00:00Z"},
		"explicit offset":   {DateTimeTimeZone{DateTime: "2026-10-07T10:00:00+09:00", TimeZone: ""}, "2026-10-07T01:00:00Z"},
		"new york zone":     {DateTimeTimeZone{DateTime: "2026-10-06T21:00:00.0000000", TimeZone: "America/New_York"}, "2026-10-07T01:00:00Z"},
		"padded values":     {DateTimeTimeZone{DateTime: " 2026-10-06T01:00:00.0000000 ", TimeZone: " UTC "}, "2026-10-06T01:00:00Z"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := tc.in.UTC()
			if err != nil {
				t.Fatalf("UTC: %v", err)
			}
			want, err := time.Parse(time.RFC3339, tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) || got.Location() != time.UTC {
				t.Errorf("UTC() = %s (%s), want %s (UTC)", got, got.Location(), tc.want)
			}
		})
	}
}

func TestDateTimeTimeZoneRejectsBadInput(t *testing.T) {
	cases := map[string]DateTimeTimeZone{
		"empty":         {},
		"blank":         {DateTime: "   "},
		"unknown zone":  {DateTime: "2026-10-06T01:00:00", TimeZone: "Mars/Olympus_Mons"},
		"nonsense time": {DateTime: "not-a-time", TimeZone: "UTC"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := in.UTC(); err == nil {
				t.Fatal("UTC() accepted an unusable pair")
			}
		})
	}
}

func TestDateTimeTimeZoneDate(t *testing.T) {
	cases := map[string]string{
		"2026-10-07T00:00:00.0000000": "2026-10-07",
		"2026-10-07":                  "2026-10-07",
		"short":                       "",
		"":                            "",
	}
	for in, want := range cases {
		if got := (DateTimeTimeZone{DateTime: in}).Date(); got != want {
			t.Errorf("Date(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---- calendarView -----------------------------------------------------------

func TestListCalendarViewSendsTheWindowTopAndSelect(t *testing.T) {
	srv, c := calendarSetup(t)

	var got []Event
	err := c.ListCalendarView(context.Background(), "", calJan2, calJan6, func(ev Event) bool {
		got = append(got, ev)
		return true
	})
	if err != nil {
		t.Fatalf("ListCalendarView: %v", err)
	}
	reqs := srv.RequestsFor(http.MethodGet, "/me/calendarView")
	if len(reqs) != 1 {
		t.Fatalf("%d requests to /me/calendarView, want 1", len(reqs))
	}
	q := reqs[0].Query
	if want := calJan2.Format(time.RFC3339); q.Get("startDateTime") != want {
		t.Errorf("startDateTime = %q, want %q", q.Get("startDateTime"), want)
	}
	if want := calJan6.Format(time.RFC3339); q.Get("endDateTime") != want {
		t.Errorf("endDateTime = %q, want %q", q.Get("endDateTime"), want)
	}
	if got := q.Get("$top"); got != "100" {
		t.Errorf("$top = %q, want 100", got)
	}
	// isOnlineMeeting must stay in $select or calendarView omits onlineMeeting
	// (verified live 2026-10-05, handoff §3, F3).
	sel := "," + q.Get("$select") + ","
	for _, field := range []string{
		"id", "subject", "start", "end", "isAllDay", "isCancelled", "showAs", "organizer",
		"isOrganizer", "isOnlineMeeting", "onlineMeeting", "onlineMeetingProvider", "location",
		"responseStatus", "webLink", "type", "seriesMasterId",
	} {
		if !strings.Contains(sel, ","+field+",") {
			t.Errorf("$select is missing %q: %s", field, q.Get("$select"))
		}
	}

	// The all-day event and the Teams meeting both came back, and the wrapper
	// hands back UTC instants regardless of the window's zone.
	byID := map[string]Event{}
	for _, ev := range got {
		byID[ev.ID] = ev
	}
	standup, ok := byID[calStandup]
	if !ok {
		t.Fatalf("the standup is missing from %d events", len(got))
	}
	if !standup.IsTeamsMeeting() || standup.JoinURL() != calJoinURL {
		t.Errorf("standup = %+v, want a Teams meeting with the join URL", standup)
	}
	start, err := standup.Start.UTC()
	if err != nil {
		t.Fatal(err)
	}
	if !start.Equal(calJan2.Add(10 * time.Hour)) {
		t.Errorf("start = %s, want %s", start, calJan2.Add(10*time.Hour))
	}
	holiday, ok := byID[calHoliday]
	if !ok {
		t.Fatal("the all-day event is missing")
	}
	if !holiday.IsAllDay {
		t.Error("isAllDay = false for the all-day event")
	}
	if got, want := holiday.Start.Date(), "2026-01-02"; got != want {
		t.Errorf("all-day Date() = %q, want %q", got, want)
	}
	if !strings.HasPrefix(holiday.Start.DateTime, "2026-01-02T00:00:00") {
		t.Errorf("all-day dateTime = %q, want midnight of the event's own date", holiday.Start.DateTime)
	}
}

func TestListCalendarViewPagesUntilTheWindowIsCovered(t *testing.T) {
	// 250 events is more than the two $top=100 pages the wrapper asks for.
	model := calendarModel()
	for i := 0; i < 250; i++ {
		model.CalendarEvents = append(model.CalendarEvents, fakegraph.CalendarEvent{
			ID: "ev-fill-" + itoa(i), Subject: "fill", Start: calJan2.Add(time.Duration(i) * time.Minute),
			End: calJan2.Add(time.Duration(i)*time.Minute + time.Minute),
		})
	}
	srv := readServer(t, model)
	c := readClient(t, srv)

	var count int
	if err := c.ListCalendarView(context.Background(), "", calJan2, calJan6, func(Event) bool {
		count++
		return true
	}); err != nil {
		t.Fatalf("ListCalendarView: %v", err)
	}
	if count < 200 {
		t.Errorf("walked %d events, want every page (>= 200)", count)
	}
	if pages := srv.RequestsFor(http.MethodGet, "/me/calendarView"); len(pages) < 3 {
		t.Errorf("%d pages requested, want at least 3", len(pages))
	}
}

func TestListCalendarViewOtherUserAccess(t *testing.T) {
	_, c := calendarSetup(t)

	t.Run("shared calendar returns events", func(t *testing.T) {
		var count int
		err := c.ListCalendarView(context.Background(), calAlice, calJan2, calJan6, func(Event) bool {
			count++
			return true
		})
		if err != nil {
			t.Fatalf("ListCalendarView(u-alice): %v", err)
		}
		if count != 1 {
			t.Errorf("the shared calendar returned %d events, want exactly Alice's 1:1", count)
		}
	})

	cases := map[string]struct {
		user   string
		status int
		code   string
	}{
		"free/busy only is 404 ErrorItemNotFound": {calBob, http.StatusNotFound, "ErrorItemNotFound"},
		"unshared is 403 ErrorAccessDenied":       {calCarol, http.StatusForbidden, "ErrorAccessDenied"},
		"no mailbox is 404 MailboxNotEnabled":     {calDave, http.StatusNotFound, "MailboxNotEnabledForRESTAPI"},
		"unknown user is 404 ErrorInvalidUser":    {"nobody@contoso.example", http.StatusNotFound, "ErrorInvalidUser"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := c.ListCalendarView(context.Background(), tc.user, calJan2, calJan6, func(Event) bool { return true })
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("ListCalendarView(%s) = %v, want an *APIError", tc.user, err)
			}
			if apiErr.Status != tc.status || apiErr.Code != tc.code {
				t.Errorf("got %d %s, want %d %s", apiErr.Status, apiErr.Code, tc.status, tc.code)
			}
		})
	}
}

func TestListCalendarViewStopsOnFalseAndValidates(t *testing.T) {
	_, c := calendarSetup(t)

	var count int
	if err := c.ListCalendarView(context.Background(), "", calJan1, calJan7, func(Event) bool {
		count++
		return false
	}); err != nil {
		t.Fatalf("ListCalendarView: %v", err)
	}
	if count != 1 {
		t.Errorf("callback ran %d times, want 1 (it returned false)", count)
	}
	if err := c.ListCalendarView(context.Background(), "", calJan6, calJan2, func(Event) bool { return true }); err == nil {
		t.Error("a window that ends before it starts was accepted")
	}
	if err := c.ListCalendarView(context.Background(), "", calJan2, calJan6, nil); err == nil {
		t.Error("a nil callback was accepted")
	}
}

// ---- getSchedule ------------------------------------------------------------

func TestGetScheduleChunksAtTwentyAddresses(t *testing.T) {
	cases := map[string]struct {
		addresses int
		wantCalls int
	}{
		"one":            {1, 1},
		"exactly twenty": {20, 1},
		"twenty one":     {21, 2},
		"forty one":      {41, 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := readServer(t, calendarModel())
			c := readClient(t, srv)
			mails := make([]string, 0, tc.addresses)
			for i := 0; i < tc.addresses; i++ {
				mails = append(mails, "user"+itoa(i)+"@contoso.example")
			}
			out, err := c.GetSchedule(context.Background(), mails, calJan2, calJan3, "UTC", 30)
			if err != nil {
				t.Fatalf("GetSchedule: %v", err)
			}
			calls := srv.RequestsFor(http.MethodPost, "/me/calendar/getSchedule")
			if len(calls) != tc.wantCalls {
				t.Errorf("%d addresses took %d call(s), want %d", tc.addresses, len(calls), tc.wantCalls)
			}
			if len(out) != tc.addresses {
				t.Errorf("got %d schedules, want %d", len(out), tc.addresses)
			}
		})
	}
}

func TestGetScheduleSendsWallClockInTheRequestedZone(t *testing.T) {
	srv := readServer(t, calendarModel())
	c := readClient(t, srv)
	if _, err := c.GetSchedule(context.Background(), []string{"me@contoso.example"}, calJan2, calJan3, "Asia/Tokyo", 30); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	calls := srv.RequestsFor(http.MethodPost, "/me/calendar/getSchedule")
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	body := string(calls[0].Body)
	// The documented request pair is a local wall clock with no offset plus the
	// zone's own IANA name (plans/calendar.md §3, F7).
	if !strings.Contains(body, `"timeZone":"Asia/Tokyo"`) {
		t.Errorf("request body does not name the zone: %s", body)
	}
	if strings.Contains(body, "+09:00") || strings.Contains(body, "Z\"") {
		t.Errorf("request body carries an offset instead of a wall clock: %s", body)
	}
	if !strings.Contains(body, `"availabilityViewInterval":30`) {
		t.Errorf("request body does not carry the interval: %s", body)
	}
}

func TestGetScheduleMailboxErrorsAndPrivacy(t *testing.T) {
	_, c := calendarSetup(t)
	out, err := c.GetSchedule(context.Background(),
		[]string{"me@contoso.example", "dave@contoso.example", "bob@contoso.example"},
		calJan2, calJan3, "Asia/Tokyo", 30)
	if err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d schedules, want 3", len(out))
	}
	// A user with no mailbox answers "5016" with a null scheduleItems; the two
	// cases cannot be told apart, so a null list must not panic
	// (plans/calendar.md §3, F7).
	if out[1].Error == nil || out[1].Error.ResponseCode != NoMailboxResponseCode {
		t.Errorf("dave's schedule error = %+v, want responseCode %q", out[1].Error, NoMailboxResponseCode)
	}
	if out[1].ScheduleItems != nil {
		t.Errorf("dave's scheduleItems = %v, want null", *out[1].ScheduleItems)
	}
	if len(out[1].Items()) != 0 {
		t.Errorf("Items() on a null scheduleItems = %v, want none", out[1].Items())
	}
	// Your own calendar carries subject and location; another user's unshared
	// one carries only start, end and status (plans/calendar.md §3, F7).
	own := out[0].Items()
	if len(own) == 0 {
		t.Fatal("the signed-in user's schedule is empty")
	}
	if own[0].Subject == "" {
		t.Error("your own schedule should carry subjects")
	}
	unshared := out[2].Items()
	if len(unshared) == 0 {
		t.Fatal("bob's schedule is empty")
	}
	for i, item := range unshared {
		if item.Subject != "" || item.Location != "" || item.IsPrivate != nil {
			t.Errorf("unshared item %d leaks %+v; only start, end and status are documented", i, item)
		}
		if item.Status == "" {
			t.Errorf("unshared item %d has no status", i)
		}
	}
}

func TestGetScheduleValidatesItsArguments(t *testing.T) {
	_, c := calendarSetup(t)
	if _, err := c.GetSchedule(context.Background(), nil, calJan2, calJan3, "UTC", 30); err == nil {
		t.Error("an empty address list was accepted")
	}
	if _, err := c.GetSchedule(context.Background(), []string{"me@contoso.example"}, calJan3, calJan2, "UTC", 30); err == nil {
		t.Error("a window that ends before it starts was accepted")
	}
	if _, err := c.GetSchedule(context.Background(), []string{"me@contoso.example"}, calJan2, calJan3, "Mars/Olympus_Mons", 30); err == nil {
		t.Error("an unknown IANA zone was accepted")
	}
	// Duplicates collapse, so the same address is not asked for twice.
	srv := readServer(t, calendarModel())
	c2 := readClient(t, srv)
	if _, err := c2.GetSchedule(context.Background(), []string{"me@contoso.example", "ME@contoso.example", " me@contoso.example "}, calJan2, calJan3, "UTC", 30); err != nil {
		t.Fatalf("GetSchedule: %v", err)
	}
	if calls := srv.RequestsFor(http.MethodPost, "/me/calendar/getSchedule"); len(calls) != 1 {
		t.Errorf("%d calls, want 1 (the three addresses are the same mailbox)", len(calls))
	}
}

// ---- event reads ------------------------------------------------------------

func TestGetEventReturnsTheJoinURL(t *testing.T) {
	_, c := calendarSetup(t)
	ev, err := c.GetEvent(context.Background(), calStandup, "")
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if ev.ID != calStandup || ev.Subject != "Daily standup" {
		t.Errorf("event = %+v", ev)
	}
	if !ev.IsTeamsMeeting() {
		t.Errorf("IsTeamsMeeting() = false for %+v", ev)
	}
	if ev.JoinURL() == "" {
		t.Error("JoinURL() is empty")
	}
	if ev.Organizer.Name() == "" {
		t.Error("organizer name is empty")
	}
	if got := ev.SubjectLine(); got != "Daily standup" {
		t.Errorf("SubjectLine() = %q", got)
	}
	if got := (Event{}).SubjectLine(); got != "(no subject)" {
		t.Errorf("SubjectLine() on an empty event = %q, want the placeholder", got)
	}
}

func TestGetEventPreCheckSelect(t *testing.T) {
	srv, c := calendarSetup(t)
	ev, err := c.GetEvent(context.Background(), calStandup, eventPreCheckSelect)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	reqs := srv.RequestsFor(http.MethodGet, "/me/events/"+calStandup)
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	if got := reqs[0].Query.Get("$select"); got != eventPreCheckSelect {
		t.Errorf("$select = %q, want %q", got, eventPreCheckSelect)
	}
	if !ev.IsOrganizer || !ev.IsOnlineMeeting {
		t.Errorf("the pre-check fields did not come back: %+v", ev)
	}
}

func TestGetEventUnknownID(t *testing.T) {
	_, c := calendarSetup(t)
	if _, err := c.GetEvent(context.Background(), "does-not-exist", ""); err == nil {
		t.Fatal("GetEvent accepted an unknown id")
	}
	if _, err := c.GetEvent(context.Background(), "", ""); err == nil {
		t.Fatal("GetEvent accepted an empty id")
	}
}

func TestAllowedOnlineMeetingProviders(t *testing.T) {
	_, c := calendarSetup(t)
	providers, err := c.AllowedOnlineMeetingProviders(context.Background())
	if err != nil {
		t.Fatalf("AllowedOnlineMeetingProviders: %v", err)
	}
	if !AllowsTeamsMeetings(providers) {
		t.Errorf("providers = %v, want teamsForBusiness", providers)
	}
	for _, no := range [][]string{nil, {}, {"skypeForBusiness"}} {
		if AllowsTeamsMeetings(no) {
			t.Errorf("AllowsTeamsMeetings(%v) = true, want false", no)
		}
	}
}

// ---- meeting chat -----------------------------------------------------------

func TestFindMeetingChatID(t *testing.T) {
	_, c := calendarSetup(t)
	chat, err := c.FindMeetingChatID(context.Background(), calJoinURL)
	if err != nil {
		t.Fatalf("FindMeetingChatID: %v", err)
	}
	if !strings.HasPrefix(chat, "19:meeting_") || !strings.HasSuffix(chat, "@thread.v2") {
		t.Errorf("chat id = %q, want the documented 19:meeting_…@thread.v2 form", chat)
	}
}

func TestFindMeetingChatIDNoMatchIsNotAnError(t *testing.T) {
	_, c := calendarSetup(t)
	// A URL that matches nothing is a 400, not an empty list; the CLI treats it
	// as "no chat found" (plans/calendar.md §3, F9).
	chat, err := c.FindMeetingChatID(context.Background(), "https://teams.microsoft.com/l/meetup-join/nothing-here")
	if err != nil {
		t.Fatalf("a non-matching join URL must not be an error: %v", err)
	}
	if chat != "" {
		t.Errorf("chat id = %q, want empty", chat)
	}
	if got, err := c.FindMeetingChatID(context.Background(), ""); err != nil || got != "" {
		t.Errorf("an empty join URL = (%q, %v), want empty and nil", got, err)
	}
}

// ---- search -----------------------------------------------------------------

func TestEventIDFromHitID(t *testing.T) {
	// The live hitIds are standard base64 of the event id, so they can contain
	// "/" and "+"; the fake produces the same form (fakegraph.StandardBase64).
	// Most real ids contain neither, which is exactly why the conversion must
	// leave an id that has no such character alone.
	restID := "AAMkADEwODY2NzllLTQ3MmEtNGRlMC05ZTUyLTE4ZDRhYmU1ZGM3NABGAAAAAAA3+iYQBnJnQabRVDelNhnzBwAejhWkAOAxQ6M4c1c9NwfrAAAAAAENAAAejhWkAOAxQ6M4c1c9NwfrAABbUZLJAAA="
	standard := fakegraph.StandardBase64(restID)
	got, ok := EventIDFromHitID(standard)
	if !ok {
		t.Fatal("EventIDFromHitID rejected a standard-base64 id")
	}
	if got != restID {
		t.Errorf("EventIDFromHitID = %q, want %q", got, restID)
	}
	// An already URL-safe value is not the standard encoding, so it is left
	// alone rather than decoded into nonsense.
	if got, ok := EventIDFromHitID("AAM-k-abc_123"); !ok || got != "AAM-k-abc_123" {
		t.Errorf("URL-safe id = (%q, %v), want it unchanged", got, ok)
	}
	if _, ok := EventIDFromHitID(""); ok {
		t.Error("an empty hitId was accepted")
	}
	if _, ok := EventIDFromHitID("   "); ok {
		t.Error("a blank hitId was accepted")
	}
	// A value that only looks encoded is returned unchanged.
	if got, ok := EventIDFromHitID("not/base64/@@@"); !ok || got != "not/base64/@@@" {
		t.Errorf("non-base64 value = (%q, %v), want it unchanged", got, ok)
	}
}

func TestSearchEventsConvertsHitIDs(t *testing.T) {
	srv, c := calendarSetup(t)
	res, err := c.SearchEvents(context.Background(), "standup", 0, 25)
	if err != nil {
		t.Fatalf("SearchEvents: %v", err)
	}
	if len(res.Hits) == 0 {
		t.Fatal("no hits for an event that exists")
	}
	// The search service reports the event id in standard base64; the wrapper
	// hands back the id the REST endpoints accept, so it must equal the seeded
	// event id verbatim (plans/calendar.md §3, F8).
	var sawRestID bool
	for _, hit := range res.Hits {
		if hit.HitID == calStandup {
			sawRestID = true
		}
		if id, ok := EventIDFromHitID(fakegraph.StandardBase64(hit.HitID)); !ok || id != hit.HitID {
			t.Errorf("hitId %q does not round-trip through the search encoding", hit.HitID)
		}
	}
	if !sawRestID {
		t.Errorf("no hit mapped back to the seeded event id %q", calStandup)
	}
	// total counts the page, not the matches
	// (refs/graph/concepts/search-concept-events.md:97).
	if res.Total != len(res.Hits) {
		t.Errorf("total = %d, want the page size %d", res.Total, len(res.Hits))
	}
	if res.MoreResultsAvailable {
		t.Error("moreResultsAvailable = true for a single-hit search")
	}
	// The request asks for the event entity type only.
	reqs := srv.RequestsFor(http.MethodPost, "/search/query")
	if len(reqs) == 0 {
		t.Fatal("no search request was recorded")
	}
	if body := string(reqs[len(reqs)-1].Body); !strings.Contains(body, `"entityTypes":["event"]`) {
		t.Errorf("search body does not ask for events: %s", body)
	}
	if _, err := c.SearchEvents(context.Background(), "  ", 0, 25); err == nil {
		t.Error("an empty query was accepted")
	}
	if _, err := c.SearchEvents(context.Background(), "x", -1, 25); err == nil {
		t.Error("a negative offset was accepted")
	}
}

// itoa renders a small int without pulling strconv into the test's imports.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}
