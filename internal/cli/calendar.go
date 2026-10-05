package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
)

// Calendar reads: `teams calendar list`, `show` and `search` (PLAN.md Phase 6a,
// plans/calendar.md §4.3-§4.6).
//
// Three decisions shape the code:
//
//   - days are LOCAL. The window is computed from --tz (or this machine's zone)
//     and sent as an RFC3339 range with an offset; no Prefer: outlook.timezone
//     header is ever sent, because an unknown zone is a hard 400 rather than a
//     fallback (plans/calendar.md §3, F6, decision D4).
//   - all-day events are floating. They are matched by their own dates and never
//     time-converted (plans/calendar.md §3, F5).
//   - the window is widened by one day on each side server-side, then filtered
//     exactly here, because the server matches all-day events as UTC midnight to
//     midnight.
//
// Another user's calendar is tried first and falls back to getSchedule free/busy
// when it is refused (decision D1).

// defaultCalendarLimit is how many rows a listing shows before it says so and
// suggests --all (plans/calendar.md §4.3).
const defaultCalendarLimit = 200

// calendarHandleLength is the length of a short event handle: 7 hex characters,
// which is what the listing prints (plans/calendar.md §4.6).
const calendarHandleLength = 7

// calendarHandleCollisionLength is the length used for both handles when two
// event ids in one listing share their 7-character prefix.
const calendarHandleCollisionLength = 10

// calendarChatConcurrency caps the concurrent `--chat` lookups, because a
// listing of 200 meetings would otherwise open 200 sockets at once
// (plans/calendar.md §4.3).
const calendarChatConcurrency = 4

// calendarRow is one rendered listing row: an event from a calendar view or a
// free/busy item from getSchedule.
type calendarRow struct {
	// User is "me" for the signed-in user, or the mail address asked for.
	User string
	// Source is "events" or "freebusy".
	Source string
	// ID is the full Graph event id; empty for a free/busy row.
	ID string
	// Handle is the short handle printed in the ID column; empty for free/busy.
	Handle string
	// Subject is the event subject; empty for another user's free/busy, which
	// returns only start, end and status (plans/calendar.md §3, F7).
	Subject string
	// Start and End are instants in UTC for a timed event, and midnight of the
	// event's own dates in the display location for an all-day event.
	Start, End time.Time
	// AllDay marks a floating all-day event.
	AllDay bool
	// Status is showAs for an event and status for a free/busy item.
	Status string
	// Response is the signed-in user's responseStatus.response.
	Response string
	// IsOrganizer marks an event organized by its mailbox owner.
	IsOrganizer bool
	// Organizer is the organizer's display name.
	Organizer string
	// OrganizerAddress is the organizer's SMTP address.
	OrganizerAddress string
	// Location is the location display name.
	Location string
	// IsOnline marks a Teams online meeting.
	IsOnline bool
	// JoinURL is the online meeting's join URL.
	JoinURL string
	// ChatID is the Teams meeting chat thread id, filled by --chat or `show`.
	ChatID string
	// WebLink opens the event in Outlook on the web.
	WebLink string
	// IsCancelled marks a cancelled event, hidden unless --include-cancelled.
	IsCancelled bool
	// EventType is singleInstance, occurrence, exception or seriesMaster.
	EventType string
	// Attendees are the event's attendees, from `show`.
	Attendees []calendarAttendee
}

// calendarAttendee is one attendee in the --json schema.
type calendarAttendee struct {
	Name     string `json:"name,omitempty"`
	Address  string `json:"address,omitempty"`
	Type     string `json:"type,omitempty"`
	Response string `json:"response,omitempty"`
}

// calendarJSON is the documented --json object (plans/calendar.md §4.4). It is
// CLI-owned and stable: it is not the Graph event type, so a Graph field
// arriving later cannot change the schema by accident.
type calendarJSON struct {
	User        string             `json:"user"`
	Source      string             `json:"source"`
	ID          string             `json:"id,omitempty"`
	Handle      string             `json:"handle,omitempty"`
	Subject     string             `json:"subject,omitempty"`
	Start       string             `json:"start"`
	End         string             `json:"end"`
	AllDay      bool               `json:"allDay"`
	Status      string             `json:"status"`
	Response    string             `json:"response,omitempty"`
	IsOrganizer bool               `json:"isOrganizer"`
	Organizer   *calendarOrganizer `json:"organizer,omitempty"`
	Location    string             `json:"location,omitempty"`
	IsOnline    bool               `json:"isOnline"`
	JoinURL     string             `json:"joinUrl,omitempty"`
	ChatID      string             `json:"chatId,omitempty"`
	WebLink     string             `json:"webLink,omitempty"`
	IsCancelled bool               `json:"isCancelled"`
	Type        string             `json:"type,omitempty"`
	Attendees   []calendarAttendee `json:"attendees,omitempty"`
}

// calendarOrganizer is the organizer object of the --json schema.
type calendarOrganizer struct {
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
}

// newCalendarCmd is the `teams calendar` command group. With no subcommand it
// prints help, like the other noun commands.
func (a *App) newCalendarCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "calendar",
		Short: "List, show and search your calendar, and act on meetings",
		Long: "Read your calendar, or a colleague's, and act on meetings.\n\n" +
			"Days and times are LOCAL: a day runs midnight to midnight in --tz, or in\n" +
			"this machine's zone when --tz is not given. All-day events are floating, so\n" +
			"they are matched by date and never shifted by a timezone conversion.\n\n" +
			"The write commands notify people: an invitation reaches every --attendee as\n" +
			"soon as the event is created, accepting or declining tells the organizer,\n" +
			"and cancelling or deleting a meeting you organize sends your attendees a\n" +
			"cancellation. They ask for confirmation first, and take --dry-run.\n\n" +
			"Calendar scopes are requested at first use, so your profile may ask for them\n" +
			"the first time one of these commands runs.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(
		a.newCalendarListCmd(), a.newCalendarShowCmd(), a.newCalendarSearchCmd(),
		// Phase 6b writes.
		a.newCalendarCreateCmd(), a.newCalendarUpdateCmd(),
		a.newCalendarAcceptCmd(), a.newCalendarTentativeCmd(), a.newCalendarDeclineCmd(),
		a.newCalendarCancelCmd(), a.newCalendarDeleteCmd(),
	)
	return cmd
}

