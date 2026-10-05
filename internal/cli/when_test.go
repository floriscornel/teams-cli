package cli

import (
	"testing"
	"time"
)

// Tests for the calendar time grammar (plans/calendar.md §4.2). Every case is
// driven by an injected clock, so nothing here depends on the machine's date,
// zone or DST rules beyond the zones the test loads itself.

// whenNow is the frozen instant the day tests are written against: a Thursday,
// in UTC, so "next Monday" is a real skip rather than tomorrow.
var whenNow = time.Date(2026, 1, 3, 14, 30, 0, 0, time.UTC)

func whenLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return loc
}

func TestParseCalendarDay(t *testing.T) {
	utc := time.UTC
	cases := map[string]struct {
		value string
		loc   *time.Location
		want  string
	}{
		"today":                    {"today", utc, "2026-01-03"},
		"tomorrow":                 {"tomorrow", utc, "2026-01-04"},
		"yesterday":                {"yesterday", utc, "2026-01-02"},
		"a date":                   {"2026-10-07", utc, "2026-10-07"},
		"uppercase":                {"TOMORROW", utc, "2026-01-04"},
		"padded":                   {"  today ", utc, "2026-01-03"},
		"plus zero days":           {"+0d", utc, "2026-01-03"},
		"plus days":                {"+5d", utc, "2026-01-08"},
		"minus zero days":          {"-0d", utc, "2026-01-03"},
		"minus days":               {"-3d", utc, "2025-12-31"},
		"today is a saturday":      {"sat", utc, "2026-01-03"},
		"next sunday":              {"sun", utc, "2026-01-04"},
		"monday skips the weekend": {"monday", utc, "2026-01-05"},
		"long weekday name":        {"thursday", utc, "2026-01-08"},
		"wednesday wraps forward":  {"wednesday", utc, "2026-01-07"},
		"abbreviated":              {"fri", utc, "2026-01-09"},
		"tues":                     {"tues", utc, "2026-01-06"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseCalendarDay(tc.value, whenNow, tc.loc)
			if err != nil {
				t.Fatalf("parseCalendarDay(%q): %v", tc.value, err)
			}
			if want, _ := time.ParseInLocation("2006-01-02", tc.want, tc.loc); !got.Equal(want) {
				t.Errorf("parseCalendarDay(%q) = %s, want %s", tc.value, got, tc.want)
			}
			if h, m, s := got.Clock(); h != 0 || m != 0 || s != 0 {
				t.Errorf("parseCalendarDay(%q) = %s, want midnight", tc.value, got)
			}
		})
	}
}

func TestParseCalendarDayRejects(t *testing.T) {
	for _, value := range []string{"", "   ", "next week", "+d", "+xd", "-1w", "2030-13-01", "01/02/2030", "mondayish", "now"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseCalendarDay(value, whenNow, time.UTC); err == nil {
				t.Fatalf("parseCalendarDay(%q) was accepted", value)
			}
		})
	}
}

func TestWindowDefaultsToOneLocalDay(t *testing.T) {
	tokyo := whenLoc(t, "Asia/Tokyo")
	f := whenFlags{date: "today", days: 1, tz: "Asia/Tokyo"}
	w, err := f.window(whenNow, false)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if !w.coversOneDay() || w.days() != 1 {
		t.Errorf("days() = %d, want 1", w.days())
	}
	if w.zone != "Asia/Tokyo" || w.loc.String() != tokyo.String() {
		t.Errorf("zone = %q loc = %v", w.zone, w.loc)
	}
	// The requested day is Tokyo's 2026-01-03 (whenNow is 2026-01-03T14:30Z,
	// which is 23:30 in Tokyo). The window must be that local day, widened one
	// day on each side (plans/calendar.md §3, F5).
	wantFrom := time.Date(2026, 1, 3, 0, 0, 0, 0, tokyo)
	if !w.from.Equal(wantFrom) {
		t.Errorf("from = %s, want %s", w.from, wantFrom)
	}
	if !w.widened {
		t.Fatal("a one-day window should be widened")
	}
	wantStart := time.Date(2026, 1, 2, 0, 0, 0, 0, tokyo)
	wantEnd := time.Date(2026, 1, 5, 0, 0, 0, 0, tokyo)
	if !w.start.Equal(wantStart) {
		t.Errorf("start = %s, want %s (one day before the local day)", w.start, wantStart)
	}
	if !w.end.Equal(wantEnd) {
		t.Errorf("end = %s, want %s (exclusive: midnight after the day, plus one)", w.end, wantEnd)
	}
}

