package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/output"
)

// This file is the calendar command group's own time grammar
// (plans/calendar.md §4.2): the <day> values, the day/range flags, the display
// location and the wall-clock times the write commands send.
//
// It is deliberately separate from parseTimeFlag in read.go, which treats a bare
// date as UTC and answers "how long ago" durations. A calendar day is a local
// day: "today" means today where the user is, and an all-day event must not be
// shifted by a timezone conversion. The two grammars must not be merged, because
// `--since 24h` and `--date tomorrow` mean genuinely different things.

// maxLocalRangeDays is the calendarView cap for the signed-in user's own
// calendar. The server limit is 1825 days and the client widens every window by
// a day on each side to catch floating all-day events, so the local cap leaves
// room for that widening (plans/calendar.md §4.2).
const maxLocalRangeDays = 1820

// maxSharedRangeDays is the getSchedule cap, and therefore the cap whenever the
// listing touches another user's calendar or asks for free/busy
// (plans/calendar.md §4.2; §3, F7).
const maxSharedRangeDays = 62

// whenFlags are the day, range and zone flags every calendar read command
// shares.
type whenFlags struct {
	date      string
	days      int
	from      string
	to        string
	tz        string
	cancelled bool
	// dateSet and daysSet record whether the user passed --date/--days. They
	// cannot be inferred from the values: --date defaults to "today" and --days
	// to 1, and a range has to be allowed to coexist with those defaults while
	// still refusing an explicit combination (plans/calendar.md §4.2).
	dateSet bool
	daysSet bool
	// cmd is the command the flags are bound to, when there is one. It is how
	// flagSet reports an explicitly passed flag.
	cmd *cobra.Command
}

// flagSet reports whether a flag was passed explicitly on the command line. With
// no command (a struct built by a test) it falls back to the recorded bool.
func (f whenFlags) flagSet(name string, recorded bool) bool {
	if f.cmd != nil {
		return f.cmd.Flags().Changed(name)
	}
	return recorded
}

// addTo binds the shared calendar window flags.
func (f *whenFlags) addTo(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.date, "date", "today", "the day to show: today, tomorrow, yesterday, YYYY-MM-DD, +Nd, -Nd or a weekday name")
	cmd.Flags().IntVar(&f.days, "days", 1, "how many days to show from --date")
	cmd.Flags().StringVar(&f.from, "from", "", "first day of a range (inclusive); must be paired with --to")
	cmd.Flags().StringVar(&f.to, "to", "", "last day of a range (inclusive); must be paired with --from")
	cmd.Flags().StringVar(&f.tz, "tz", "", "IANA time zone for the days and times (default: this machine's)")
	cmd.Flags().BoolVar(&f.cancelled, "include-cancelled", false, "also show events that were cancelled")
	// The command is kept so window() can tell an explicit --date/--days from a
	// default value. That has to happen through cmd.Flags().Changed rather than in
	// a PreRunE hook: cobra generates its own PreRunE to implement a boolean flag's
	// --no- form (--no-notify on the response verbs), and overwriting it disables
	// that flag.
	f.cmd = cmd
}

// rangeWindow is the resolved request: the local days asked for and the widened
// window that is sent to Graph.
type rangeWindow struct {
	// loc is the display location: every day and time in the output is rendered
	// in it.
	loc *time.Location
	// zone is the IANA name of loc, for the requests that need one.
	zone string
	// from and to are the first and last requested local days.
	from, to time.Time
	// start and end are the widened window [start, end) sent to Graph: one day
	// on each side, because all-day events are floating and the server matches
	// them as UTC midnight to midnight (plans/calendar.md §3, F5).
	start, end time.Time
	// shared marks a window that has to respect the 62-day getSchedule cap
	// rather than the 1820-day calendarView cap.
	shared bool
	// widened records whether the ±1-day widening around the requested days
	// actually happened. A one-day window that is already at the server's range
	// cap cannot be widened, and the caller must then filter all-day events by
	// their exact dates instead (plans/calendar.md §3, F5).
	widened bool
}

// days returns how many local days the window covers.
func (w rangeWindow) days() int {
	if w.to.Before(w.from) {
		return 0
	}
	return int(w.to.Sub(w.from).Hours()/24) + 1
}

// coversOneDay reports whether the range is a single day, which is what decides
// whether the table shows a Date column.
func (w rangeWindow) coversOneDay() bool { return w.days() == 1 }