// calendarListFlags are the `list` flags.
type calendarListFlags struct {
	whenFlags
	users    []string
	freeBusy bool
	chat     bool
	limit    int
	all      bool
}

func (a *App) newCalendarListCmd() *cobra.Command {
	var flags calendarListFlags
	cmd := &cobra.Command{
		Use:   "list [--user <who>]… [--date <day>] [--days N] [--from <day> --to <day>]",
		Short: "List meetings for a day or a range",
		Long: "List your meetings, or a colleague's, for one day or a date range.\n\n" +
			"Days are local (--tz, or this machine's zone). Another user's calendar is\n" +
			"shown in full when it is shared with you; otherwise the command falls back to\n" +
			"free/busy, which shows only times and status (--free-busy asks for that\n" +
			"directly). The ID column is a short handle for the full event id, usable with\n" +
			"`teams calendar show`.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runCalendarList(cmd.Context(), flags)
		},
	}
	flags.addTo(cmd)
	cmd.Flags().StringSliceVar(&flags.users, "user", nil, "a colleague whose calendar to include (repeatable, comma-separated; default: you)")
	cmd.Flags().BoolVar(&flags.freeBusy, "free-busy", false, "show free/busy only, even for your own shared calendars")
	cmd.Flags().BoolVar(&flags.chat, "chat", false, "resolve each Teams meeting's chat and add a Chat column")
	cmd.Flags().IntVar(&flags.limit, "limit", defaultCalendarLimit, "maximum number of rows")
	cmd.Flags().BoolVar(&flags.all, "all", false, "show every row instead of --limit")
	return cmd
}

// runCalendarList implements plans/calendar.md §4.3.
func (a *App) runCalendarList(ctx context.Context, flags calendarListFlags) error {
	// The scope checks come first, before any Graph call — including the /me read
	// that resolving a person needs — because exit 3 means "fix your consent
	// before this command does anything" (plans/calendar.md §4.8, §4.5). Whether
	// the request needs the shared scope is decided from the flags alone: a
	// --user that resolves to the signed-in user still costs one extra scope, and
	// that is cheaper than a round trip on every plain `calendar list`.
	others := len(flags.users) > 0
	shared := flags.freeBusy || others
	if err := a.requireCalendarReadScope(ctx, "teams calendar list", others, flags.freeBusy); err != nil {
		return err
	}
	if flags.chat {
		if err := a.requireIncrementalScopes(ctx, "teams calendar list --chat",
			[]string{"OnlineMeetings.Read", "OnlineMeetings.ReadWrite"}, []string{"OnlineMeetings.Read"}); err != nil {
			return err
		}
	}
	window, err := flags.window(a.Clock.Now(), shared)
	if err != nil {
		return err
	}
	targets, err := a.calendarTargets(ctx, flags.users)
	if err != nil {
		return err
	}
	a.Printer.Statusf("fetching calendar...")
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	// The resolver carries the entity cache the handles are written to. Building
	// it here (rather than lazily, the way a name lookup does) is what makes
	// `calendar list` persist the handles it prints, so `calendar show <handle>`
	// finds them in the next command (plans/calendar.md §4.6).
	if _, rerr := a.resolver(ctx); rerr != nil {
		return rerr
	}
	var (
		rows    []calendarRow
		forBusy []calendarTarget
		failed  int
	)
	for _, target := range targets {
		if target.self && !flags.freeBusy {
			events, ferr := a.calendarViewRows(ctx, client, target, window)
			if ferr != nil {
				return ferr
			}
			rows = append(rows, events...)
			continue
		}
		if flags.freeBusy {
			forBusy = append(forBusy, target)
			continue
		}
		events, ferr := a.calendarViewRows(ctx, client, target, window)
		if ferr == nil {
			rows = append(rows, events...)
			continue
		}
		fallback, notFound := classifyCalendarViewError(ferr)
		switch {
		case fallback:
			a.Printer.Warnf("%s: calendar not shared with you; showing free/busy", target.label)
			forBusy = append(forBusy, target)
		case notFound != nil:
			a.Printer.Errorf("%s: %v", target.label, notFound)
			failed++
		default:
			return ferr
		}
	}
	if len(forBusy) > 0 {
		busyRows, err := a.freeBusyRows(ctx, client, forBusy, window)
		if err != nil {
			return err
		}
		rows = append(rows, busyRows...)
	}

	rows = filterCalendarRows(rows, window, flags.cancelled)
	sortCalendarRows(rows, window)

	// One user failing is a warning; every user failing is exit 4
	// (plans/calendar.md §4.3).
	if failed > 0 && failed == len(targets) {
		return output.NotFoundf("no calendar could be read for %d user(s)", failed)
	}

	if flags.chat {
		a.fillMeetingChats(ctx, client, rows)
	}
	handles := a.assignHandles(rows)

	limit := flags.limit
	if flags.all || limit <= 0 {
		limit = 0
	}
	truncated := limit > 0 && len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}

	if handles > 0 {
		// The cache is persisted after a successful command, so a handle printed
		// now still resolves later (plans/calendar.md §4.6).
		a.saveEntityCache()
	}

	if truncated {
		a.Printer.Warnf("showing first %d; use --all", limit)
	}
	if a.Printer.JSONMode() {
		return a.Printer.JSON(calendarJSONRows(rows, window.loc))
	}
	a.printCalendarTable(rows, window, flags.chat, len(targets) > 1)
	return nil
}