func TestWindowDaysAndRanges(t *testing.T) {
	cases := map[string]struct {
		flags whenFlags
		days  int
		from  string
		to    string
	}{
		"--days 3":            {whenFlags{date: "today", days: 3}, 3, "2026-01-03", "2026-01-05"},
		"--days 1":            {whenFlags{date: "tomorrow", days: 1}, 1, "2026-01-04", "2026-01-04"},
		"--from/--to":         {whenFlags{from: "mon", to: "fri"}, 5, "2026-01-05", "2026-01-09"},
		"--from/--to one day": {whenFlags{from: "2026-02-02", to: "2026-02-02"}, 1, "2026-02-02", "2026-02-02"},
		"--from/--to span":    {whenFlags{from: "2026-01-01", to: "2026-01-31"}, 31, "2026-01-01", "2026-01-31"},
		"--days with --date":  {whenFlags{date: "2026-01-05", days: 2}, 2, "2026-01-05", "2026-01-06"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, err := tc.flags.window(whenNow, false)
			if err != nil {
				t.Fatalf("window: %v", err)
			}
			if w.days() != tc.days {
				t.Errorf("days() = %d, want %d", w.days(), tc.days)
			}
			if got := w.from.Format("2006-01-02"); got != tc.from {
				t.Errorf("from = %s, want %s", got, tc.from)
			}
			if got := w.to.Format("2006-01-02"); got != tc.to {
				t.Errorf("to = %s, want %s", got, tc.to)
			}
		})
	}
}

func TestWindowRejectsUsageErrors(t *testing.T) {
	cases := map[string]whenFlags{
		"--days 0":              {date: "today", days: 0},
		"--days negative":       {date: "today", days: -3},
		"--to alone":            {to: "friday"},
		"--from alone":          {from: "monday"},
		"--from with --date":    {from: "mon", to: "fri", date: "tomorrow", days: 1, dateSet: true},
		"--from with --days":    {from: "mon", to: "fri", days: 2, daysSet: true},
		"--to before --from":    {from: "friday", to: "monday", days: 1},
		"bad day":               {date: "someday", days: 1},
		"bad tz":                {date: "today", days: 1, tz: "Mars/Olympus"},
		"own range over cap":    {from: "2026-01-01", to: "2032-01-01", days: 1},
		"shared range over cap": {from: "2026-01-01", to: "2026-06-01", days: 1},
	}
	for name, flags := range cases {
		t.Run(name, func(t *testing.T) {
			shared := name == "shared range over cap"
			if _, err := flags.window(whenNow, shared); err == nil {
				t.Fatalf("window(%+v, shared=%v) was accepted", flags, shared)
			}
		})
	}
}

func TestWindowCapsByNameTheLimit(t *testing.T) {
	// 62 days is the free/busy cap and 63 is over it; 1820 days is the local cap
	// and 1821 is over it (plans/calendar.md §4.2).
	if _, err := (whenFlags{from: "2026-01-01", to: "2026-03-03", days: 1}).window(whenNow, true); err != nil {
		t.Errorf("a 62-day shared window was refused: %v", err)
	}
	if _, err := (whenFlags{from: "2026-01-01", to: "2026-03-04", days: 1}).window(whenNow, true); err == nil {
		t.Error("a 63-day shared window was accepted")
	} else if !contains(err.Error(), "62") {
		t.Errorf("the error does not name the 62-day limit: %v", err)
	}
	if _, err := (whenFlags{from: "2026-01-01", to: "2030-12-25", days: 1}).window(whenNow, false); err != nil {
		t.Errorf("a 1820-day window was refused: %v", err)
	}
	if _, err := (whenFlags{from: "2026-01-01", to: "2030-12-26", days: 1}).window(whenNow, false); err == nil {
		t.Error("a 1821-day window was accepted")
	} else if !contains(err.Error(), "1820") {
		t.Errorf("the error does not name the 1820-day limit: %v", err)
	}
}

func TestWindowAtTheServerCapGivesUpTheWidening(t *testing.T) {
	// A 62-day free/busy window is already at the getSchedule cap, so it cannot
	// be widened; the caller then filters all-day events by their exact dates.
	w, err := (whenFlags{from: "2026-01-01", to: "2026-03-03", days: 1}).window(whenNow, true)
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if w.widened {
		t.Error("a window at the server cap reported itself as widened")
	}
	if got := w.start.Format("2006-01-02"); got != "2026-01-01" {
		t.Errorf("start = %s, want the unwidened 2026-01-01", got)
	}
	if got := w.end.Format("2006-01-02"); got != "2026-03-04" {
		t.Errorf("end = %s, want the exclusive 2026-03-04", got)
	}
	if span := int(w.end.Sub(w.start).Hours() / 24); span != 62 {
		t.Errorf("the unwidened span is %d days, want 62", span)
	}
}

