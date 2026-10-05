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

// Tests for the Phase 6b calendar writes: internal/graph/calendar_write.go.
//
// They go through the real client against the real fake Graph server, and the
// contract hook validates every request and response against the vendored
// OpenAPI subset, so a body that drifts from the documented shape fails here.

// writeCalendarModel seeds the events the write tests act on. The signed-in user
// organizes the standup and is only an invitee of the review, which is the
// distinction every pre-check turns on.
func writeCalendarModel() fakegraph.Model {
	model := calendarModel()
	model.CalendarEvents = append(model.CalendarEvents, fakegraph.CalendarEvent{
		ID: "ev-invited", Subject: "Someone else's meeting",
		Start: calJan2.Add(15 * time.Hour), End: calJan2.Add(16 * time.Hour),
		OrganizerName: "Bob Builder", IsOrganizer: false, AllowNewTimeProposals: boolPtr(false),
	})
	return model
}

func boolPtr(v bool) *bool { return &v }

func writeCalendarSetup(t *testing.T) (*fakegraph.Server, *Client) {
	t.Helper()
	srv := readServer(t, writeCalendarModel())
	return srv, readClient(t, srv)
}

func TestCreateEvent(t *testing.T) {
	srv, c := writeCalendarSetup(t)
	start := calJan3.Add(9 * time.Hour)
	ev, err := c.CreateEvent(context.Background(), EventCreate{
		Subject:       "New meeting",
		Start:         start,
		Duration:      45 * time.Minute,
		Zone:          "UTC",
		Location:      "Room 2",
		Body:          "agenda",
		Attendees:     []Recipient{{EmailAddress: &EmailAddress{Name: "Alice", Address: "alice@contoso.example"}}},
		TransactionID: "txn-1",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if ev.Subject != "New meeting" || ev.ID == "" {
		t.Errorf("event = %+v", ev)
	}
	reqs := srv.RequestsFor(http.MethodPost, "/me/calendar/events")
	if len(reqs) != 1 {
		t.Fatalf("%d create requests, want 1", len(reqs))
	}
	body := string(reqs[0].Body)
	for _, want := range []string{`"subject":"New meeting"`, `"transactionId":"txn-1"`, `"isAllDay"`, `"attendees"`, `"location"`, `"body"`} {
		if want == `"isAllDay"` {
			// isAllDay is omitempty and false here, so it must NOT be sent.
			if strings.Contains(body, want) {
				t.Errorf("an all-day flag was sent for a timed event: %s", body)
			}
			continue
		}
		if !strings.Contains(body, want) {
			t.Errorf("the create body is missing %s: %s", want, body)
		}
	}
	// The start is a wall clock in the named zone, not an instant with an offset.
	if strings.Contains(body, "+00:00") || strings.Contains(body, "Z\"") {
		t.Errorf("the create body carries an offset instead of a wall clock: %s", body)
	}
}

func TestCreateEventDeduplicatesOnTransactionID(t *testing.T) {
	_, c := writeCalendarSetup(t)
	in := EventCreate{
		Subject: "Retry me", Start: calJan3.Add(9 * time.Hour), Duration: 30 * time.Minute,
		Zone: "UTC", TransactionID: "txn-retry",
	}
	first, err := c.CreateEvent(context.Background(), in)
	if err != nil {
		t.Fatalf("first CreateEvent: %v", err)
	}
	// The same body and transactionId must return the SAME event, which is what
	// makes the call safe to retry (plans/calendar.md §3, F10).
	second, err := c.CreateEvent(context.Background(), in)
	if err != nil {
		t.Fatalf("second CreateEvent: %v", err)
	}
	if first.ID != second.ID {
		t.Errorf("a retried create made a second event: %s then %s", first.ID, second.ID)
	}
}

func TestCreateEventAllDay(t *testing.T) {
	_, c := writeCalendarSetup(t)
	day := time.Date(2026, 1, 7, 0, 0, 0, 0, time.UTC)
	ev, err := c.CreateEvent(context.Background(), EventCreate{
		Subject: "Holiday", Start: day, End: day.AddDate(0, 0, 1), AllDay: true, Zone: "UTC",
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if !ev.IsAllDay {
		t.Error("isAllDay = false")
	}
	if ev.Start.Date() != "2026-01-07" {
		t.Errorf("all-day start = %q, want 2026-01-07", ev.Start.DateTime)
	}
}

func TestCreateEventTeamsMeeting(t *testing.T) {
	_, c := writeCalendarSetup(t)
	ev, err := c.CreateEvent(context.Background(), EventCreate{
		Subject: "Online", Start: calJan3.Add(9 * time.Hour), Duration: 30 * time.Minute,
		Zone: "UTC", Teams: true,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if ev.JoinURL() == "" {
		t.Error("a Teams meeting came back without a join URL")
	}
	if !ev.IsTeamsMeeting() {
		t.Errorf("IsTeamsMeeting() = false for %+v", ev)
	}
}

func TestCreateEventSilentlyDropsTeamsOnANonTeamsMailbox(t *testing.T) {
	// A mailbox that cannot create Teams meetings accepts the create and returns
	// no joinUrl, which is why the CLI checks the provider list first
	// (plans/calendar.md §3, F10).
	model := writeCalendarModel()
	for i := range model.Users {
		if model.Users[i].ID == calMe {
			model.Users[i].OnlineMeetingProviders = []string{"skypeForBusiness"}
		}
	}
	srv := readServer(t, model)
	c := readClient(t, srv)

	providers, err := c.AllowedOnlineMeetingProviders(context.Background())
	if err != nil {
		t.Fatalf("AllowedOnlineMeetingProviders: %v", err)
	}
	if graphAllows(providers) {
		t.Fatal("the seeded mailbox should not allow Teams meetings")
	}
	ev, err := c.CreateEvent(context.Background(), EventCreate{
		Subject: "No teams", Start: calJan3.Add(9 * time.Hour), Duration: 30 * time.Minute,
		Zone: "UTC", Teams: true,
	})
	if err != nil {
		t.Fatalf("CreateEvent: %v", err)
	}
	if ev.JoinURL() != "" {
		t.Errorf("join URL = %q, want empty on a mailbox without teamsForBusiness", ev.JoinURL())
	}
}

// graphAllows is AllowsTeamsMeetings, named locally so the test reads clearly.
func graphAllows(providers []string) bool { return AllowsTeamsMeetings(providers) }

func TestCreateEventValidatesItsArguments(t *testing.T) {
	_, c := writeCalendarSetup(t)
	cases := map[string]EventCreate{
		"no subject":       {Start: calJan3, Zone: "UTC"},
		"no start":         {Subject: "x", Zone: "UTC"},
		"end before start": {Subject: "x", Start: calJan3.Add(time.Hour), End: calJan3, Zone: "UTC"},
		"bad zone":         {Subject: "x", Start: calJan3, Zone: "Mars/Olympus"},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := c.CreateEvent(context.Background(), in); err == nil {
				t.Fatal("CreateEvent accepted an unusable event")
			}
		})
	}
}

func TestUpdateEventSendsOnlyTheFieldsGiven(t *testing.T) {
	srv, c := writeCalendarSetup(t)
	subject := "Renamed"
	ev, err := c.UpdateEvent(context.Background(), calStandup, EventUpdate{Subject: &subject, Zone: "UTC"})
	if err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	if ev.Subject != subject {
		t.Errorf("subject = %q, want %q", ev.Subject, subject)
	}
	reqs := srv.RequestsFor(http.MethodPatch, "/me/events/"+calStandup)
	if len(reqs) != 1 {
		t.Fatalf("%d patch requests, want 1", len(reqs))
	}
	body := string(reqs[0].Body)
	if !strings.Contains(body, `"subject":"Renamed"`) {
		t.Errorf("the patch body is missing the subject: %s", body)
	}
	// body and start must NOT travel, because rewriting them can drop the Teams
	// meeting blob and move the meeting (plans/calendar.md §4.7).
	for _, unwanted := range []string{`"body"`, `"start"`, `"end"`, `"location"`} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the patch body carries %s, which the caller did not set: %s", unwanted, body)
		}
	}
}

func TestUpdateEventWithANewStartDerivesTheEnd(t *testing.T) {
	srv, c := writeCalendarSetup(t)
	start := calJan3.Add(11 * time.Hour)
	if _, err := c.UpdateEvent(context.Background(), calStandup, EventUpdate{
		Start: &start, Duration: time.Hour, Zone: "UTC",
	}); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	reqs := srv.RequestsFor(http.MethodPatch, "/me/events/"+calStandup)
	if len(reqs) != 1 {
		t.Fatalf("%d patch requests, want 1", len(reqs))
	}
	body := string(reqs[0].Body)
	if !strings.Contains(body, `"start"`) || !strings.Contains(body, `"end"`) {
		t.Errorf("a start change must carry an end as well: %s", body)
	}
	if !strings.Contains(body, "11:00:00") || !strings.Contains(body, "12:00:00") {
		t.Errorf("the derived end is wrong: %s", body)
	}
}

func TestUpdateEventUnknownID(t *testing.T) {
	_, c := writeCalendarSetup(t)
	subject := "x"
	if _, err := c.UpdateEvent(context.Background(), "nope", EventUpdate{Subject: &subject}); err == nil {
		t.Fatal("UpdateEvent accepted an unknown id")
	}
	if _, err := c.UpdateEvent(context.Background(), "", EventUpdate{Subject: &subject}); err == nil {
		t.Fatal("UpdateEvent accepted an empty id")
	}
}

func TestRespondEventAcceptsAndDeclines(t *testing.T) {
	_, c := writeCalendarSetup(t)
	// ev-invited is the one event the signed-in user does not organize.
	if err := c.RespondEvent(context.Background(), "ev-invited", ResponseAccept, "looking forward", true, nil, "UTC"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	ev, err := c.GetEvent(context.Background(), "ev-invited", "")
	if err != nil {
		t.Fatal(err)
	}
	if ev.ResponseStatus == nil || ev.ResponseStatus.Response != "accepted" {
		t.Errorf("response = %+v, want accepted", ev.ResponseStatus)
	}

	// Declining moves the event to Deleted Items, so it leaves the calendar.
	if err := c.RespondEvent(context.Background(), "ev-invited", ResponseDecline, "cannot make it", true, nil, "UTC"); err != nil {
		t.Fatalf("decline: %v", err)
	}
	if _, err := c.GetEvent(context.Background(), "ev-invited", ""); err == nil {
		t.Error("the declined event is still readable")
	}
}

func TestRespondEventRefusesYourOwnMeeting(t *testing.T) {
	_, c := writeCalendarSetup(t)
	err := c.RespondEvent(context.Background(), calStandup, ResponseAccept, "", true, nil, "UTC")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("responding to your own meeting = %v, want an *APIError", err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "ErrorInvalidRequest" {
		t.Errorf("got %d %s, want 400 ErrorInvalidRequest", apiErr.Status, apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "organizer") {
		t.Errorf("message = %q, want it to name the organizer", apiErr.Message)
	}
}

func TestRespondEventProposedTimeRules(t *testing.T) {
	_, c := writeCalendarSetup(t)
	slot := &TimeSlot{Start: calJan3.Add(9 * time.Hour), End: calJan3.Add(10 * time.Hour)}

	// A proposed time with sendResponse:false is a 400 ErrorInvalidParameter
	// (plans/calendar.md §3, F10).
	err := c.RespondEvent(context.Background(), "ev-invited", ResponseTentative, "maybe", false, slot, "UTC")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("proposed time with no response = %v, want an *APIError", err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "ErrorInvalidParameter" {
		t.Errorf("got %d %s, want 400 ErrorInvalidParameter", apiErr.Status, apiErr.Code)
	}
	// With a response it works.
	if err := c.RespondEvent(context.Background(), "ev-invited", ResponseTentative, "maybe", true, slot, "UTC"); err != nil {
		t.Fatalf("tentative with a proposed time: %v", err)
	}
}

func TestRespondEventRejectsAnUnknownKind(t *testing.T) {
	_, c := writeCalendarSetup(t)
	if err := c.RespondEvent(context.Background(), "ev-invited", EventResponse("maybe"), "", true, nil, "UTC"); err == nil {
		t.Fatal("an unknown response kind was accepted")
	}
	if err := c.RespondEvent(context.Background(), "", ResponseAccept, "", true, nil, "UTC"); err == nil {
		t.Fatal("an empty event id was accepted")
	}
}

func TestCancelEventOrganizerOnly(t *testing.T) {
	_, c := writeCalendarSetup(t)
	if err := c.CancelEvent(context.Background(), calStandup, "sorry"); err != nil {
		t.Fatalf("CancelEvent: %v", err)
	}
	ev, err := c.GetEvent(context.Background(), calStandup, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ev.IsCancelled {
		t.Error("isCancelled = false after a cancel")
	}
	// An attendee cannot cancel.
	err = c.CancelEvent(context.Background(), "ev-invited", "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("cancelling someone else's meeting = %v, want an *APIError", err)
	}
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "ErrorInvalidRequest" {
		t.Errorf("got %d %s, want 400 ErrorInvalidRequest", apiErr.Status, apiErr.Code)
	}
	// An unknown id is a 404.
	if err := c.CancelEvent(context.Background(), "nope", ""); err == nil {
		t.Error("cancelling an unknown event succeeded")
	}
}

func TestDeleteEventThen404(t *testing.T) {
	_, c := writeCalendarSetup(t)
	if err := c.DeleteEvent(context.Background(), calHoliday); err != nil {
		t.Fatalf("DeleteEvent: %v", err)
	}
	// A second delete is a 404 ErrorItemNotFound (plans/calendar.md §3, F10).
	err := c.DeleteEvent(context.Background(), calHoliday)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("second delete = %v, want an *APIError", err)
	}
	if apiErr.Status != http.StatusNotFound {
		t.Errorf("status = %d, want 404", apiErr.Status)
	}
	if err := c.DeleteEvent(context.Background(), ""); err == nil {
		t.Error("DeleteEvent accepted an empty id")
	}
}

func TestPreCheckEventReadsTheThreeFields(t *testing.T) {
	srv, c := writeCalendarSetup(t)
	pre, err := c.PreCheckEvent(context.Background(), calStandup)
	if err != nil {
		t.Fatalf("PreCheckEvent: %v", err)
	}
	if !pre.IsOrganizer {
		t.Error("isOrganizer = false for the user's own event")
	}
	if pre.Subject == "" {
		t.Error("subject is empty")
	}
	// allowNewTimeProposals defaults to true when Graph omits it.
	if !pre.AllowNewTimeProposals {
		t.Error("allowNewTimeProposals = false, want the documented default of true")
	}
	reqs := srv.RequestsFor(http.MethodGet, "/me/events/"+calStandup)
	if len(reqs) != 1 {
		t.Fatalf("%d pre-check requests, want 1", len(reqs))
	}
	if got := reqs[0].Query.Get("$select"); got != EventPreCheckSelect {
		t.Errorf("$select = %q, want %q", got, EventPreCheckSelect)
	}

	// An event the user does not organize reports false, and this one refuses
	// proposals.
	other, err := c.PreCheckEvent(context.Background(), "ev-invited")
	if err != nil {
		t.Fatal(err)
	}
	if other.IsOrganizer {
		t.Error("isOrganizer = true for someone else's event")
	}
	if other.AllowNewTimeProposals {
		t.Error("allowNewTimeProposals = true for an event that refuses proposals")
	}
}