// calendarTarget is one calendar the command reads: the signed-in user or a
// colleague, with the address getSchedule needs and the id the path needs.
type calendarTarget struct {
	// label is what errors and warnings name.
	label string
	// user is the --json "user" value: "me" for the signed-in user.
	user string
	// id is the user id used in the calendarView path.
	id string
	// mail is the SMTP address getSchedule takes.
	mail string
	// self marks the signed-in user, whose own calendarView is /me/calendarView.
	self bool
}

// calendarTargets resolves the --user flags, defaulting to the signed-in user.
//
// `me`, your own user principal name and your own mail address all mean the
// signed-in user (plans/calendar.md §4.3).
func (a *App) calendarTargets(ctx context.Context, users []string) ([]calendarTarget, error) {
	me, err := a.Me(ctx)
	if err != nil {
		return nil, err
	}
	targets := []calendarTarget{{
		label: "me", user: "me", id: me.ID, mail: firstNonEmptyString(me.Mail, me.UserPrincipalName), self: true,
	}}
	seen := map[string]bool{}
	for _, raw := range users {
		value := strings.TrimSpace(raw)
		if value == "" {
			return nil, output.Usagef("--user needs a person")
		}
		if isSelfReference(value, me) {
			continue
		}
		// An address or a UPN is taken as written. The calendarView and
		// getSchedule endpoints both accept `{id | userPrincipalName}`, so
		// resolving it through the directory would need User.ReadBasic.All or
		// People.Read for nothing — a real cost, because those scopes are in a
		// different consent set than the calendar ones (plans/calendar.md §4.3).
		if looksLikeAddress(value) {
			key := strings.ToLower(value)
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, calendarTarget{label: value, user: value, id: value, mail: value})
			continue
		}
		resolver, err := a.resolver(ctx)
		if err != nil {
			return nil, err
		}
		person, err := resolver.Person(ctx, value)
		if err != nil {
			return nil, err
		}
		if person.UserID == me.ID {
			continue
		}
		key := strings.ToLower(person.UserID)
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, calendarTarget{
			label: firstNonEmptyString(person.UserMail, person.UserName, value),
			user:  firstNonEmptyString(person.UserMail, person.UserName, value),
			id:    person.UserID,
			mail:  firstNonEmptyString(person.UserMail, person.UserName),
		})
	}
	return targets, nil
}

// looksLikeAddress reports whether a --user value is already an SMTP address or
// a user principal name, which the calendar endpoints resolve themselves.
func looksLikeAddress(value string) bool {
	at := strings.IndexByte(value, '@')
	if at <= 0 || at == len(value)-1 {
		return false
	}
	// A UPN with a path or a space is not an address.
	return !strings.ContainsAny(value, " /\\")
}

// isSelfReference reports whether a --user value means the signed-in user.
func isSelfReference(value string, me graph.Me) bool {
	switch {
	case strings.EqualFold(value, "me"):
		return true
	case me.ID != "" && strings.EqualFold(value, me.ID):
		return true
	case me.Mail != "" && strings.EqualFold(value, me.Mail):
		return true
	case me.UserPrincipalName != "" && strings.EqualFold(value, me.UserPrincipalName):
		return true
	default:
		return false
	}
}

// requireCalendarReadScope checks the read scope the request needs. A listing
// that touches another user's calendar needs the shared scope; everything else
// needs the plain read scope. The re-consent request asks for both, so the first
// calendar command that runs leaves a profile that can do all of the reads
// (plans/calendar.md §4.8).
func (a *App) requireCalendarReadScope(ctx context.Context, command string, others, freeBusy bool) error {
	anyOf := []string{"Calendars.Read", "Calendars.ReadWrite"}
	if others && !freeBusy {
		anyOf = []string{"Calendars.Read.Shared", "Calendars.ReadWrite.Shared"}
	}
	return a.requireIncrementalScopes(ctx, command, anyOf, []string{"Calendars.Read", "Calendars.Read.Shared"})
}

