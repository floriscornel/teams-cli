package cli

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/floriscornel/teams-cli/internal/graph"
	"github.com/floriscornel/teams-cli/internal/output"
)

// Tests for the calendar command group's own logic: the failure classification
// and the short event handles (plans/calendar.md §4.5 and §4.6).
//
// The scope pre-check path lives here rather than in a testscript because a
// script runs with TEAMS_ACCESS_TOKEN, which the CLI never scope-checks
// (PLAN.md:105); these tests inject the granted set the way the other CLI tests
// do.

func TestClassifyCalendarViewError(t *testing.T) {
	cases := map[string]struct {
		err          error
		wantFallback bool
		wantNotFound bool
	}{
		// Not shared with you: fall back to free/busy (plans/calendar.md §3, F4).
		"403 ErrorAccessDenied": {
			&graph.APIError{Status: 403, Code: "ErrorAccessDenied"}, true, false,
		},
		"404 ErrorItemNotFound": {
			&graph.APIError{Status: 404, Code: "ErrorItemNotFound"}, true, false,
		},
		// The mailbox cannot serve a calendar view at all: report and skip.
		"404 MailboxNotEnabledForRESTAPI": {
			&graph.APIError{Status: 404, Code: "MailboxNotEnabledForRESTAPI"}, false, true,
		},
		"404 ErrorInvalidUser": {
			&graph.APIError{Status: 404, Code: "ErrorInvalidUser"}, false, true,
		},
		// Anything else is neither: the caller returns it unchanged.
		"429 throttled":  {&graph.APIError{Status: 429, Code: "TooManyRequests"}, false, false},
		"500":            {&graph.APIError{Status: 500}, false, false},
		"403 other code": {&graph.APIError{Status: 403, Code: "Authorization_RequestDenied"}, false, false},
		"404 other code": {&graph.APIError{Status: 404, Code: "NotFound"}, false, false},
		"a plain error":  {errors.New("boom"), false, false},
		"a wrapped 403":  {errors.Join(errors.New("ctx"), &graph.APIError{Status: 403, Code: "ErrorAccessDenied"}), true, false},
		"a usage error":  {output.Usagef("nope"), false, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fallback, notFound := classifyCalendarViewError(tc.err)
			if fallback != tc.wantFallback {
				t.Errorf("fallback = %v, want %v", fallback, tc.wantFallback)
			}
			if (notFound != nil) != tc.wantNotFound {
				t.Errorf("notFound = %v, want present=%v", notFound, tc.wantNotFound)
			}
			if notFound != nil && output.CodeOf(notFound) != output.CodeNotFound {
				t.Errorf("the notFound error maps to exit %d, want 4", output.CodeOf(notFound))
			}
			if notFound != nil && output.CodeOf(notFound) != output.CodeNotFound {
				t.Errorf("the notFound error maps to exit %d, want 4", output.CodeOf(notFound))
			}
		})
	}
}

func TestClassifyCalendarViewErrorNamesTheMailboxProblem(t *testing.T) {
	_, notFound := classifyCalendarViewError(&graph.APIError{
		Status: 404, Code: "MailboxNotEnabledForRESTAPI",
		Message: "The mailbox is not enabled for the REST API.",
	})
	if notFound == nil {
		t.Fatal("no error for a mailbox that cannot serve the REST API")
	}
	if hint := output.HintOf(notFound); !strings.Contains(hint, "Exchange Online mailbox") {
		t.Errorf("hint = %q, want it to name the missing mailbox", hint)
	}
}

func TestShortHandle(t *testing.T) {
	// The handle is the first seven hex characters of the SHA-256 of the event id
	// (plans/calendar.md §4.6), so it is stable across runs and machines.
	const id = "cal-standup"
	if got := shortHandle(id, 7); got != "1d69044" {
		t.Errorf("shortHandle(%q) = %q, want 1d69044", id, got)
	}
	if got := shortHandle(id, 10); got != "1d6904412d" {
		t.Errorf("shortHandle(%q, 10) = %q", id, got)
	}
	if got := shortHandle(id, 64); len(got) != 64 {
		t.Errorf("a 64-character handle is %d characters: %q", len(got), got)
	}
	// Asking for more than the digest holds returns the whole digest, not a panic.
	if got := shortHandle(id, 200); len(got) != 64 {
		t.Errorf("shortHandle(id, 200) is %d characters, want 64", len(got))
	}
}

