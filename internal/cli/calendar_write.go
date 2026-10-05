package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
)

// Calendar writes (PLAN.md Phase 6b, plans/calendar.md §4.7): create, update, the
// four responses, cancel and delete.
//
// Three rules shape the code:
//
//   - every verb that reaches other people confirms first, and refuses in
//     non-interactive mode unless --yes was passed (AGENTS.md: never prompt in
//     non-interactive mode);
//   - a pre-check read refuses the requests Graph would answer with a 400 — a
//     response to your own meeting, a cancel you do not organize, an update that
//     would only change your own copy (plans/calendar.md §4.5);
//   - --dry-run prints the request it would send, including the transactionId a
//     create generates, and still runs the read-only pre-checks.

// calendarWriteScope is the scope every calendar write needs. It is incremental:
// no preset carries it (plans/calendar.md §4.8, decision D3).
var calendarWriteScope = []string{"Calendars.ReadWrite", "Calendars.ReadWrite.Shared"}

// calendarRespondFlags are the flags the three responses share.
type calendarRespondFlags struct {
	comment  string
	notify   bool
	noNotify bool
	propose  string
	dryRun   bool
}

// sendsResponse reports whether the response reaches the organizer: --notify is
// on unless --no-notify turns it off.
func (f calendarRespondFlags) sendsResponse() bool { return f.notify && !f.noNotify }

// addTo binds the shared response flags. --notify defaults to true, so the
// useful spelling for the other case is --no-notify (plans/calendar.md §4.1).
func (f *calendarRespondFlags) addTo(cmd *cobra.Command, withPropose bool) {
	cmd.Flags().StringVar(&f.comment, "comment", "", "message to send with the response")
	cmd.Flags().BoolVar(&f.notify, "notify", true, "tell the organizer about the response")
	// --no-notify is bound explicitly: cobra only generates a --no- form for a
	// flag whose name already starts with "no", and the handoff's spelling for
	// this pair is --notify/--no-notify (plans/calendar.md §4.1).
	cmd.Flags().BoolVar(&f.noNotify, "no-notify", false, "do not tell the organizer about the response")
	if withPropose {
		cmd.Flags().StringVar(&f.propose, "propose", "", "propose a new time as <start>/<end>")
	}
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "print the Graph request without sending it")
}

// newCalendarAcceptCmd accepts a meeting invitation.
func (a *App) newCalendarAcceptCmd() *cobra.Command {
	var flags calendarRespondFlags
	cmd := &cobra.Command{
		Use:   "accept <event>",
		Short: "Accept a meeting invitation",
		Long: "Accept a meeting you were invited to. The organizer is notified unless\n" +
			"--no-notify is passed.\n\n" +
			"You cannot respond to a meeting you organize: Graph refuses it, so the CLI\n" +
			"does too, before sending anything.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCalendarRespond(cmd.Context(), graph.ResponseAccept, "teams calendar accept", args[0], flags)
		},
	}
	flags.addTo(cmd, false)
	return cmd
}

// newCalendarTentativeCmd tentatively accepts a meeting invitation.
func (a *App) newCalendarTentativeCmd() *cobra.Command {
	var flags calendarRespondFlags
	cmd := &cobra.Command{
		Use:   "tentative <event>",
		Short: "Tentatively accept a meeting invitation",
		Long: "Tentatively accept a meeting you were invited to, optionally proposing a\n" +
			"different time with --propose.\n\n" +
			"--propose needs --notify (it is a response to the organizer), and the\n" +
			"organizer must allow new-time proposals.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCalendarRespond(cmd.Context(), graph.ResponseTentative, "teams calendar tentative", args[0], flags)
		},
	}
	flags.addTo(cmd, true)
	return cmd
}

// newCalendarDeclineCmd declines a meeting invitation.
func (a *App) newCalendarDeclineCmd() *cobra.Command {
	var flags calendarRespondFlags
	cmd := &cobra.Command{
		Use:   "decline <event>",
		Short: "Decline a meeting invitation",
		Long: "Decline a meeting you were invited to, optionally proposing a different time\n" +
			"with --propose.\n\n" +
			"Declining moves the event to Deleted Items, so it leaves your calendar.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCalendarRespond(cmd.Context(), graph.ResponseDecline, "teams calendar decline", args[0], flags)
		},
	}
	flags.addTo(cmd, true)
	return cmd
}

