package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExitCodesAndWrapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, CodeOK},
		{Errorf("boom"), CodeError},
		{Usagef("bad flag"), CodeUsage},
		{Authf("sign in first"), CodeAuth},
		{NotFoundf("no such channel"), CodeNotFound},
		{Throttledf("slow down"), CodeThrottled},
		{errors.New("plain"), CodeError},
		{WithCode(errors.New("plain"), CodeNotFound), CodeNotFound},
		{WithHint(Authf("sign in first"), "run teams auth login"), CodeAuth},
	}
	for _, tc := range cases {
		if got := CodeOf(tc.err); got != tc.want {
			t.Errorf("CodeOf(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}

func TestErrorMessagesAndUnwrap(t *testing.T) {
	cause := errors.New("connection refused")
	err := WithHint(WithCode(fmt.Errorf("calling graph: %w", cause), CodeError), "check your network")
	if got := HintOf(err); got != "check your network" {
		t.Errorf("HintOf = %q", got)
	}
	if !errors.Is(err, cause) {
		t.Error("wrapped error is not reachable through errors.Is")
	}
	if got := err.Error(); !strings.Contains(got, "connection refused") {
		t.Errorf("Error() = %q, want the cause in the message", got)
	}
	if HintOf(errors.New("plain")) != "" {
		t.Error("plain error should have no hint")
	}
}

func TestWithHintKeepsTheFirstHint(t *testing.T) {
	err := WithHint(Authf("x"), "first")
	err = WithHint(err, "second")
	if got := HintOf(err); got != "first" {
		t.Errorf("HintOf = %q, want %q", got, "first")
	}
	if CodeOf(err) != CodeAuth {
		t.Errorf("CodeOf = %d, want %d", CodeOf(err), CodeAuth)
	}
}

func TestWithCodePreservesMessage(t *testing.T) {
	var base *Error
	if !errors.As(Errorf("bad thing"), &base) {
		t.Fatal("Errorf did not return a coded error")
	}
	got := WithCode(base, CodeNotFound)
	if got.Error() != "bad thing" {
		t.Errorf("message changed: %q", got.Error())
	}
	if base.Code != CodeError {
		t.Error("WithCode mutated the original error")
	}
}

func TestPrinterStreamsAndLevels(t *testing.T) {
	var out, errBuf bytes.Buffer
	p := New(Options{Out: &out, Err: &errBuf, Quiet: true, Verbose: 0})
	p.Printf("hello %s\n", "world")
	p.Statusf("hidden by quiet")
	p.Debugf("hidden without -v")
	p.Warnf("careful")
	p.Successf("hidden by quiet")
	if got := out.String(); got != "hello world\n" {
		t.Errorf("stdout = %q", got)
	}
	if got := errBuf.String(); !strings.Contains(got, "warning: careful") {
		t.Errorf("stderr = %q", got)
	}
	for _, want := range []string{"hidden by quiet", "hidden without -v", "hidden by quiet"} {
		if strings.Contains(out.String()+errBuf.String(), want) {
			t.Errorf("quiet/verbose filtering failed for %q", want)
		}
	}

	out.Reset()
	errBuf.Reset()
	p = New(Options{Out: &out, Err: &errBuf, Verbose: 2})
	p.Debugf("detail %d", 7)
	p.Statusf("working")
	p.Successf("done")
	if got := errBuf.String(); !strings.Contains(got, "debug: detail 7") || !strings.Contains(got, "working") {
		t.Errorf("stderr = %q", got)
	}
	if got := out.String(); !strings.Contains(got, "done") {
		t.Errorf("stdout = %q", got)
	}
}

func TestReportErrorPrintsHintOnce(t *testing.T) {
	var errBuf bytes.Buffer
	p := New(Options{Out: &bytes.Buffer{}, Err: &errBuf})
	p.ReportError(WithHint(Authf("not signed in"), "run: teams auth login"))
	got := errBuf.String()
	if !strings.Contains(got, "error: not signed in") || !strings.Contains(got, "hint: run: teams auth login") {
		t.Fatalf("stderr = %q", got)
	}
	errBuf.Reset()
	p.ReportError(nil)
	if errBuf.Len() != 0 {
		t.Fatalf("nil error printed %q", errBuf.String())
	}
}

func TestJSONOutputIsStableAndUnstyled(t *testing.T) {
	var out bytes.Buffer
	p := New(Options{Out: &out})
	payload := map[string]any{"name": "Engineering/General", "count": 2}
	if err := p.JSON(payload); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out.Bytes(), &back); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, out.String())
	}
	if !strings.Contains(out.String(), "\n  \"count\"") {
		t.Errorf("expected indented JSON, got %q", out.String())
	}
	if strings.Contains(out.String(), "\x1b[") {
		t.Errorf("JSON output contains ANSI escapes: %q", out.String())
	}
}