// window resolves the day/range flags against the injected clock.
//
// shared selects the range cap: another user's calendar or --free-busy goes
// through getSchedule, which the live service caps at 62 days
// (plans/calendar.md §4.2).
func (f whenFlags) window(now time.Time, shared bool) (rangeWindow, error) {
	var w rangeWindow
	loc, zone, err := displayLocation(f.tz)
	if err != nil {
		return w, err
	}
	w.loc, w.zone, w.shared = loc, zone, shared

	if f.from != "" || f.to != "" {
		switch {
		case f.from == "":
			return w, output.Usagef("--to needs --from as well")
		case f.to == "":
			return w, output.Usagef("--from needs --to as well")
		case f.flagSet("date", f.dateSet):
			return w, output.Usagef("--from/--to cannot be combined with --date")
		case f.flagSet("days", f.daysSet):
			return w, output.Usagef("--from/--to cannot be combined with --days")
		}
		from, err := parseCalendarDay(f.from, now, loc)
		if err != nil {
			return w, err
		}
		to, err := parseCalendarDay(f.to, now, loc)
		if err != nil {
			return w, err
		}
		if to.Before(from) {
			return w, output.Usagef("--to (%s) is before --from (%s)", f.to, f.from)
		}
		w.from, w.to = from, to
	} else {
		if f.days < 1 {
			return w, output.Usagef("--days must be at least 1")
		}
		from, err := parseCalendarDay(f.date, now, loc)
		if err != nil {
			return w, err
		}
		w.from = from
		w.to = from.AddDate(0, 0, f.days-1)
	}

	got := w.days()
	limit := maxLocalRangeDays
	if shared {
		limit = maxSharedRangeDays
	}
	if got > limit {
		if shared {
			return w, output.Usagef("a range of %d days is too long for another user's calendar or --free-busy: the server caps free/busy at %d days", got, maxSharedRangeDays)
		}
		return w, output.Usagef("a range of %d days is too long: the server caps a calendar view at %d days", got, maxLocalRangeDays)
	}

	// The window is [midnight(from), midnight(to+1)) in the display location,
	// widened by one day on each side because all-day events are floating and
	// the server matches them as UTC midnight to midnight (plans/calendar.md §3,
	// F5). time.Date keeps a DST day correct: a 25-hour day still runs midnight
	// to midnight.
	w.start = midnight(w.from, loc)
	w.end = midnight(w.to.AddDate(0, 0, 1), loc)

	// The request must still be a legal range: the server refuses a calendarView
	// over 1825 days and a getSchedule over 62. A window at the cap therefore
	// gives up its widening instead of its validity, and the caller filters
	// all-day events exactly.
	serverCap := maxLocalRangeDays + 1
	if shared {
		serverCap = maxSharedRangeDays
	}
	widened := true
	if int(w.end.AddDate(0, 0, 1).Sub(w.start.AddDate(0, 0, -1)).Hours()/24) > serverCap {
		widened = false
	} else {
		w.start = w.start.AddDate(0, 0, -1)
		w.end = w.end.AddDate(0, 0, 1)
	}
	w.widened = widened
	return w, nil
}

// midnight returns the start of t's local day in loc.
func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// parseCalendarDay resolves a <day> value to the midnight of that local day.
//
// The accepted forms are the handoff's (plans/calendar.md §4.2): today,
// tomorrow, yesterday, YYYY-MM-DD, +Nd/-Nd (N ≥ 0) and a weekday name, which
// means the next occurrence including today.
func parseCalendarDay(value string, now time.Time, loc *time.Location) (time.Time, error) {
	raw := strings.ToLower(strings.TrimSpace(value))
	if raw == "" {
		return time.Time{}, output.Usagef("--date needs a day")
	}
	switch raw {
	case "today":
		return midnight(now, loc), nil
	case "tomorrow":
		return midnight(now, loc).AddDate(0, 0, 1), nil
	case "yesterday":
		return midnight(now, loc).AddDate(0, 0, -1), nil
	}
	// +Nd / -Nd, including +0d and -0d.
	if (raw[0] == '+' || raw[0] == '-') && strings.HasSuffix(raw, "d") {
		digits := raw[1 : len(raw)-1]
		n, err := strconv.Atoi(digits)
		if err != nil || n < 0 {
			return time.Time{}, output.Usagef("%q is not a day: use +Nd or -Nd with N of at least 0", value)
		}
		if raw[0] == '-' {
			n = -n
		}
		return midnight(now, loc).AddDate(0, 0, n), nil
	}
	if wd, ok := weekdayNumber(raw); ok {
		today := midnight(now, loc)
		delta := int(wd - today.Weekday())
		if delta < 0 {
			delta += 7
		}
		return today.AddDate(0, 0, delta), nil
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, loc); err == nil {
		return t, nil
	}
	return time.Time{}, output.Usagef("%q is not a day: use today, tomorrow, yesterday, YYYY-MM-DD, +Nd, -Nd or a weekday name", value)
}

// weekdayNumber maps a weekday name to its time.Weekday. Both the three-letter
// and the full spelling are accepted, because both read naturally in a command.
func weekdayNumber(name string) (time.Weekday, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "sun", "sunday":
		return time.Sunday, true
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tues", "tuesday":
		return time.Tuesday, true
	case "wed", "wednesday":
		return time.Wednesday, true
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	default:
		return 0, false
	}
}