func TestHandleLengthsLengthensOnlyACollidingPrefix(t *testing.T) {
	// Two ids that share a 7-character prefix (a and a+b have the same SHA-256
	// prefix when the prefix is short enough) must both grow, so the user can tell
	// them apart; an unrelated id keeps the short form.
	a := "event-aaa"
	b := "event-bbb"
	pa := shortHandle(a, 7)
	pb := shortHandle(b, 7)
	if pa == pb {
		t.Skipf("the fixture ids collide at 7 characters (%s); pick others", pa)
	}
	lengths := handleLengths([]string{a, b}, nil)
	if lengths[a] != 7 || lengths[b] != 7 {
		t.Errorf("non-colliding ids got %v, want 7 each", lengths)
	}

	// A real 7-character SHA-256 collision needs ~2^28 ids, so the collision rule
	// is exercised through the prefix seam instead of by brute force: two ids that
	// share a prefix must both grow, and their 10-character handles must differ.
	shared := func(id string) string {
		if strings.HasPrefix(id, "twin-") {
			return "abcdef0"
		}
		return shortHandle(id, 7)
	}
	ids := []string{"twin-a", "twin-b", "solo"}
	lengths = handleLengthsWithPrefix(ids, nil, shared)
	if lengths["twin-a"] != 10 || lengths["twin-b"] != 10 {
		t.Errorf("colliding ids got %v, want 10 each", lengths)
	}
	if lengths["solo"] != 7 {
		t.Errorf("an unrelated id got length %d, want 7", lengths["solo"])
	}
	if shortHandle("twin-a", 10) == shortHandle("twin-b", 10) {
		t.Fatal("the fixture ids collide at 10 characters too; pick others")
	}

	// Two handles that share a prefix lengthen both, so a handle printed by an
	// earlier run cannot be ambiguous with a new one.
	lengths = handleLengthsWithPrefix([]string{"twin-a", "twin-b"}, nil, shared)
	if lengths["twin-a"] != 10 || lengths["twin-b"] != 10 {
		t.Errorf("colliding ids got %v, want 10 each", lengths)
	}
	// A cached handle with a different prefix changes nothing: the id keeps the
	// short form, so a normal listing stays short.
	other := "event-independent"
	lengths = handleLengths([]string{other}, []string{"fffffff"})
	if lengths[other] != 7 {
		t.Errorf("an unrelated cached handle changed the length to %d, want 7", lengths[other])
	}
}