// calendarViewRows reads one user's calendarView and converts it to rows.
func (a *App) calendarViewRows(ctx context.Context, client *graph.Client, target calendarTarget, window rangeWindow) ([]calendarRow, error) {
	userID := ""
	if !target.self {
		userID = target.id
	}
	rows := make([]calendarRow, 0, 16)
	err := client.ListCalendarView(ctx, userID, window.start, window.end, func(ev graph.Event) bool {
		row, ok := calendarRowFromEvent(ev, target, window)
		if ok {
			rows = append(rows, row)
		}
		return true
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// calendarRowFromEvent converts one event to a row, in the display location.
//
// An all-day event keeps its own dates: its dateTime is always midnight of those
// dates whatever zone was asked for, so converting it would move the event into
// the previous or next day for half the world (plans/calendar.md §3, F5).
func calendarRowFromEvent(ev graph.Event, target calendarTarget, window rangeWindow) (calendarRow, bool) {
	row := calendarRow{
		User:        target.user,
		Source:      "events",
		ID:          ev.ID,
		Subject:     ev.Subject,
		Status:      ev.ShowAs,
		IsOrganizer: ev.IsOrganizer,
		IsCancelled: ev.IsCancelled,
		EventType:   ev.Type,
		WebLink:     ev.WebLink,
		IsOnline:    ev.IsTeamsMeeting(),
		JoinURL:     ev.JoinURL(),
	}
	if ev.Organizer != nil {
		row.Organizer = ev.Organizer.Name()
		row.OrganizerAddress = ev.Organizer.Address()
	}
	if ev.Location != nil {
		row.Location = ev.Location.DisplayName
	}
	if ev.ResponseStatus != nil {
		row.Response = ev.ResponseStatus.Response
	}
	for _, attendee := range ev.Attendees {
		entry := calendarAttendee{
			Name: attendee.DisplayName(),
			Type: attendee.Type,
		}
		if attendee.EmailAddress != nil {
			entry.Address = attendee.EmailAddress.Address
		}
		if attendee.Status != nil {
			entry.Response = attendee.Status.Response
		}
		row.Attendees = append(row.Attendees, entry)
	}
	if ev.IsAllDay {
		// An all-day event is floating: its dateTime is always midnight of its own
		// dates whatever zone was requested, so the dates are taken as written and
		// only re-anchored to the display location (plans/calendar.md §3, F5).
		//
		// row.End is the EXCLUSIVE end: the midnight after the last day, which is
		// what Graph reports for an all-day event and what the filter compares. A
		// single-day event then spans [day, day+1), not [day, day) — a zero-length
		// range would be invisible to every overlap test.
		from, err := time.Parse("2006-01-02", ev.Start.Date())
		if err != nil {
			return row, false
		}
		to, err := time.Parse("2006-01-02", ev.End.Date())
		if err != nil || !to.After(from) {
			to = from.AddDate(0, 0, 1)
		}
		row.AllDay = true
		row.Start = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, window.loc)
		row.End = time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, window.loc)
		return row, true
	}
	start, err := ev.Start.UTC()
	if err != nil {
		return row, false
	}
	end, err := ev.End.UTC()
	if err != nil {
		return row, false
	}
	row.Start, row.End = start, end
	return row, true
}

// freeBusyRows asks getSchedule for the queued users and converts the schedules.
func (a *App) freeBusyRows(ctx context.Context, client *graph.Client, targets []calendarTarget, window rangeWindow) ([]calendarRow, error) {
	mails := make([]string, 0, len(targets))
	byMail := map[string]calendarTarget{}
	for _, target := range targets {
		if target.mail == "" {
			a.Printer.Warnf("%s: no mail address, so free/busy cannot be requested", target.label)
			continue
		}
		mails = append(mails, target.mail)
		byMail[strings.ToLower(target.mail)] = target
	}
	if len(mails) == 0 {
		return nil, nil
	}
	schedules, err := client.GetSchedule(ctx, mails, window.start, window.end, window.zone, graph.DefaultScheduleInterval)
	if err != nil {
		return nil, err
	}
	rows := make([]calendarRow, 0, len(schedules))
	for _, schedule := range schedules {
		target, ok := byMail[strings.ToLower(schedule.ScheduleID)]
		if !ok {
			// Graph answers with the scheduleId we asked for; when it does not,
			// the row keeps the address so it is not silently dropped.
			target = calendarTarget{label: schedule.ScheduleID, user: schedule.ScheduleID}
		}
		if schedule.Error != nil {
			// A mailbox that could not be resolved at all: 5016 covers both "no
			// mailbox" and "no such user", and the CLI cannot tell them apart
			// (plans/calendar.md §3, F7).
			a.Printer.Warnf("%s: no free/busy available (%s)", target.label, schedule.Error.ResponseCode)
			continue
		}
		for _, item := range schedule.Items() {
			if item.IsFree() {
				// The live service returns free slots for your own calendar;
				// a listing of them is noise.
				continue
			}
			start, serr := item.Start.UTC()
			if serr != nil {
				continue
			}
			end, eerr := item.End.UTC()
			if eerr != nil {
				continue
			}
			// An all-day item comes back as midnight of the *requested* zone
			// converted to UTC, so it is recognised by both ends being midnight
			// and rendered by date rather than as a 00:00–00:00 range
			// (plans/calendar.md §3, F7).
			allDay := isMidnightUTC(start) && !end.After(start)
			if allDay {
				end = start.AddDate(0, 0, 1)
			}
			rows = append(rows, calendarRow{
				User:     target.user,
				Source:   "freebusy",
				Subject:  item.Subject,
				Start:    start,
				End:      end,
				AllDay:   allDay,
				Status:   item.Status,
				Location: item.Location,
			})
		}
	}
	return rows, nil
}

// filterCalendarRows keeps the rows inside the requested local days and drops
// cancelled events unless they were asked for.
//
// A timed event is kept when it overlaps the window as instants. An all-day
// event is kept when its own date range intersects the requested local days
// (plans/calendar.md §3, F5).
func filterCalendarRows(rows []calendarRow, window rangeWindow, includeCancelled bool) []calendarRow {
	// The filter uses the REQUESTED days, never the widened server window: the
	// widening exists only so the floating all-day match has room, and reusing it
	// here would show the neighbouring days (plans/calendar.md §3, F5).
	firstDay := window.from.Format("2006-01-02")
	lastDay := window.to.Format("2006-01-02")
	from := midnight(window.from, window.loc)
	to := midnight(window.to.AddDate(0, 0, 1), window.loc)

	out := make([]calendarRow, 0, len(rows))
	for _, row := range rows {
		if row.IsCancelled && !includeCancelled {
			continue
		}
		if row.AllDay {
			// An all-day event is compared by its own dates: [start day, end day)
			// against the requested [from day, to day + 1). Comparing the instants
			// instead would move it a day for half the world, because the same
			// midnight is a different calendar date in another zone.
			startDay := dayKey(row.Start, window.loc)
			endDay := dayKey(row.End, window.loc)
			if startDay <= lastDay && endDay > firstDay {
				out = append(out, row)
			}
			continue
		}
		if row.Start.Before(to) && row.End.After(from) {
			out = append(out, row)
		}
	}
	return out
}

// isMidnightUTC reports whether an instant is exactly midnight UTC, which is how
// an all-day free/busy item arrives (plans/calendar.md §3, F7).
func isMidnightUTC(t time.Time) bool {
	utc := t.UTC()
	return utc.Hour() == 0 && utc.Minute() == 0 && utc.Second() == 0 && utc.Nanosecond() == 0
}

// sortCalendarRows orders a listing the way a day reads: earliest first, all-day
// events before the timed ones within a day, then by user and subject
// (plans/calendar.md §4.3).
func sortCalendarRows(rows []calendarRow, window rangeWindow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		dayA := dayKey(a.Start, window.loc)
		dayB := dayKey(b.Start, window.loc)
		if dayA != dayB {
			return dayA < dayB
		}
		if a.AllDay != b.AllDay {
			return a.AllDay
		}
		if !a.Start.Equal(b.Start) {
			return a.Start.Before(b.Start)
		}
		if !strings.EqualFold(a.User, b.User) {
			return strings.ToLower(a.User) < strings.ToLower(b.User)
		}
		return strings.ToLower(a.Subject) < strings.ToLower(b.Subject)
	})
}