// runCalendarRespond implements accept, tentative and decline.
func (a *App) runCalendarRespond(ctx context.Context, kind graph.EventResponse, command, reference string, flags calendarRespondFlags) error {
	// --propose is a response the organizer has to receive, so it needs
	// sendResponse: proposedNewTime with sendResponse:false is a 400
	// ErrorInvalidParameter, and the CLI refuses it before the round trip
	// (plans/calendar.md §4.5).
	if flags.propose != "" && !flags.sendsResponse() {
		return output.Usagef("--propose cannot be combined with --no-notify: a proposed time has to reach the organizer")
	}
	if err := a.requireIncrementalScopes(ctx, command, calendarWriteScope, calendarWriteScope); err != nil {
		return err
	}
	zone := zoneName("")
	loc, _, err := displayLocation("")
	if err != nil {
		return err
	}
	var propose *graph.TimeSlot
	if flags.propose != "" {
		slot, perr := parseProposedTime(flags.propose, a.Clock.Now(), loc)
		if perr != nil {
			return perr
		}
		propose = &slot
	}
	if err := a.resolverReady(ctx); err != nil {
		return err
	}
	id, err := a.resolveEventReference(ctx, reference)
	if err != nil {
		return err
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	a.Printer.Statusf("checking the event...")
	pre, err := client.PreCheckEvent(ctx, id)
	if err != nil {
		return err
	}
	if pre.IsOrganizer {
		return output.WithHint(output.Errorf("you are the organizer of this meeting"),
			"an organizer cannot respond to their own meeting; cancel or delete it instead")
	}
	if propose != nil && !pre.AllowNewTimeProposals {
		return output.Errorf("the organizer does not accept proposed times for this meeting")
	}
	path := "/me/events/" + id + "/" + string(kind)
	body := map[string]any{"comment": flags.comment, "sendResponse": flags.sendsResponse()}
	if propose != nil {
		body["proposedNewTime"] = map[string]any{
			"start": map[string]string{"dateTime": propose.Start.In(loc).Format("2006-01-02T15:04:05"), "timeZone": zone},
			"end":   map[string]string{"dateTime": propose.End.In(loc).Format("2006-01-02T15:04:05"), "timeZone": zone},
		}
	}
	if flags.dryRun {
		return a.printDryRun(dryRunDocument{Method: "POST", Path: path, Body: body})
	}
	if err := client.RespondEvent(ctx, id, kind, flags.comment, flags.sendsResponse(), propose, zone); err != nil {
		return err
	}
	a.Printer.Successf("%s %s", responseVerb(kind), firstNonEmptyString(pre.Subject, id))
	return nil
}

// responseVerb is the past-tense verb a response reports.
func responseVerb(kind graph.EventResponse) string {
	switch kind {
	case graph.ResponseAccept:
		return "accepted"
	case graph.ResponseTentative:
		return "tentatively accepted"
	default:
		return "declined"
	}
}

// parseProposedTime parses the --propose value: <start>/<end>, each in any of the
// calendar time forms (plans/calendar.md §4.2).
func parseProposedTime(value string, now time.Time, loc *time.Location) (graph.TimeSlot, error) {
	start, end, ok := strings.Cut(value, "/")
	if !ok {
		return graph.TimeSlot{}, output.Usagef("--propose needs <start>/<end>, for example 2026-10-07 14:00/2026-10-07 14:30")
	}
	startAt, err := parseCalendarTime(start, now, loc)
	if err != nil {
		return graph.TimeSlot{}, err
	}
	endAt, err := parseCalendarTime(end, now, loc)
	if err != nil {
		return graph.TimeSlot{}, err
	}
	if endAt.Before(startAt) {
		return graph.TimeSlot{}, output.Usagef("--propose ends before it starts")
	}
	return graph.TimeSlot{Start: startAt, End: endAt}, nil
}

// newCalendarCancelCmd cancels a meeting the caller organizes.
func (a *App) newCalendarCancelCmd() *cobra.Command {
	var (
		comment string
		yes     bool
		dryRun  bool
	)
	cmd := &cobra.Command{
		Use:   "cancel <event>",
		Short: "Cancel a meeting you organize",
		Long: "Cancel a meeting. Only the organizer can cancel; use `teams calendar\n" +
			"decline` to turn down an invitation instead.\n\n" +
			"Your attendees are told the meeting is cancelled.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCalendarCancel(cmd.Context(), args[0], comment, yes, dryRun)
		},
	}
	cmd.Flags().StringVar(&comment, "comment", "", "message to send to the attendees")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the Graph request without sending it")
	return cmd
}