func TestTableIsTabSeparatedWhenPiped(t *testing.T) {
	var out bytes.Buffer
	p := New(Options{Out: &out})
	p.Table([]string{"NAME", "ID"}, [][]string{
		{"General", "19:abc"},
		{"Multi\nline", "19\tx"},
		{"short"},
	})
	want := "NAME\tID\nGeneral\t19:abc\nMulti line\t19 x\nshort\t\n"
	if got := out.String(); got != want {
		t.Errorf("table = %q, want %q", got, want)
	}
}

func TestTableIsBorderedWithColour(t *testing.T) {
	t.Setenv("CLICOLOR_FORCE", "1")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	var out bytes.Buffer
	p := New(Options{Out: &out})
	if !p.Color() {
		t.Fatal("CLICOLOR_FORCE did not enable colour")
	}
	p.Table([]string{"NAME", "ID"}, [][]string{{"General", "19:abc"}})
	got := out.String()
	if !strings.Contains(got, "\x1b[") {
		t.Errorf("styled table has no ANSI: %q", got)
	}
	if !strings.Contains(got, "General") {
		t.Errorf("styled table lost the cell: %q", got)
	}
}

func TestColourIsDisabledByEnvironmentAndFlags(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		opts Options
	}{
		{"no-color flag", map[string]string{"CLICOLOR_FORCE": "1"}, Options{NoColor: true}},
		{"json", map[string]string{"CLICOLOR_FORCE": "1"}, Options{JSON: true}},
		{"NO_COLOR", map[string]string{"CLICOLOR_FORCE": "1", "NO_COLOR": "1"}, Options{}},
		{"dumb terminal", map[string]string{"CLICOLOR_FORCE": "1", "TERM": "dumb"}, Options{}},
		{"not a terminal", map[string]string{}, Options{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("CLICOLOR_FORCE", "")
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			p := New(tc.opts)
			if p.Color() {
				t.Errorf("colour enabled for %s", tc.name)
			}
		})
	}
}

func TestDefinitionsAlignsLabels(t *testing.T) {
	var out bytes.Buffer
	p := New(Options{Out: &out})
	p.Definitions([][2]string{
		{"Account", "me@example.com"},
		{"Tenant", "colorkrew.com"},
		{"Empty", ""},
		{"Multi", "line one\nline two"},
	})
	got := out.String()
	if !strings.Contains(got, "Account:  me@example.com") {
		t.Errorf("Account line missing alignment: %q", got)
	}
	if !strings.Contains(got, "Tenant:   colorkrew.com") {
		t.Errorf("Tenant line missing alignment: %q", got)
	}
	if !strings.Contains(got, "Multi:    line one\n          line two") {
		t.Errorf("multi-line value not indented: %q", got)
	}
}

func TestHumanAgeAndDuration(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{now.Add(-5 * time.Second), "now"},
		{now.Add(-30 * time.Second), "30s ago"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-26 * time.Hour), "26h ago"},
		{now.Add(-72 * time.Hour), "3d ago"},
		{now.Add(2 * time.Hour), "in 2h"},
		{time.Time{}, ""},
	}
	for _, tc := range cases {
		if got := HumanAge(now, tc.at); got != tc.want {
			t.Errorf("HumanAge(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
	if got := HumanDuration(90 * time.Minute); got != "1h30m" {
		t.Errorf("HumanDuration(90m) = %q", got)
	}
	if got := HumanDurationSeconds(15 * time.Minute); got != "15 minutes" {
		t.Errorf("HumanDurationSeconds = %q", got)
	}
	if got := HumanDurationSeconds(30 * time.Second); got != "30 seconds" {
		t.Errorf("HumanDurationSeconds = %q", got)
	}
	if got := HumanDurationSeconds(time.Minute); got != "1 minute" {
		t.Errorf("HumanDurationSeconds = %q", got)
	}
	if got := HumanDurationSeconds(0); got != "0 minutes" {
		t.Errorf("HumanDurationSeconds(0) = %q", got)
	}
}

func TestDetectInteractive(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("CI", "")
	if DetectInteractive(&buf, &buf, false) {
		t.Error("buffers are not terminals")
	}
	if DetectInteractive(&buf, &buf, true) {
		t.Error("--no-input must disable interaction")
	}
	t.Setenv("CI", "true")
	if DetectInteractive(&buf, &buf, false) {
		t.Error("CI must disable interaction")
	}
}

func TestJoinNonEmpty(t *testing.T) {
	if got := JoinNonEmpty(" ", "a", "", "  ", "b"); got != "a b" {
		t.Errorf("JoinNonEmpty = %q", got)
	}
}