func TestWindowHandlesDSTDays(t *testing.T) {
	cases := map[string]struct {
		zone  string
		date  string
		hours float64
	}{
		// Europe/Amsterdam's DST ends on 2026-10-25: a 25-hour day.
		"amsterdam 25-hour day": {"Europe/Amsterdam", "2026-10-25", 25},
		// America/New_York's DST starts on 2026-03-08: a 23-hour day.
		"new york 23-hour day": {"America/New_York", "2026-03-08", 23},
		// A normal day, so the two above are not magic.
		"amsterdam normal day": {"Europe/Amsterdam", "2026-10-20", 24},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w, err := (whenFlags{date: tc.date, days: 1, tz: tc.zone}).window(whenNow, false)
			if err != nil {
				t.Fatalf("window: %v", err)
			}
			// The requested day itself is midnight to midnight local, which is
			// what time.Date guarantees across a DST boundary.
			day := midnight(w.from, w.loc)
			next := midnight(w.to.AddDate(0, 0, 1), w.loc)
			if got := next.Sub(day).Hours(); got != tc.hours {
				t.Errorf("the local day is %.0f hours, want %.0f", got, tc.hours)
			}
			if h, m, _ := w.from.Clock(); h != 0 || m != 0 {
				t.Errorf("from = %s, want local midnight", w.from)
			}
		})
	}
}

func TestZoneNamePrecedence(t *testing.T) {
	// An explicit --tz wins, and an unusable one is ignored rather than sent:
	// Graph answers 400 TimeZoneNotSupportedException for a zone it does not
	// know, so a bad name is worse than none (plans/calendar.md §3, F6).
	if got := zoneName("Asia/Tokyo"); got != "Asia/Tokyo" {
		t.Errorf("zoneName(Asia/Tokyo) = %q", got)
	}
	if got := zoneName("Mars/Olympus"); got == "Mars/Olympus" {
		t.Errorf("zoneName kept an unusable zone: %q", got)
	}
	t.Setenv("TZ", "Europe/Amsterdam")
	if got := zoneName(""); got != "Europe/Amsterdam" {
		t.Errorf("zoneName(\"\") = %q, want the $TZ value", got)
	}
	if got := zoneName("Mars/Olympus"); got != "Europe/Amsterdam" {
		t.Errorf("zoneName with a bad --tz = %q, want $TZ", got)
	}
	t.Setenv("TZ", "not a zone")
	if got := zoneName(""); got != "" && !isKnownZone(got) {
		t.Errorf("zoneName fell back to an unusable zone: %q", got)
	}
}

func TestParseCalendarTime(t *testing.T) {
	amsterdam := whenLoc(t, "Europe/Amsterdam")
	cases := map[string]struct {
		value string
		want  string
	}{
		"stamp":           {"2026-10-07 10:00", "2026-10-07T10:00:00+02:00"},
		"stamp with T":    {"2026-10-07T10:00", "2026-10-07T10:00:00+02:00"},
		"stamp with secs": {"2026-10-07 10:00:30", "2026-10-07T10:00:30+02:00"},
		"RFC3339":         {"2026-10-07T10:00:00Z", "2026-10-07T12:00:00+02:00"},
		"RFC3339 offset":  {"2026-10-07T10:00:00+09:00", "2026-10-07T03:00:00+02:00"},
		"bare time today": {"14:00", "2026-01-03T14:00:00+01:00"},
		"bare time secs":  {"14:00:15", "2026-01-03T14:00:15+01:00"},
		"padded":          {" 09:30 ", "2026-01-03T09:30:00+01:00"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := parseCalendarTime(tc.value, whenNow, amsterdam)
			if err != nil {
				t.Fatalf("parseCalendarTime(%q): %v", tc.value, err)
			}
			want, err := time.Parse(time.RFC3339, tc.want)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(want) {
				t.Errorf("parseCalendarTime(%q) = %s, want %s", tc.value, got.Format(time.RFC3339), tc.want)
			}
			if got.Location().String() != amsterdam.String() {
				t.Errorf("parseCalendarTime(%q) returned %s, want the display location", tc.value, got.Location())
			}
		})
	}
}

