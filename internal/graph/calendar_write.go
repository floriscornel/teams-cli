package graph

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// This file wraps the Phase 6b calendar writes: create, update, the four
// responses, cancel and delete (plans/calendar.md §4.7).
//
// Refs: refs/graph/api-reference/v1.0/api/calendar-post-events.md,
// refs/graph/api-reference/v1.0/api/event-update.md,
// refs/graph/api-reference/v1.0/api/event-delete.md,
// refs/graph/api-reference/v1.0/api/event-accept.md,
// refs/graph/api-reference/v1.0/api/event-tentativelyaccept.md,
// refs/graph/api-reference/v1.0/api/event-decline.md,
// refs/graph/api-reference/v1.0/api/event-cancel.md.

// EventCreate is the body of `calendar create`.
type EventCreate struct {
	// Subject is the event title.
	Subject string
	// Start and End are instants; the wire body renders them as a wall clock in
	// the event's own zone (plans/calendar.md §4.7).
	Start time.Time
	// End is the instant the event ends. A zero End means Start plus Duration.
	End time.Time
	// Duration fills in End when End is zero.
	Duration time.Duration
	// AllDay makes the event a floating all-day event: both ends are sent as
	// midnight of their dates, and the zone is the one the caller names.
	AllDay bool
	// Zone is the IANA zone name both ends carry.
	Zone string
	// Attendees are the required attendees; each gets an invitation.
	Attendees []Recipient
	// Location is the free-text location.
	Location string
	// Body is the event body; empty means no body property is sent at all.
	Body string
	// Teams asks for a Teams online meeting. The caller checks the mailbox first
	// (plans/calendar.md §3, F10).
	Teams bool
	// TransactionID deduplicates a retried create (plans/calendar.md §3, F10).
	TransactionID string
}

// eventCreateWire is the POST body. start and end are dateTimeTimeZone as
// documented, and every other property is omitted when empty so the request says
// exactly what the user asked for.
type eventCreateWire struct {
	Subject           string            `json:"subject,omitempty"`
	Start             *DateTimeTimeZone `json:"start,omitempty"`
	End               *DateTimeTimeZone `json:"end,omitempty"`
	IsAllDay          bool              `json:"isAllDay,omitempty"`
	Attendees         []attendeeWire    `json:"attendees,omitempty"`
	Location          *Location         `json:"location,omitempty"`
	Body              *ItemBody         `json:"body,omitempty"`
	IsOnlineMeeting   bool              `json:"isOnlineMeeting,omitempty"`
	OnlineMeetingProv string            `json:"onlineMeetingProvider,omitempty"`
	TransactionID     string            `json:"transactionId,omitempty"`
}

// attendeeWire is one attendee of a create. The docs make emailAddress and type
// the two properties that matter (refs/graph/api-reference/v1.0/resources/attendee.md).
type attendeeWire struct {
	EmailAddress *EmailAddress `json:"emailAddress"`
	Type         string        `json:"type,omitempty"`
}

// AttendeeTypeRequired is the attendee type `calendar create` sends. The CLI has
// no "--optional" flag, so every attendee is required.
const AttendeeTypeRequired = "required"

// CalendarEventPath is the documented create path. The api-reference documents
// POST /me/calendar/events, not POST /me/events
// (refs/graph/api-reference/v1.0/api/calendar-post-events.md:12).
const CalendarEventPath = "/me/calendar/events"