// dayKey is the display day of a row, for sorting.
func dayKey(t time.Time, loc *time.Location) string { return t.In(loc).Format("2006-01-02") }

// assignHandles gives every event row a short handle, stores it in the entity
// cache and returns how many handles were written.
//
// A handle is the first seven hex characters of the SHA-256 of the event id. When
// two ids in one listing share that prefix, both handles are lengthened to ten
// characters (plans/calendar.md §4.6).
func (a *App) assignHandles(rows []calendarRow) int {
	byID := map[string]bool{}
	for _, row := range rows {
		if row.ID != "" {
			byID[row.ID] = true
		}
	}
	if len(byID) == 0 {
		return 0
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	lengths := handleLengths(ids, a.cachedHandles())
	written := 0
	for i := range rows {
		if rows[i].ID == "" {
			continue
		}
		handle := shortHandle(rows[i].ID, lengths[rows[i].ID])
		rows[i].Handle = handle
		if a.entityCache != nil {
			a.entityCache.PutEvent(handle, rows[i].ID)
			written++
		}
	}
	return written
}

// cachedHandles returns the handles already in the entity cache, so a new handle
// cannot collide with one printed by an earlier run.
func (a *App) cachedHandles() []string {
	if a.entityCache == nil {
		return nil
	}
	return a.entityCache.EventHandles()
}

// handleLengths decides the handle length for each id: 7 characters, or 10 for
// every id that shares its 7-character prefix with another one.
func handleLengths(ids, existing []string) map[string]int {
	// The prefix function is a parameter so the collision rule is testable without
	// brute-forcing a real SHA-256 collision.
	return handleLengthsWithPrefix(ids, existing, func(id string) string {
		return shortHandle(id, calendarHandleLength)
	})
}

// handleLengthsWithPrefix implements handleLengths. prefix must return the
// 7-character prefix of an id.
func handleLengthsWithPrefix(ids, existing []string, prefix func(string) string) map[string]int {
	out := make(map[string]int, len(ids))
	groups := map[string][]string{}
	for _, id := range ids {
		p := prefix(id)
		groups[p] = append(groups[p], id)
	}
	// A handle already in the cache that shares a prefix with an id is a collision
	// too: the user could not tell the two apart from what the cache holds.
	for _, handle := range existing {
		if len(handle) > calendarHandleLength {
			handle = handle[:calendarHandleLength]
		}
		for _, id := range ids {
			if prefix(id) == handle && !containsID(groups[handle], id) {
				groups[handle] = append(groups[handle], id)
			}
		}
	}
	for _, group := range groups {
		length := calendarHandleLength
		if len(group) > 1 {
			length = calendarHandleCollisionLength
		}
		for _, id := range group {
			out[id] = length
		}
	}
	return out
}

// containsID reports whether a group already holds an id.
func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// shortHandle is the first n hex characters of the SHA-256 of an event id.
func shortHandle(id string, n int) string {
	sum := sha256.Sum256([]byte(id))
	full := hex.EncodeToString(sum[:])
	if n > len(full) {
		n = len(full)
	}
	return full[:n]
}

// fillMeetingChats resolves the Teams meeting chat for every row that has a join
// URL, with at most calendarChatConcurrency lookups in flight.
func (a *App) fillMeetingChats(ctx context.Context, client *graph.Client, rows []calendarRow) {
	indexes := make([]int, 0, len(rows))
	for i, row := range rows {
		if row.JoinURL != "" {
			indexes = append(indexes, i)
		}
	}
	if len(indexes) == 0 {
		return
	}
	sem := make(chan struct{}, calendarChatConcurrency)
	type result struct {
		index int
		chat  string
	}
	results := make(chan result, len(indexes))
	for _, index := range indexes {
		go func(index int) {
			sem <- struct{}{}
			defer func() { <-sem }()
			chat, err := client.FindMeetingChatID(ctx, rows[index].JoinURL)
			if err != nil {
				a.Printer.Debugf("meeting chat for %s: %v", rows[index].ID, err)
			}
			results <- result{index: index, chat: chat}
		}(index)
	}
	for range indexes {
		res := <-results
		rows[res.index].ChatID = res.chat
	}
}

// printCalendarTable renders the compact listing (plans/calendar.md §4.4).
func (a *App) printCalendarTable(rows []calendarRow, window rangeWindow, chat, multiUser bool) {
	headers := make([]string, 0, 8)
	if multiUser {
		headers = append(headers, "USER")
	}
	if !window.coversOneDay() {
		headers = append(headers, "DATE")
	}
	headers = append(headers, "ID", "WHEN", "SUBJECT", "ORGANIZER", "TEAMS")
	if chat {
		headers = append(headers, "CHAT")
	}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		values := make([]string, 0, 8)
		if multiUser {
			values = append(values, row.User)
		}
		if !window.coversOneDay() {
			values = append(values, dayLabel(row.Start, window.loc))
		}
		values = append(values,
			row.Handle,
			calendarWhenLabel(row, window.loc),
			truncateSubject(row),
			row.Organizer,
			teamsMark(row.IsOnline),
		)
		if chat {
			values = append(values, row.ChatID)
		}
		table = append(table, values)
	}
	a.Printer.Table(headers, table)
}