func (a *App) runCalendarCancel(ctx context.Context, reference, comment string, yes, dryRun bool) error {
	if err := a.requireIncrementalScopes(ctx, "teams calendar cancel", calendarWriteScope, calendarWriteScope); err != nil {
		return err
	}
	if err := a.resolverReady(ctx); err != nil {
		return err
	}
	id, err := a.resolveEventReference(ctx, reference)
	if err != nil {
		return err
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	a.Printer.Statusf("checking the event...")
	pre, err := client.PreCheckEvent(ctx, id)
	if err != nil {
		return err
	}
	if !pre.IsOrganizer {
		return output.WithHint(output.Errorf("only the organizer can cancel %q", firstNonEmptyString(pre.Subject, id)),
			"use `teams calendar decline` to turn down a meeting you were invited to")
	}
	body := map[string]any{"comment": comment}
	if dryRun {
		return a.printDryRun(dryRunDocument{Method: "POST", Path: "/me/events/" + id + "/cancel", Body: body})
	}
	prompt := fmt.Sprintf("cancel %q and tell the attendees?", firstNonEmptyString(pre.Subject, id))
	if err := a.confirm(prompt, yes); err != nil {
		return err
	}
	if err := client.CancelEvent(ctx, id, comment); err != nil {
		return err
	}
	a.Printer.Successf("cancelled %s", firstNonEmptyString(pre.Subject, id))
	return nil
}

// newCalendarDeleteCmd deletes an event.
func (a *App) newCalendarDeleteCmd() *cobra.Command {
	var (
		yes    bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "delete <event>",
		Short: "Delete an event",
		Long: "Delete an event from your calendar.\n\n" +
			"If you organize it and it has attendees, they are sent a cancellation.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCalendarDelete(cmd.Context(), args[0], yes, dryRun)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the Graph request without sending it")
	return cmd
}

func (a *App) runCalendarDelete(ctx context.Context, reference string, yes, dryRun bool) error {
	if err := a.requireIncrementalScopes(ctx, "teams calendar delete", calendarWriteScope, calendarWriteScope); err != nil {
		return err
	}
	if err := a.resolverReady(ctx); err != nil {
		return err
	}
	id, err := a.resolveEventReference(ctx, reference)
	if err != nil {
		return err
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	a.Printer.Statusf("checking the event...")
	pre, err := client.PreCheckEvent(ctx, id)
	if err != nil {
		return err
	}
	if dryRun {
		return a.printDryRun(dryRunDocument{Method: "DELETE", Path: "/me/events/" + id})
	}
	subject := firstNonEmptyString(pre.Subject, id)
	prompt := fmt.Sprintf("delete %q?", subject)
	if pre.IsOrganizer {
		prompt = fmt.Sprintf("delete %q and send cancellations to its attendees?", subject)
	}
	if err := a.confirm(prompt, yes); err != nil {
		return err
	}
	if err := client.DeleteEvent(ctx, id); err != nil {
		return err
	}
	a.Printer.Successf("deleted %s", subject)
	return nil
}

// calendarUpdateFlags are the flags of `calendar update`.
type calendarUpdateFlags struct {
	subject   string
	start     string
	end       string
	duration  string
	location  string
	body      string
	localCopy bool
	dryRun    bool
}

// newCalendarUpdateCmd patches the fields the caller names.
func (a *App) newCalendarUpdateCmd() *cobra.Command {
	var flags calendarUpdateFlags
	cmd := &cobra.Command{
		Use:   "update <event> [--subject] [--start] [--end|--duration] [--location] [--body]",
		Short: "Change the fields you name on an event",
		Long: "Change an event. Only the fields you name are sent, so a change to the time\n" +
			"cannot drop the meeting body or the Teams link.\n\n" +
			"As a non-organizer an update succeeds but changes only YOUR copy, which the\n" +
			"organizer's next update overwrites; pass --local-copy to do it anyway.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runCalendarUpdate(cmd.Context(), args[0], flags)
		},
	}
	cmd.Flags().StringVar(&flags.subject, "subject", "", "new subject")
	cmd.Flags().StringVar(&flags.start, "start", "", "new start time")
	cmd.Flags().StringVar(&flags.end, "end", "", "new end time")
	cmd.Flags().StringVar(&flags.duration, "duration", "", "new length (30m, 1h30m); used when --end is not given")
	cmd.Flags().StringVar(&flags.location, "location", "", "new location")
	cmd.Flags().StringVar(&flags.body, "body", "", "new body text (rewriting a body can drop the Teams meeting details)")
	cmd.Flags().BoolVar(&flags.localCopy, "local-copy", false, "update your own copy even though you are not the organizer")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "print the Graph request without sending it")
	return cmd
}