// CreateEvent posts a new event (POST /me/calendar/events).
//
// A transactionId makes the call safe to retry: re-posting the same body with the
// same transactionId returns the same event instead of creating a second one
// (verified live 2026-10-05, handoff §3, F10).
func (c *Client) CreateEvent(ctx context.Context, in EventCreate) (Event, error) {
	var ev Event
	if strings.TrimSpace(in.Subject) == "" {
		return ev, usageError("graph: an event needs a subject")
	}
	if in.Start.IsZero() {
		return ev, usageError("graph: an event needs a start time")
	}
	end := in.End
	if end.IsZero() {
		duration := in.Duration
		if duration <= 0 {
			duration = 30 * time.Minute
		}
		end = in.Start.Add(duration)
	}
	if end.Before(in.Start) {
		return ev, usageError("graph: the event ends before it starts")
	}
	zone := strings.TrimSpace(in.Zone)
	if zone == "" {
		zone = "UTC"
	}
	loc, err := zoneLocation(zone)
	if err != nil {
		return ev, err
	}
	startPair, endPair := wallClock(in.Start, loc, zone), wallClock(end, loc, zone)
	if in.AllDay {
		startPair.DateTime = dateOnly(in.Start, loc)
		endPair.DateTime = dateOnly(end, loc)
	}
	body := eventCreateWire{
		Subject:       in.Subject,
		Start:         &startPair,
		End:           &endPair,
		IsAllDay:      in.AllDay,
		TransactionID: in.TransactionID,
	}
	if in.Location != "" {
		body.Location = &Location{DisplayName: in.Location}
	}
	if in.Body != "" {
		body.Body = &ItemBody{Content: in.Body, ContentType: "text"}
	}
	for _, attendee := range in.Attendees {
		if attendee.EmailAddress == nil || attendee.EmailAddress.Address == "" {
			continue
		}
		body.Attendees = append(body.Attendees, attendeeWire{EmailAddress: attendee.EmailAddress, Type: AttendeeTypeRequired})
	}
	if in.Teams {
		// isOnlineMeeting is silently ignored by a mailbox that cannot create
		// Teams meetings, which is why the caller checks
		// allowedOnlineMeetingProviders first (plans/calendar.md §3, F10).
		body.IsOnlineMeeting = true
		body.OnlineMeetingProv = OnlineMeetingProviderTeams
	}
	if err := c.Post(ctx, CalendarEventPath, body, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}

// dateOnly renders an all-day event's date, which the docs require to be
// midnight in the event's own zone.
func dateOnly(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02T15:04:05")
}

// EventUpdate is the PATCH body of `calendar update`. Every field is a pointer so
// "not given" and "given as empty" stay distinguishable: the handoff requires
// that only the fields the user named are sent (plans/calendar.md §4.7).
type EventUpdate struct {
	Subject  *string
	Start    *time.Time
	End      *time.Time
	Duration time.Duration
	Zone     string
	Location *string
	Body     *string
}

// eventUpdateWire is the PATCH body.
//
// body is deliberately only sent when the caller set it: rewriting an event's
// body drops the Teams meeting blob, which the docs warn about
// (plans/calendar.md §4.7).
type eventUpdateWire struct {
	Subject  *string           `json:"subject,omitempty"`
	Start    *DateTimeTimeZone `json:"start,omitempty"`
	End      *DateTimeTimeZone `json:"end,omitempty"`
	Location *Location         `json:"location,omitempty"`
	Body     *ItemBody         `json:"body,omitempty"`
}

// UpdateEvent patches an event with only the fields the caller set
// (PATCH /me/events/{event-id}).
//
// As a non-organizer the call succeeds but changes only the caller's own copy,
// and the organizer's next update overwrites it, so the CLI refuses that case
// unless --local-copy was passed (verified live 2026-10-05, handoff §3, F10).
func (c *Client) UpdateEvent(ctx context.Context, eventID string, in EventUpdate) (Event, error) {
	var ev Event
	if strings.TrimSpace(eventID) == "" {
		return ev, usageError("graph: an event id is required")
	}
	body := eventUpdateWire{Subject: in.Subject, Location: nil, Body: nil}
	if in.Location != nil {
		body.Location = &Location{DisplayName: *in.Location}
	}
	if in.Body != nil {
		body.Body = &ItemBody{Content: *in.Body, ContentType: "text"}
	}
	if in.Start != nil {
		zone := strings.TrimSpace(in.Zone)
		if zone == "" {
			zone = "UTC"
		}
		loc, err := zoneLocation(zone)
		if err != nil {
			return ev, err
		}
		start := wallClock(*in.Start, loc, zone)
		body.Start = &start
		// An end the caller did not name is derived from --duration, so a start
		// change never silently makes the event zero-length.
		end := in.End
		if end == nil {
			duration := in.Duration
			if duration <= 0 {
				duration = 30 * time.Minute
			}
			derived := in.Start.Add(duration)
			end = &derived
		}
		endPair := wallClock(*end, loc, zone)
		body.End = &endPair
	} else if in.End != nil {
		zone := strings.TrimSpace(in.Zone)
		if zone == "" {
			zone = "UTC"
		}
		loc, err := zoneLocation(zone)
		if err != nil {
			return ev, err
		}
		endPair := wallClock(*in.End, loc, zone)
		body.End = &endPair
	}
	if err := c.Patch(ctx, "/me/events/"+segment(eventID), body, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}

// EventResponse is the kind of response `calendar accept|tentative|decline`
// sends.
type EventResponse string

// The three documented responses (refs/graph/api-reference/v1.0/api/event-accept.md).
const (
	ResponseAccept    EventResponse = "accept"
	ResponseTentative EventResponse = "tentativelyAccept"
	ResponseDecline   EventResponse = "decline"
)

// TimeSlot is microsoft.graph.timeSlot, the proposedNewTime shape
// (refs/graph/api-reference/v1.0/resources/timeslot.md).
type TimeSlot struct {
	Start time.Time
	End   time.Time
}

// timeSlotWire is timeSlot on the wire.
type timeSlotWire struct {
	Start *DateTimeTimeZone `json:"start,omitempty"`
	End   *DateTimeTimeZone `json:"end,omitempty"`
}

// respondWire is the accept/decline body.
type respondWire struct {
	Comment         string        `json:"comment,omitempty"`
	SendResponse    bool          `json:"sendResponse"`
	ProposedNewTime *timeSlotWire `json:"proposedNewTime,omitempty"`
}

// RespondEvent accepts, tentatively accepts or declines an event.
//
// comment and sendResponse are always sent; propose is optional and only the
// tentative and decline responses document it. A proposed time with
// sendResponse: false is a 400 ErrorInvalidParameter, which the CLI refuses up
// front (plans/calendar.md §4.7, §4.5).
func (c *Client) RespondEvent(ctx context.Context, eventID string, kind EventResponse, comment string, sendResponse bool, propose *TimeSlot, zone string) error {
	if strings.TrimSpace(eventID) == "" {
		return usageError("graph: an event id is required")
	}
	switch kind {
	case ResponseAccept, ResponseTentative, ResponseDecline:
	default:
		return usageError(fmt.Sprintf("graph: %q is not a calendar response", kind))
	}
	body := respondWire{Comment: comment, SendResponse: sendResponse}
	if propose != nil {
		loc, err := zoneLocation(strings.TrimSpace(zone))
		if err != nil {
			return err
		}
		start := wallClock(propose.Start, loc, strings.TrimSpace(zone))
		end := wallClock(propose.End, loc, strings.TrimSpace(zone))
		body.ProposedNewTime = &timeSlotWire{Start: &start, End: &end}
	}
	return c.Post(ctx, "/me/events/"+segment(eventID)+"/"+string(kind), body, nil)
}

// CancelEvent cancels an event the caller organizes
// (POST /me/events/{event-id}/cancel). The organizer's attendees get a
// cancellation notice.
func (c *Client) CancelEvent(ctx context.Context, eventID, comment string) error {
	if strings.TrimSpace(eventID) == "" {
		return usageError("graph: an event id is required")
	}
	body := struct {
		Comment string `json:"comment,omitempty"`
	}{Comment: comment}
	return c.Post(ctx, "/me/events/"+segment(eventID)+"/cancel", body, nil)
}

// DeleteEvent deletes an event. Deleting an event you organize sends
// cancellations to its attendees (refs/graph/api-reference/v1.0/api/event-delete.md).
func (c *Client) DeleteEvent(ctx context.Context, eventID string) error {
	if strings.TrimSpace(eventID) == "" {
		return usageError("graph: an event id is required")
	}
	return c.Delete(ctx, "/me/events/"+segment(eventID))
}

// EventPreCheck is the small read `calendar` writes take before they act, so the
// CLI can refuse a request Graph would answer with a 400
// (plans/calendar.md §4.7).
type EventPreCheck struct {
	IsOrganizer           bool
	AllowNewTimeProposals bool
	IsOnlineMeeting       bool
	Subject               string
}

// EventPreCheckSelect is the $select the pre-check uses.
const EventPreCheckSelect = "isOrganizer,allowNewTimeProposals,isOnlineMeeting,onlineMeeting,subject"

// PreCheckEvent reads the three fields the write pre-checks need.
func (c *Client) PreCheckEvent(ctx context.Context, eventID string) (EventPreCheck, error) {
	var out EventPreCheck
	ev, err := c.GetEvent(ctx, eventID, EventPreCheckSelect)
	if err != nil {
		return out, err
	}
	out.IsOrganizer = ev.IsOrganizer
	out.IsOnlineMeeting = ev.IsOnlineMeeting
	out.Subject = ev.Subject
	// allowNewTimeProposals defaults to true when Graph omits it.
	out.AllowNewTimeProposals = ev.AllowNewTimeProposals == nil || *ev.AllowNewTimeProposals
	return out, nil
}