// calendarWhenLabel renders the When column: a time range, or "all day".
func calendarWhenLabel(row calendarRow, loc *time.Location) string {
	if row.AllDay {
		return "all day"
	}
	return timeLabel(row.Start, loc) + "–" + timeLabel(row.End, loc)
}

// truncateSubject renders the Subject column, with the free/busy placeholder for
// a row another user's calendar shared only as availability, and a 60-character
// cap with an ellipsis (plans/calendar.md §4.4).
func truncateSubject(row calendarRow) string {
	subject := strings.TrimSpace(row.Subject)
	if subject == "" {
		subject = freeBusyPlaceholder(row.Status)
	}
	const limit = 60
	if len([]rune(subject)) <= limit {
		return subject
	}
	runes := []rune(subject)
	return string(runes[:limit-1]) + "…"
}

// freeBusyPlaceholder is what a free/busy row without a subject shows.
func freeBusyPlaceholder(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "tentative":
		return "[tentative]"
	case "oof":
		return "[oof]"
	case "workingelsewhere":
		return "[elsewhere]"
	case "busy", "":
		return "[busy]"
	default:
		return "[" + status + "]"
	}
}

// teamsMark is the Teams column: a tick only for an online meeting whose provider
// is teamsForBusiness (plans/calendar.md §4.4).
func teamsMark(isOnline bool) string {
	if isOnline {
		return "✓"
	}
	return ""
}

// calendarJSONRows renders the rows as the documented --json array.
func calendarJSONRows(rows []calendarRow, loc *time.Location) []calendarJSON {
	out := make([]calendarJSON, 0, len(rows))
	for _, row := range rows {
		entry := calendarJSON{
			User:        row.User,
			Source:      row.Source,
			ID:          row.ID,
			Handle:      row.Handle,
			Subject:     row.Subject,
			Start:       row.Start.In(loc).Format(time.RFC3339),
			End:         row.End.In(loc).Format(time.RFC3339),
			AllDay:      row.AllDay,
			Status:      calendarStatus(row),
			Response:    row.Response,
			IsOrganizer: row.IsOrganizer,
			Location:    row.Location,
			IsOnline:    row.IsOnline,
			JoinURL:     row.JoinURL,
			ChatID:      row.ChatID,
			WebLink:     row.WebLink,
			IsCancelled: row.IsCancelled,
			Type:        row.EventType,
			Attendees:   row.Attendees,
		}
		if row.Source == "freebusy" {
			// A free/busy row has no event behind it, so it carries no handle,
			// organizer or response (plans/calendar.md §4.4).
			entry.Handle = ""
			entry.Response = ""
			entry.IsOrganizer = false
		}
		if row.Organizer != "" || row.OrganizerAddress != "" {
			entry.Organizer = &calendarOrganizer{Name: row.Organizer, Address: row.OrganizerAddress}
		}
		out = append(out, entry)
	}
	return out
}

// calendarStatus reads the documented status value: showAs for an event and
// status for a free/busy item, with "unknown" when Graph sent neither.
func calendarStatus(row calendarRow) string {
	status := strings.TrimSpace(row.Status)
	if status == "" {
		return "unknown"
	}
	return status
}

// classifyCalendarViewError splits the failures of another user's calendarView
// into the two cases the CLI reacts to (plans/calendar.md §3, F4 and §4.5):
//
//   - fallback: the calendar exists but is not shared with the caller, which is
//     403 ErrorAccessDenied or 404 ErrorItemNotFound. The caller switches to
//     getSchedule free/busy.
//   - notFound: the request can never succeed for this user (no Exchange Online
//     mailbox, or no such user). The caller reports it and carries on with the
//     other users, and the command exits 4 when every user failed.
//
// Anything else is neither, and the caller returns the error unchanged.
func classifyCalendarViewError(err error) (fallback bool, notFound error) {
	var apiErr *graph.APIError
	if !errors.As(err, &apiErr) {
		return false, nil
	}
	switch {
	case apiErr.Status == 403 && apiErr.Code == "ErrorAccessDenied":
		return true, nil
	case apiErr.Status == 404 && apiErr.Code == "ErrorItemNotFound":
		return true, nil
	case apiErr.Status == 404 && apiErr.Code == "MailboxNotEnabledForRESTAPI":
		return false, output.WithHint(output.NotFoundf("%v", apiErr),
			"this user has no Exchange Online mailbox (or it is inactive or on-premises), so their calendar cannot be read")
	case apiErr.Status == 404 && apiErr.Code == "ErrorInvalidUser":
		return false, output.NotFoundf("%v", apiErr)
	default:
		return false, nil
	}
}

// newCalendarShowCmd shows one event in full.
func (a *App) newCalendarShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <event>",
		Short: "Show one event in full",
		Long: "Show one event: when, where, who, your response and the Teams join URL.\n\n" +
			"<event> is a handle from `teams calendar list`/`search`, a full event id, or\n" +
			"an Outlook web link. Days and times are shown in your local zone.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCalendarShow(cmd.Context(), args[0])
		},
	}
	return cmd
}