func (a *App) runCalendarUpdate(ctx context.Context, reference string, flags calendarUpdateFlags) error {
	if err := a.requireIncrementalScopes(ctx, "teams calendar update", calendarWriteScope, calendarWriteScope); err != nil {
		return err
	}
	loc, zone, err := displayLocation("")
	if err != nil {
		return err
	}
	now := a.Clock.Now()
	update := graph.EventUpdate{Zone: zone}
	if flags.subject != "" {
		update.Subject = &flags.subject
	}
	if flags.start != "" {
		start, serr := parseCalendarTime(flags.start, now, loc)
		if serr != nil {
			return serr
		}
		update.Start = &start
	}
	if flags.end != "" {
		if flags.duration != "" {
			return output.Usagef("--end and --duration are mutually exclusive")
		}
		end, eerr := parseCalendarTime(flags.end, now, loc)
		if eerr != nil {
			return eerr
		}
		update.End = &end
	}
	if flags.duration != "" {
		if update.Start == nil {
			return output.Usagef("--duration needs --start: without a new start there is nothing to measure it from")
		}
		d, derr := durationOrDefault(flags.duration)
		if derr != nil {
			return derr
		}
		update.Duration = d
	}
	if flags.location != "" {
		update.Location = &flags.location
	}
	if flags.body != "" {
		update.Body = &flags.body
	}
	if update.Subject == nil && update.Start == nil && update.End == nil && update.Location == nil && update.Body == nil {
		return output.Usagef("nothing to update: pass --subject, --start, --end/--duration, --location or --body")
	}
	if err := a.resolverReady(ctx); err != nil {
		return err
	}
	id, err := a.resolveEventReference(ctx, reference)
	if err != nil {
		return err
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	a.Printer.Statusf("checking the event...")
	pre, err := client.PreCheckEvent(ctx, id)
	if err != nil {
		return err
	}
	if !pre.IsOrganizer && !flags.localCopy {
		return output.WithHint(output.Errorf("you are not the organizer of %q, so this would only change your copy", firstNonEmptyString(pre.Subject, id)),
			"the organizer's next update overwrites it; pass --local-copy to do it anyway")
	}
	if flags.dryRun {
		return a.printDryRun(dryRunDocument{Method: "PATCH", Path: "/me/events/" + id, Body: updateBodyPreview(update, loc, zone)})
	}
	ev, err := client.UpdateEvent(ctx, id, update)
	if err != nil {
		return err
	}
	a.Printer.Successf("updated %s", firstNonEmptyString(ev.Subject, pre.Subject, id))
	return nil
}

// updateBodyPreview renders the PATCH body a --dry-run prints.
func updateBodyPreview(update graph.EventUpdate, loc *time.Location, zone string) map[string]any {
	body := map[string]any{}
	if update.Subject != nil {
		body["subject"] = *update.Subject
	}
	if update.Start != nil {
		body["start"] = map[string]string{"dateTime": update.Start.In(loc).Format("2006-01-02T15:04:05"), "timeZone": zone}
		end := update.End
		if end == nil {
			duration := update.Duration
			if duration <= 0 {
				duration = defaultEventDuration
			}
			derived := update.Start.Add(duration)
			end = &derived
		}
		body["end"] = map[string]string{"dateTime": end.In(loc).Format("2006-01-02T15:04:05"), "timeZone": zone}
	} else if update.End != nil {
		body["end"] = map[string]string{"dateTime": update.End.In(loc).Format("2006-01-02T15:04:05"), "timeZone": zone}
	}
	if update.Location != nil {
		body["location"] = map[string]string{"displayName": *update.Location}
	}
	if update.Body != nil {
		body["body"] = map[string]string{"contentType": "text", "content": *update.Body}
	}
	return body
}

// calendarCreateFlags are the flags of `calendar create`.
type calendarCreateFlags struct {
	whenFlags
	subject   string
	start     string
	end       string
	duration  string
	allDay    bool
	attendees []string
	teams     bool
	location  string
	body      string
	yes       bool
	dryRun    bool
}

// newCalendarCreateCmd creates an event.
func (a *App) newCalendarCreateCmd() *cobra.Command {
	var flags calendarCreateFlags
	cmd := &cobra.Command{
		Use:   "create --subject <s> (--start <time> [--end <time> | --duration <d>] | --all-day --date <day> [--days N])",
		Short: "Create an event, optionally with attendees and a Teams meeting",
		Long: "Create an event. Days and times are LOCAL (--tz, or this machine's zone).\n\n" +
			"Attendees are invited immediately: on a terminal the command asks first, and\n" +
			"anywhere else it needs --yes. --teams asks for a Teams meeting, which the\n" +
			"mailbox has to allow.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runCalendarCreate(cmd.Context(), flags)
		},
	}
	flags.addTo(cmd)
	cmd.Flags().StringVar(&flags.subject, "subject", "", "the event subject")
	cmd.Flags().StringVar(&flags.start, "start", "", "start time (YYYY-MM-DD HH:MM, HH:MM, or RFC3339)")
	cmd.Flags().StringVar(&flags.end, "end", "", "end time")
	cmd.Flags().StringVar(&flags.duration, "duration", "", "length when --end is not given (default 30m)")
	cmd.Flags().BoolVar(&flags.allDay, "all-day", false, "create a floating all-day event instead of a timed one")
	cmd.Flags().StringSliceVar(&flags.attendees, "attendee", nil, "invite this person (repeatable, comma-separated)")
	cmd.Flags().BoolVar(&flags.teams, "teams", false, "make it a Teams online meeting")
	cmd.Flags().StringVar(&flags.location, "location", "", "the location")
	cmd.Flags().StringVar(&flags.body, "body", "", "the event body text")
	cmd.Flags().BoolVar(&flags.yes, "yes", false, "skip the confirmation prompt when there are attendees")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "print the Graph request without sending it")
	return cmd
}