// displayLocation resolves the location every day and time in a calendar command
// is rendered in: --tz when given, otherwise this machine's own zone.
func displayLocation(tz string) (*time.Location, string, error) {
	if name := strings.TrimSpace(tz); name != "" {
		loc, err := time.LoadLocation(name)
		if err != nil {
			return nil, "", output.Usagef("--tz %q is not a known IANA time zone (for example Europe/Amsterdam or Asia/Tokyo)", tz)
		}
		return loc, name, nil
	}
	return localLocation(), zoneName(tz), nil
}

// localLocation is this machine's zone when nothing was asked for.
//
// $TZ is consulted first, and that is not the same thing as time.Local: Go reads
// TZ for time.Local on unix only, so a Windows run that was told TZ=Asia/Tokyo
// would render every time in the runner's own zone while the request bodies kept
// naming Asia/Tokyo. Reading the variable here is what keeps the rendered times
// and the requested zone in step on all three platforms, and it is what the
// tests' `env TZ=...` relies on.
func localLocation() *time.Location {
	if name := strings.TrimSpace(os.Getenv("TZ")); name != "" {
		if loc, err := time.LoadLocation(name); err == nil {
			return loc
		}
	}
	if runtime.GOOS == "windows" {
		// There is no /etc/localtime and no TZ support in time.Local here, and the
		// runner's zone is unknown, so UTC is the only honest answer: it is also
		// what zoneName falls back to.
		return time.UTC
	}
	return time.Local
}

// zoneName returns the IANA name a request body needs.
//
// The order is the handoff's (plans/calendar.md §4.2): the --tz value, then $TZ
// when it loads, then the target of the /etc/localtime symlink, then "UTC". The
// name matters because Graph answers 400 TimeZoneNotSupportedException for an
// unknown zone rather than falling back (§3, F6), so a wrong name is worse than
// no name at all.
func zoneName(tz string) string {
	if name := strings.TrimSpace(tz); name != "" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	if name := strings.TrimSpace(os.Getenv("TZ")); name != "" {
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	if runtime.GOOS != "windows" {
		if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
			const marker = "zoneinfo/"
			if i := strings.LastIndex(target, marker); i >= 0 {
				if name := strings.TrimPrefix(target[i+len(marker):], "/"); name != "" {
					if _, err := time.LoadLocation(name); err == nil {
						return name
					}
				}
			}
		}
	}
	return "UTC"
}

// parseCalendarTime accepts the time forms the write commands take
// (plans/calendar.md §4.2): an RFC3339 stamp, "YYYY-MM-DD HH:MM",
// "YYYY-MM-DDTHH:MM", or "HH:MM" meaning today in the display location.
func parseCalendarTime(value string, now time.Time, loc *time.Location) (time.Time, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return time.Time{}, output.Usagef("a time is required")
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.In(loc), nil
	}
	layouts := []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05"}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, raw, loc); err == nil {
			return t, nil
		}
	}
	// A bare time is today: that is the natural reading of "calendar create
	// --start 14:00".
	for _, layout := range []string{"15:04", "15:04:05"} {
		if clock, err := time.ParseInLocation(layout, raw, loc); err == nil {
			y, m, d := now.In(loc).Date()
			return time.Date(y, m, d, clock.Hour(), clock.Minute(), clock.Second(), 0, loc), nil
		}
	}
	return time.Time{}, output.Usagef("%q is not a time: use YYYY-MM-DD HH:MM, YYYY-MM-DDTHH:MM, an RFC3339 stamp or HH:MM", value)
}

// durationOrDefault parses a --duration value, defaulting to 30 minutes.
func durationOrDefault(value string) (time.Duration, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return defaultEventDuration, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, output.Usagef("--duration %q is not a positive Go duration (for example 30m, 1h30m or 90m)", value)
	}
	return d, nil
}

// defaultEventDuration is the default meeting length for `calendar create`
// (plans/calendar.md §4.2).
const defaultEventDuration = 30 * time.Minute

// timeLabel renders a single time in the display location, for a table cell.
func timeLabel(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("15:04")
}

// dayLabel renders a day the way the Date column shows it: 2026-10-07 Wed.
func dayLabel(t time.Time, loc *time.Location) string {
	return t.In(loc).Format("2006-01-02 Mon")
}

// rangeLabel describes a window for a header or a status line.
func rangeLabel(w rangeWindow) string {
	if w.coversOneDay() {
		return w.from.Format("2006-01-02 Mon")
	}
	return fmt.Sprintf("%s..%s", w.from.Format("2006-01-02"), w.to.Format("2006-01-02"))
}