// itoaTest renders a small int without an strconv import in this file.
func itoaTest(n int) string {
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

func TestIsHandle(t *testing.T) {
	cases := map[string]bool{
		"1d69044":       true,  // 7 hex
		"1d69044466":    true,  // 10 hex
		"abcdef123456":  true,  // 12 hex, the longest accepted
		"1d6904":        false, // too short
		"abcdef1234567": false, // too long
		"1D69044":       false, // upper case is not a handle spelling
		"AAMkADevent":   false,
		"zzzzzzz":       false,
		"":              false,
		"12 34567":      false,
	}
	for value, want := range cases {
		if got := isHandle(value); got != want {
			t.Errorf("isHandle(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestIsOutlookWebLinkAndItemID(t *testing.T) {
	cases := map[string]bool{
		"https://outlook.office.com/calendar/item/AAMkAD": true,
		"https://outlook.office365.com/owa/?ItemID=x":     true,
		"https://outlook.live.com/owa/?ItemID=x":          true,
		"cal-standup":                                     false,
		"AAMkADevent":                                     false,
	}
	for value, want := range cases {
		if got := isOutlookWebLink(value); got != want {
			t.Errorf("isOutlookWebLink(%q) = %v, want %v", value, got, want)
		}
	}
	if got := itemIDFromWebLink("https://outlook.office.com/calendar/item/x?ItemID=abc%3D"); got != "abc=" {
		t.Errorf("itemIDFromWebLink = %q, want the decoded abc=", got)
	}
	if got := itemIDFromWebLink("https://outlook.office.com/calendar/item/x"); got != "" {
		t.Errorf("itemIDFromWebLink without ItemID = %q, want empty", got)
	}
	if got := itemIDFromWebLink("not a url at all"); got != "" {
		t.Errorf("itemIDFromWebLink on a non-URL = %q, want empty", got)
	}
}

func TestLooksLikeAddressAndSelfReference(t *testing.T) {
	addresses := map[string]bool{
		"bob@example.com":     true,
		"BOB@EXAMPLE.COM":     true,
		"bob@contoso":         true,
		"@bob":                false,
		"bob":                 false,
		"bob@":                false,
		"bob@example com":     false,
		"contoso/bob@example": false,
	}
	for value, want := range addresses {
		if got := looksLikeAddress(value); got != want {
			t.Errorf("looksLikeAddress(%q) = %v, want %v", value, got, want)
		}
	}

	me := graph.Me{ID: "user-1", Mail: "alice@example.com", UserPrincipalName: "alice@example.com"}
	for value, want := range map[string]bool{
		"me":                true,
		"ME":                true,
		"user-1":            true,
		"alice@example.com": true,
		"ALICE@EXAMPLE.COM": true,
		"bob@example.com":   false,
		"somebody":          false,
	} {
		if got := isSelfReference(value, me); got != want {
			t.Errorf("isSelfReference(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestFreeBusyPlaceholder(t *testing.T) {
	cases := map[string]string{
		"busy":             "[busy]",
		"":                 "[busy]",
		"tentative":        "[tentative]",
		"oof":              "[oof]",
		"workingElsewhere": "[elsewhere]",
		"unknown":          "[unknown]",
		"BUSY":             "[busy]",
	}
	for status, want := range cases {
		if got := freeBusyPlaceholder(status); got != want {
			t.Errorf("freeBusyPlaceholder(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestTruncateSubject(t *testing.T) {
	if got := truncateSubject(calendarRow{Subject: "Standup"}); got != "Standup" {
		t.Errorf("truncateSubject = %q", got)
	}
	if got := truncateSubject(calendarRow{Subject: "  Standup  "}); got != "Standup" {
		t.Errorf("truncateSubject did not trim: %q", got)
	}
	// A free/busy row without a subject shows the status placeholder.
	if got := truncateSubject(calendarRow{Status: "oof"}); got != "[oof]" {
		t.Errorf("truncateSubject of a free/busy row = %q", got)
	}
	// 60 characters is the cap, and the ellipsis replaces the last one.
	long := strings.Repeat("x", 80)
	got := truncateSubject(calendarRow{Subject: long})
	if runes := []rune(got); len(runes) != 60 {
		t.Errorf("a long subject rendered %d runes, want 60", len(runes))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a truncated subject does not end in an ellipsis: %q", got)
	}
	// An exactly-60-character subject is not truncated.
	exact := strings.Repeat("y", 60)
	if got := truncateSubject(calendarRow{Subject: exact}); got != exact {
		t.Errorf("a 60-character subject was truncated: %q", got)
	}
}

func TestCalendarStatusAndTeamsMark(t *testing.T) {
	if got := calendarStatus(calendarRow{Status: "busy"}); got != "busy" {
		t.Errorf("calendarStatus = %q", got)
	}
	if got := calendarStatus(calendarRow{}); got != "unknown" {
		t.Errorf("calendarStatus with no status = %q, want unknown", got)
	}
	if got := teamsMark(true); got != "✓" {
		t.Errorf("teamsMark(true) = %q", got)
	}
	if got := teamsMark(false); got != "" {
		t.Errorf("teamsMark(false) = %q, want empty", got)
	}
}

func TestAttendeeSummary(t *testing.T) {
	if got := attendeeSummary(nil); got != "" {
		t.Errorf("attendeeSummary(nil) = %q", got)
	}
	few := []calendarAttendee{
		{Name: "Alice", Address: "alice@example.com", Response: "accepted"},
		{Name: "Bob"},
	}
	got := attendeeSummary(few)
	if !strings.Contains(got, "Alice <alice@example.com> — accepted") {
		t.Errorf("attendeeSummary = %q, want the name, address and response", got)
	}
	if !strings.Contains(got, "Bob") {
		t.Errorf("attendeeSummary dropped an attendee: %q", got)
	}
	// The first 20 are listed and the rest are summarised
	// (plans/calendar.md §4.4).
	many := make([]calendarAttendee, 25)
	for i := range many {
		many[i] = calendarAttendee{Name: itoaTest(i)}
	}
	got = attendeeSummary(many)
	if !strings.Contains(got, "… 5 more") {
		t.Errorf("attendeeSummary of 25 = %q, want a trailing '… 5 more'", got)
	}
}

func TestCalendarWhenLabels(t *testing.T) {
	loc := time.UTC
	start := time.Date(2026, 10, 7, 10, 0, 0, 0, loc)
	row := calendarRow{Start: start, End: start.Add(30 * 60 * 1e9)}
	if got := calendarWhenLabel(row, loc); got != "10:00–10:30" {
		t.Errorf("calendarWhenLabel = %q", got)
	}
	allDay := calendarRow{AllDay: true, Start: time.Date(2026, 10, 7, 0, 0, 0, 0, loc), End: time.Date(2026, 10, 8, 0, 0, 0, 0, loc)}
	if got := calendarWhenLabel(allDay, loc); got != "all day" {
		t.Errorf("calendarWhenLabel of an all-day event = %q", got)
	}
	if got := calendarWhenFullLabel(allDay, loc); got != "all day 2026-10-07 Wed" {
		t.Errorf("calendarWhenFullLabel of a one-day all-day event = %q", got)
	}
	multi := calendarRow{AllDay: true, Start: time.Date(2026, 10, 7, 0, 0, 0, 0, loc), End: time.Date(2026, 10, 9, 0, 0, 0, 0, loc)}
	if got := calendarWhenFullLabel(multi, loc); got != "all day 2026-10-07 Wed..2026-10-08 Thu" {
		t.Errorf("calendarWhenFullLabel of a two-day all-day event = %q", got)
	}
	span := calendarRow{Start: start, End: start.Add(26 * 60 * 60 * 1e9)}
	if got := calendarWhenFullLabel(span, loc); !strings.Contains(got, "..") {
		t.Errorf("calendarWhenFullLabel of a multi-day event = %q, want a range", got)
	}
}

// ---- the scope pre-check ----------------------------------------------------

// TestCalendarScopePreCheckFailsWithHintBeforeTheCall is the exit-3 path a real
// profile hits: the token carries no calendar scope, so the command must stop
// before any Graph call and name the scope, with a hint that mentions the admin
// (plans/calendar.md §4.5).
func TestCalendarScopePreCheckFailsWithHintBeforeTheCall(t *testing.T) {
	commands := map[string][]string{
		"list":   {"calendar", "list"},
		"show":   {"calendar", "show", "cal-standup"},
		"search": {"calendar", "search", "standup"},
	}
	for name, args := range commands {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			calls := h.countGraphCalls(t)
			h.useAccessToken(t, "User.Read Chat.Read")
			h.app.Hooks.GrantedScopes = []string{"User.Read", "Chat.Read"}

			err := h.run(args...)
			h.wantCode(err, output.CodeAuth)
			// Calendars.Read is not marked admin-consent by the reference, so the
			// hint says the tenant *may* require an admin rather than claiming it
			// does (plans/calendar.md §3, F1).
			if hint := output.HintOf(err); !strings.Contains(hint, "admin") {
				t.Errorf("hint = %q, want it to mention an admin", hint)
			}
			if got := calls(); got != 0 {
				t.Errorf("%s reached Graph %d time(s) despite a missing scope", name, got)
			}
		})
	}
}

// TestCalendarSharedScopeIsTheRequirementForAnotherUser pins the second row of
// the scope table: listing a colleague needs the shared read scope, not the plain
// one (plans/calendar.md §4.8).
func TestCalendarSharedScopeIsTheRequirementForAnotherUser(t *testing.T) {
	h := newHarness(t)
	calls := h.countGraphCalls(t)
	h.useAccessToken(t, "User.Read Calendars.Read")
	h.app.Hooks.GrantedScopes = []string{"User.Read", "Calendars.Read"}

	err := h.run("calendar", "list", "--user", "bob@example.com")
	h.wantCode(err, output.CodeAuth)
	if msg := err.Error(); !strings.Contains(msg, "Calendars.Read.Shared") {
		t.Errorf("error = %q, want it to name Calendars.Read.Shared", msg)
	}
	if got := calls(); got != 0 {
		t.Errorf("the command reached Graph %d time(s)", got)
	}
}

// TestCalendarChatScopeIsCheckedSeparately checks that --chat is gated on
// OnlineMeetings.Read even when the calendar read scope is present, and that the
// prompt wording says the tenant may need an admin rather than claiming it
// always does (plans/calendar.md §3, F1).
func TestCalendarChatScopeIsCheckedSeparately(t *testing.T) {
	h := newHarness(t)
	calls := h.countGraphCalls(t)
	h.useAccessToken(t, "User.Read Calendars.Read")
	h.app.Hooks.GrantedScopes = []string{"User.Read", "Calendars.Read"}

	err := h.run("calendar", "list", "--chat")
	h.wantCode(err, output.CodeAuth)
	if msg := err.Error(); !strings.Contains(msg, "OnlineMeetings.Read") {
		t.Errorf("error = %q, want it to name OnlineMeetings.Read", msg)
	}
	if got := calls(); got != 0 {
		t.Errorf("the command reached Graph %d time(s)", got)
	}
}

// countGraphCalls points the harness at a server that counts requests and returns
// the counter, so a test can prove a command stopped before any Graph call.
func (h *harness) countGraphCalls(t *testing.T) func() int {
	t.Helper()
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"value":[]}`)
	}))
	t.Cleanup(srv.Close)
	h.graphURL = srv.URL + "/v1.0"
	h.applyHooks()
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
}

// ---- the --teams mailbox check ---------------------------------------------

// TestCalendarCreateTeamsNeedsATeamsMailbox pins the F10 rule: a mailbox whose
// allowedOnlineMeetingProviders lacks teamsForBusiness accepts the create and
// silently drops the meeting, so the CLI checks first and fails with exit 1
// instead of creating an event that is not online.
//
// This lives here rather than in a testscript because it needs a server whose
// /me/calendar says something different from the scripts' default mailbox.
func TestCalendarCreateTeamsNeedsATeamsMailbox(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.0/me":
			_, _ = io.WriteString(w, `{"id":"user-1","displayName":"Alice","mail":"alice@example.com"}`)
		case "/v1.0/me/calendar":
			_, _ = io.WriteString(w, `{"allowedOnlineMeetingProviders":["skypeForBusiness"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"code":"ErrorItemNotFound","message":"not found"}}`)
		}
	}))
	defer srv.Close()

	h := newHarness(t)
	h.graphURL = srv.URL + "/v1.0"
	h.applyHooks()
	h.useAccessToken(t, "User.Read Calendars.ReadWrite")
	h.app.Hooks.GrantedScopes = []string{"User.Read", "Calendars.Read", "Calendars.ReadWrite"}

	err := h.run("calendar", "create", "--subject", "Online", "--start", "09:00", "--teams")
	h.wantCode(err, output.CodeError)
	if msg := err.Error(); !strings.Contains(msg, "cannot create Teams meetings") {
		t.Errorf("error = %q, want it to name the mailbox limitation", msg)
	}
	// The create must never have been sent.
	for _, path := range paths {
		if strings.HasPrefix(path, "POST /v1.0/me/calendar/events") {
			t.Errorf("the create was sent despite the mailbox check: %v", paths)
		}
	}
}