func (a *App) runCalendarCreate(ctx context.Context, flags calendarCreateFlags) error {
	if strings.TrimSpace(flags.subject) == "" {
		return output.Usagef("--subject is required")
	}
	if err := a.requireIncrementalScopes(ctx, "teams calendar create", calendarWriteScope, calendarWriteScope); err != nil {
		return err
	}
	if flags.teams {
		// The chat/join scope is not needed to create, but the mailbox check is a
		// read of the calendar object, which Calendars.ReadWrite covers.
		if err := a.requireIncrementalScopes(ctx, "teams calendar create --teams",
			[]string{"Calendars.Read", "Calendars.ReadWrite"}, []string{"Calendars.Read", "Calendars.Read.Shared"}); err != nil {
			return err
		}
	}
	loc, zone, err := displayLocation(flags.tz)
	if err != nil {
		return err
	}
	now := a.Clock.Now()
	create := graph.EventCreate{
		Subject: flags.subject,
		Zone:    zone,
		// A transactionId makes the create safe to retry: re-posting the same body
		// returns the same event instead of creating a second one
		// (plans/calendar.md §3, F10).
		TransactionID: uuid.NewString(),
		Location:      flags.location,
		Body:          flags.body,
		Teams:         flags.teams,
	}
	switch {
	case flags.allDay:
		if flags.start != "" {
			return output.Usagef("--all-day and --start are mutually exclusive: the day comes from --date")
		}
		window, werr := flags.createDayFlags().window(now, false)
		if werr != nil {
			return werr
		}
		create.Start = window.from
		create.End = window.to.AddDate(0, 0, 1)
		create.AllDay = true
	case flags.start == "":
		return output.Usagef("--start is required unless --all-day is given")
	default:
		start, serr := parseCalendarTime(flags.start, now, loc)
		if serr != nil {
			return serr
		}
		create.Start = start
		if flags.end != "" {
			end, eerr := parseCalendarTime(flags.end, now, loc)
			if eerr != nil {
				return eerr
			}
			create.End = end
		} else {
			duration, derr := durationOrDefault(flags.duration)
			if derr != nil {
				return derr
			}
			create.Duration = duration
		}
	}
	if err := a.resolverReady(ctx); err != nil {
		return err
	}
	for _, raw := range flags.attendees {
		person, perr := a.calendarAttendee(ctx, raw)
		if perr != nil {
			return perr
		}
		create.Attendees = append(create.Attendees, person)
	}
	client, err := a.Graph(ctx)
	if err != nil {
		return err
	}
	if flags.teams {
		providers, perr := client.AllowedOnlineMeetingProviders(ctx)
		if perr != nil {
			return perr
		}
		if !graph.AllowsTeamsMeetings(providers) {
			return output.Errorf("this mailbox cannot create Teams meetings (allowedOnlineMeetingProviders lacks teamsForBusiness)")
		}
	}
	if flags.dryRun {
		return a.printDryRun(dryRunDocument{
			Method: "POST", Path: graph.CalendarEventPath,
			Body: createBodyPreview(create, loc, zone),
			Notes: []string{
				"transactionId makes a retried create return the same event instead of a second one",
				"attendees are invited as soon as the event is created",
			},
		})
	}
	if len(create.Attendees) > 0 {
		prompt := fmt.Sprintf("send invitations to %d %s?", len(create.Attendees), plural(len(create.Attendees), "person", "people"))
		if err := a.confirm(prompt, flags.yes); err != nil {
			return err
		}
	}
	ev, err := client.CreateEvent(ctx, create)
	if err != nil {
		return err
	}
	handle := shortHandle(ev.ID, calendarHandleLength)
	if a.entityCache != nil {
		a.entityCache.PutEvent(handle, ev.ID)
		a.saveEntityCache()
	}
	row := calendarRow{
		Handle: handle, Subject: ev.Subject, Start: create.Start, End: createEnd(create), AllDay: create.AllDay,
	}
	a.Printer.Successf("created %s  %s  %s", handle, calendarWhenLabel(row, loc), firstNonEmptyString(ev.Subject, flags.subject))
	if join := ev.JoinURL(); join != "" {
		a.Printer.Statusf("join: %s", join)
	}
	if flags.teams && ev.JoinURL() == "" {
		// The mailbox accepted the create and dropped the online meeting, which is
		// what Graph does silently; the event exists, so it is not deleted.
		a.Printer.Warnf("the mailbox accepted the event but returned no Teams join URL; the meeting is not online")
		return output.Errorf("the event %s was created without a Teams meeting", handle)
	}
	if a.Printer.JSONMode() {
		row.ID = ev.ID
		if ev.Organizer != nil {
			row.Organizer = ev.Organizer.Name()
			row.OrganizerAddress = ev.Organizer.Address()
		}
		row.Location = flags.location
		row.IsOnline = ev.JoinURL() != ""
		row.JoinURL = ev.JoinURL()
		row.WebLink = ev.WebLink
		row.Response = "organizer"
		row.IsOrganizer = true
		for _, attendee := range create.Attendees {
			if attendee.EmailAddress == nil {
				continue
			}
			row.Attendees = append(row.Attendees, calendarAttendee{
				Name: attendee.EmailAddress.Name, Address: attendee.EmailAddress.Address, Type: graph.AttendeeTypeRequired,
			})
		}
		return a.Printer.JSON(calendarJSONRows([]calendarRow{row}, loc)[0])
	}
	return nil
}