func (a *App) runCalendarShow(ctx context.Context, reference string) error {
	if err := a.requireIncrementalScopes(ctx, "teams calendar show",
		[]string{"Calendars.Read", "Calendars.ReadWrite"},
		[]string{"Calendars.Read", "Calendars.Read.Shared"}); err != nil {
		return err
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	loc, _, err := displayLocation("")
	if err != nil {
		return err
	}
	// The resolver owns the entity cache the handles live in; building it first is
	// what lets `show <handle>` find what `list`/`search` stored, and what lets
	// this command store the handle it prints.
	if _, rerr := a.resolver(ctx); rerr != nil {
		return rerr
	}
	id, err := a.resolveEventReference(ctx, reference)
	if err != nil {
		return err
	}
	a.Printer.Statusf("fetching event...")
	ev, err := client.GetEvent(ctx, id, eventShowSelect)
	if err != nil {
		return err
	}
	target := calendarTarget{label: "me", user: "me", self: true}
	window := rangeWindow{loc: loc, zone: zoneName("")}
	row, ok := calendarRowFromEvent(ev, target, window)
	if !ok {
		return output.Errorf("the event %s has no usable start or end time", reference)
	}
	row.Handle = shortHandle(row.ID, calendarHandleLength)
	if a.entityCache != nil {
		a.entityCache.PutEvent(row.Handle, row.ID)
		a.saveEntityCache()
	}

	// The chat id needs OnlineMeetings.Read; when it is missing the command still
	// shows everything else and says so (plans/calendar.md §4.4). A static
	// TEAMS_ACCESS_TOKEN is never scope-checked, so a lookup that comes back empty
	// is reported the same way rather than silently.
	chatNote := ""
	if row.JoinURL != "" {
		if scopeErr := a.requireScope(ctx, "teams calendar show",
			[]string{"OnlineMeetings.Read", "OnlineMeetings.ReadWrite"}); scopeErr != nil {
			chatNote = chatNeedsScopeNote
		} else if chat, cerr := client.FindMeetingChatID(ctx, row.JoinURL); cerr == nil && chat != "" {
			row.ChatID = chat
		} else {
			chatNote = chatNeedsScopeNote
		}
	}

	if a.Printer.JSONMode() {
		entries := calendarJSONRows([]calendarRow{row}, loc)
		return a.Printer.JSON(entries[0])
	}
	a.printEventDetails(ev, row, loc, chatNote)
	return nil
}

// chatNeedsScopeNote is what `show` prints in place of a chat it could not
// resolve, and what the help text promises.
const chatNeedsScopeNote = "(needs OnlineMeetings.Read)"

// eventShowSelect is the $select `show` asks for: the listing fields plus the
// body and the attendees.
const eventShowSelect = calendarViewSelect + ",attendees,body,allowNewTimeProposals"

// calendarViewSelect mirrors the graph package's listing select, which the
// package keeps unexported because only its own wrappers use it. The two are
// asserted equal by a test, so they cannot drift.
const calendarViewSelect = "id,subject,start,end,isAllDay,isCancelled,showAs,organizer,isOrganizer," +
	"isOnlineMeeting,onlineMeeting,onlineMeetingProvider,location,responseStatus,webLink,type,seriesMasterId"

// printEventDetails renders `show` as a definition list (plans/calendar.md §4.4).
func (a *App) printEventDetails(ev graph.Event, row calendarRow, loc *time.Location, chatNote string) {
	pairs := [][2]string{
		{"subject", firstNonEmptyString(ev.Subject, "(no subject)")},
		{"when", calendarWhenFullLabel(row, loc)},
	}
	if row.Organizer != "" || row.OrganizerAddress != "" {
		pairs = append(pairs, [2]string{"organizer", strings.TrimSpace(row.Organizer + " <" + row.OrganizerAddress + ">")})
	}
	if row.Response != "" {
		pairs = append(pairs, [2]string{"your response", row.Response})
	}
	if row.Location != "" {
		pairs = append(pairs, [2]string{"location", row.Location})
	}
	pairs = append(pairs, [2]string{"teams meeting", yesNo(row.IsOnline)})
	if row.JoinURL != "" {
		pairs = append(pairs, [2]string{"join url", row.JoinURL})
	}
	switch {
	case row.ChatID != "":
		pairs = append(pairs, [2]string{"chat", row.ChatID})
	case chatNote != "":
		pairs = append(pairs, [2]string{"chat", chatNote})
	}
	if len(row.Attendees) > 0 {
		pairs = append(pairs, [2]string{"attendees", attendeeSummary(row.Attendees)})
	}
	if row.WebLink != "" {
		pairs = append(pairs, [2]string{"web link", row.WebLink})
	}
	pairs = append(pairs, [2]string{"id", row.ID}, [2]string{"handle", row.Handle})
	a.Printer.Definitions(pairs)
}

// calendarWhenFullLabel renders the when line for `show`, including the date and
// the all-day case.
func calendarWhenFullLabel(row calendarRow, loc *time.Location) string {
	if row.AllDay {
		from := dayLabel(row.Start, loc)
		// End is exclusive, so the last day shown is the day before it.
		last := row.End.AddDate(0, 0, -1)
		if dayKey(last, loc) != dayKey(row.Start, loc) {
			return "all day " + from + ".." + dayLabel(last, loc)
		}
		return "all day " + from
	}
	sameDay := dayKey(row.Start, loc) == dayKey(row.End, loc)
	if sameDay {
		return dayLabel(row.Start, loc) + " " + timeLabel(row.Start, loc) + "–" + timeLabel(row.End, loc)
	}
	return dayLabel(row.Start, loc) + " " + timeLabel(row.Start, loc) + " .. " + dayLabel(row.End, loc) + " " + timeLabel(row.End, loc)
}

// attendeeSummary lists attendees, first 20 then "… N more"
// (plans/calendar.md §4.4).
func attendeeSummary(attendees []calendarAttendee) string {
	const shown = 20
	parts := make([]string, 0, shown+1)
	for i, attendee := range attendees {
		if i == shown {
			parts = append(parts, fmt.Sprintf("… %d more", len(attendees)-shown))
			break
		}
		name := firstNonEmptyString(attendee.Name, attendee.Address)
		entry := name
		if attendee.Name != "" && attendee.Address != "" {
			entry = name + " <" + attendee.Address + ">"
		}
		if attendee.Response != "" {
			entry += " — " + attendee.Response
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, "\n")
}

// resolveEventReference turns the <event> argument into a full event id
// (plans/calendar.md §4.6):
//
//  1. 7 to 12 lower-case hex characters are a handle, looked up in the cache;
//  2. a string containing outlook.office or outlook.live is a webLink, whose
//     ItemID query parameter is the id;
//  3. anything else is treated as a full event id.
func (a *App) resolveEventReference(ctx context.Context, reference string) (string, error) {
	value := strings.TrimSpace(reference)
	if value == "" {
		return "", output.Usagef("an event is required")
	}
	if isHandle(value) {
		if resolver, err := a.resolver(ctx); err == nil && resolver.Cache() != nil {
			if id, ok := resolver.Cache().Event(strings.ToLower(value)); ok {
				return id, nil
			}
		}
		return "", output.WithHint(output.NotFoundf("no event is cached under the handle %q", value),
			"run `teams calendar list` first, or pass the full event id")
	}
	if isOutlookWebLink(value) {
		id := itemIDFromWebLink(value)
		if id == "" {
			return "", output.Usagef("%q is an Outlook link without an ItemID parameter, so the event cannot be identified", value)
		}
		return id, nil
	}
	return value, nil
}

// isHandle reports whether a value looks like a short event handle: 7 to 12
// lower-case hex characters.
func isHandle(value string) bool {
	if len(value) < calendarHandleLength || len(value) > 12 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// isOutlookWebLink reports whether a value is an Outlook on the web link, which
// carries the event id in its ItemID parameter.
func isOutlookWebLink(value string) bool {
	lower := strings.ToLower(value)
	return strings.Contains(lower, "outlook.office") || strings.Contains(lower, "outlook.live")
}

// itemIDFromWebLink reads the ItemID parameter of an Outlook link, which is
// percent-encoded (plans/calendar.md §4.6).
func itemIDFromWebLink(link string) string {
	parsed, err := url.Parse(link)
	if err != nil {
		return ""
	}
	if id := parsed.Query().Get("ItemID"); id != "" {
		return id
	}
	// Graph percent-encodes the id, so a link that is not a full URL is decoded
	// as a query string instead.
	if _, query, ok := strings.Cut(link, "?"); ok {
		if values, err := url.ParseQuery(query); err == nil {
			if id := values.Get("ItemID"); id != "" {
				return id
			}
		}
	}
	return ""
}

// newCalendarSearchCmd searches the primary calendar.
func (a *App) newCalendarSearchCmd() *cobra.Command {
	var flags listFlags
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search your calendar",
		Long: "Search events in your PRIMARY calendar only — no shared or delegated\n" +
			"calendar is searched, and no other calendar can be asked for. Results are\n" +
			"not sorted by the service and come 25 per page; --limit/--all decide how\n" +
			"many are shown.\n\n" +
			"Results are printed with the same columns and the same --json schema as\n" +
			"`teams calendar list`.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCalendarSearch(cmd.Context(), strings.Join(args, " "), flags)
		},
	}
	cmd.Flags().IntVar(&flags.limit, "limit", graph.DefaultTopSearch, "maximum number of results")
	cmd.Flags().BoolVar(&flags.all, "all", false, "page through every result")
	return cmd
}

// calendarSearchPageSize is the page size event search accepts
// (refs/graph/concepts/search-concept-events.md:21).
const calendarSearchPageSize = 25

func (a *App) runCalendarSearch(ctx context.Context, query string, flags listFlags) error {
	if strings.TrimSpace(query) == "" {
		return output.Usagef("search needs a query")
	}
	if err := a.requireIncrementalScopes(ctx, "teams calendar search",
		[]string{"Calendars.Read", "Calendars.ReadWrite"},
		[]string{"Calendars.Read", "Calendars.Read.Shared"}); err != nil {
		return err
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	loc, _, err := displayLocation("")
	if err != nil {
		return err
	}
	// Built here for the same reason as in `list`: the handles printed below are
	// written to its entity cache, so a later `calendar show <handle>` resolves
	// (plans/calendar.md §4.6).
	if _, rerr := a.resolver(ctx); rerr != nil {
		return rerr
	}
	a.Printer.Statusf("searching your calendar...")

	limit := flags.limitOf(graph.DefaultTopSearch)
	window := rangeWindow{loc: loc, zone: zoneName("")}
	rows := make([]calendarRow, 0, calendarSearchPageSize)
	for from := 0; ; from += calendarSearchPageSize {
		page, err := client.SearchEvents(ctx, query, from, calendarSearchPageSize)
		if err != nil {
			return err
		}
		for _, hit := range page.Hits {
			if limit > 0 && len(rows) >= limit {
				break
			}
			// A hit carries no times, so the event is read back to get the row
			// the listing schema needs (plans/calendar.md §4.4).
			ev, err := client.GetEvent(ctx, hit.HitID, calendarViewSelect)
			if err != nil {
				a.Printer.Debugf("search hit %s could not be read: %v", hit.HitID, err)
				continue
			}
			row, ok := calendarRowFromEvent(ev, calendarTarget{user: "me"}, window)
			if !ok {
				continue
			}
			rows = append(rows, row)
		}
		if !page.MoreResultsAvailable || limit > 0 && len(rows) >= limit {
			break
		}
		if len(page.Hits) == 0 {
			break
		}
	}
	a.assignHandles(rows)
	a.saveEntityCache()
	sortCalendarRows(rows, window)

	if a.Printer.JSONMode() {
		return a.Printer.JSON(calendarJSONRows(rows, window.loc))
	}
	headers := []string{"ID", "DATE", "WHEN", "SUBJECT"}
	table := make([][]string, 0, len(rows))
	for _, row := range rows {
		table = append(table, []string{
			row.Handle,
			dayLabel(row.Start, window.loc),
			calendarWhenLabel(row, window.loc),
			truncateSubject(row),
		})
	}
	a.Printer.Table(headers, table)
	return nil
}