func TestParseCalendarTimeRejects(t *testing.T) {
	for _, value := range []string{"", "  ", "tomorrow", "10", "10:0", "2026-13-01 10:00", "10:00 yesterday"} {
		t.Run(value, func(t *testing.T) {
			if _, err := parseCalendarTime(value, whenNow, time.UTC); err == nil {
				t.Fatalf("parseCalendarTime(%q) was accepted", value)
			}
		})
	}
}

func TestDurationOrDefault(t *testing.T) {
	if got, err := durationOrDefault(""); err != nil || got != defaultEventDuration {
		t.Errorf("durationOrDefault(\"\") = (%v, %v), want the 30m default", got, err)
	}
	for value, want := range map[string]time.Duration{"1h30m": 90 * time.Minute, "45m": 45 * time.Minute, "2h": 2 * time.Hour} {
		if got, err := durationOrDefault(value); err != nil || got != want {
			t.Errorf("durationOrDefault(%q) = (%v, %v), want %v", value, got, err, want)
		}
	}
	for _, value := range []string{"0m", "-5m", "an hour", "1h30"} {
		if _, err := durationOrDefault(value); err == nil {
			t.Errorf("durationOrDefault(%q) was accepted", value)
		}
	}
}

func TestDayAndTimeLabels(t *testing.T) {
	amsterdam := whenLoc(t, "Europe/Amsterdam")
	noon := time.Date(2026, 10, 7, 12, 0, 0, 0, amsterdam)
	if got := timeLabel(noon, amsterdam); got != "12:00" {
		t.Errorf("timeLabel = %q", got)
	}
	if got := dayLabel(noon, amsterdam); got != "2026-10-07 Wed" {
		t.Errorf("dayLabel = %q", got)
	}
	tokyo := whenLoc(t, "Asia/Tokyo")
	if got := timeLabel(noon, tokyo); got != "19:00" {
		t.Errorf("timeLabel in Tokyo = %q, want 19:00", got)
	}
	w, err := (whenFlags{from: "2026-10-05", to: "2026-10-07", days: 1}).window(whenNow, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := rangeLabel(w); got != "2026-10-05..2026-10-07" {
		t.Errorf("rangeLabel = %q", got)
	}
	one, err := (whenFlags{date: "2026-10-07", days: 1}).window(whenNow, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := rangeLabel(one); got != "2026-10-07 Wed" {
		t.Errorf("rangeLabel for one day = %q", got)
	}
}

// contains is a small strings.Contains, kept local so the test file's imports
// stay minimal.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// isKnownZone reports whether time.LoadLocation accepts a name, for the zoneName
// fallback assertion.
func isKnownZone(name string) bool {
	_, err := time.LoadLocation(name)
	return err == nil
}

func TestLocalLocationHonoursTZ(t *testing.T) {
	// The display location has to follow $TZ, not time.Local: Go reads TZ for
	// time.Local on unix only, so a Windows run told TZ=Asia/Tokyo would render
	// every time in the runner's own zone while the request bodies kept naming
	// Asia/Tokyo (which is what the Windows CI run of the calendar scripts
	// showed).
	t.Setenv("TZ", "Asia/Tokyo")
	loc := localLocation()
	if loc.String() != "Asia/Tokyo" {
		t.Fatalf("localLocation() = %s, want Asia/Tokyo", loc)
	}
	loc2, zone, err := displayLocation("")
	if err != nil {
		t.Fatalf("displayLocation: %v", err)
	}
	if loc2.String() != "Asia/Tokyo" || zone != "Asia/Tokyo" {
		t.Errorf("displayLocation(\"\") = (%s, %q), want Asia/Tokyo for both", loc2, zone)
	}
	// The two must agree: a request that names one zone and renders another is
	// the bug this guards.
	if loc2.String() != zoneName("") {
		t.Errorf("the display location is %s but the request would name %q", loc2, zoneName(""))
	}

	// An unusable TZ falls back instead of failing.
	t.Setenv("TZ", "not a zone")
	if got := localLocation(); got == nil {
		t.Error("localLocation() = nil for an unusable TZ")
	}
	// --tz still wins over the environment.
	t.Setenv("TZ", "Asia/Tokyo")
	loc3, zone3, err := displayLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	if loc3.String() != "Europe/Amsterdam" || zone3 != "Europe/Amsterdam" {
		t.Errorf("--tz was ignored: (%s, %q)", loc3, zone3)
	}
}