// createDayFlags returns the day flags for an --all-day create, with the defaults
// filled in so an unset --date/--days still means today.
func (f calendarCreateFlags) createDayFlags() whenFlags {
	flags := f.whenFlags
	if flags.date == "" {
		flags.date = "today"
	}
	if flags.days < 1 {
		flags.days = 1
	}
	return flags
}

// calendarAttendee resolves one --attendee value to a recipient.
//
// An address or a UPN is used as written, so the calendar scopes alone are
// enough; a display name goes through the directory, which needs
// User.ReadBasic.All or People.Read (the same rule as --user).
func (a *App) calendarAttendee(ctx context.Context, raw string) (graph.Recipient, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return graph.Recipient{}, output.Usagef("--attendee needs a person")
	}
	if looksLikeAddress(value) {
		return graph.Recipient{EmailAddress: &graph.EmailAddress{Name: value, Address: value}}, nil
	}
	resolver, err := a.resolver(ctx)
	if err != nil {
		return graph.Recipient{}, err
	}
	person, err := resolver.Person(ctx, value)
	if err != nil {
		return graph.Recipient{}, err
	}
	address := firstNonEmptyString(person.UserMail, person.UserName)
	if address == "" {
		return graph.Recipient{}, output.NotFoundf("no e-mail address for %q", value)
	}
	return graph.Recipient{EmailAddress: &graph.EmailAddress{Name: person.UserName, Address: address}}, nil
}

// createEnd is the instant a create ends: End when the caller gave one, otherwise
// Start plus the duration (or the 30-minute default). The Graph wrapper applies
// the same rule, so the reported range always matches what was sent.
func createEnd(create graph.EventCreate) time.Time {
	if !create.End.IsZero() {
		return create.End
	}
	duration := create.Duration
	if duration <= 0 {
		duration = defaultEventDuration
	}
	return create.Start.Add(duration)
}

// createBodyPreview renders the POST body a --dry-run prints.
func createBodyPreview(create graph.EventCreate, loc *time.Location, zone string) map[string]any {
	start := create.Start.In(loc).Format("2006-01-02T15:04:05")
	end := createEnd(create)
	body := map[string]any{
		"subject":       create.Subject,
		"start":         map[string]string{"dateTime": start, "timeZone": zone},
		"end":           map[string]string{"dateTime": end.In(loc).Format("2006-01-02T15:04:05"), "timeZone": zone},
		"transactionId": create.TransactionID,
	}
	if create.AllDay {
		body["isAllDay"] = true
	}
	if create.Location != "" {
		body["location"] = map[string]string{"displayName": create.Location}
	}
	if create.Body != "" {
		body["body"] = map[string]string{"contentType": "text", "content": create.Body}
	}
	if len(create.Attendees) > 0 {
		attendees := make([]map[string]any, 0, len(create.Attendees))
		for _, attendee := range create.Attendees {
			if attendee.EmailAddress == nil {
				continue
			}
			attendees = append(attendees, map[string]any{
				"emailAddress": map[string]string{"name": attendee.EmailAddress.Name, "address": attendee.EmailAddress.Address},
				"type":         graph.AttendeeTypeRequired,
			})
		}
		body["attendees"] = attendees
	}
	if create.Teams {
		body["isOnlineMeeting"] = true
		body["onlineMeetingProvider"] = graph.OnlineMeetingProviderTeams
	}
	return body
}

// plural picks between two words for a count.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// resolverReady builds the resolver (and with it the entity cache) so a handle
// printed by a previous command can be resolved, and one printed here is stored.
func (a *App) resolverReady(ctx context.Context) error {
	_, err := a.resolver(ctx)
	return err
}
