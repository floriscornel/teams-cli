package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// Options configures a Printer. The CLI fills it from global flags plus TTY
// detection; tests construct it directly.
type Options struct {
	Out         io.Writer
	Err         io.Writer
	JSON        bool
	NoColor     bool
	Quiet       bool
	Verbose     int
	Interactive bool
}

// Printer writes everything the CLI shows a human, in the one place the linters
// allow formatted output. It never writes to os.Stdout or os.Stderr directly.
type Printer struct {
	out     io.Writer
	err     io.Writer
	json    bool
	color   bool
	quiet   bool
	verbose int
	inter   bool
	styles  styles
}

type styles struct {
	bold   lipgloss.Style
	dim    lipgloss.Style
	red    lipgloss.Style
	green  lipgloss.Style
	yellow lipgloss.Style
}

// New builds a Printer. Colour is on only for a terminal that did not ask for
// plain text: NO_COLOR, TERM=dumb, --no-color, --json and a pipe all disable it.
// CLICOLOR_FORCE=1 keeps colour even when the output is not a terminal, which is
// what the pty-based tests and colour-sensitive scripts use.
func New(opts Options) *Printer {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Err == nil {
		opts.Err = io.Discard
	}
	forced := os.Getenv("CLICOLOR_FORCE") != "" && os.Getenv("CLICOLOR_FORCE") != "0"
	color := (forced || IsTerminal(opts.Out)) &&
		!opts.NoColor && !opts.JSON &&
		os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	return &Printer{
		out:     opts.Out,
		err:     opts.Err,
		json:    opts.JSON,
		color:   color,
		quiet:   opts.Quiet,
		verbose: opts.Verbose,
		inter:   opts.Interactive,
		styles: styles{
			bold:   lipgloss.NewStyle().Bold(true),
			dim:    lipgloss.NewStyle().Faint(true),
			red:    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
			green:  lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
			yellow: lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		},
	}
}

// Out returns the stdout stream, for commands that stream (file download).
func (p *Printer) Out() io.Writer { return p.out }

// Err returns the stderr stream.
func (p *Printer) Err() io.Writer { return p.err }

// JSONMode reports whether --json was requested.
func (p *Printer) JSONMode() bool { return p.json }

// Color reports whether styled output is enabled.
func (p *Printer) Color() bool { return p.color }

// Quiet reports whether --quiet was requested.
func (p *Printer) Quiet() bool { return p.quiet }

// Interactive reports whether the CLI may prompt: a TTY on both ends and no
// --no-input or CI environment. PLAN.md: non-interactive mode never prompts,
// it fails fast with an exit code.
func (p *Printer) Interactive() bool { return p.inter }

// Printf writes formatted output to stdout.
func (p *Printer) Printf(format string, args ...any) {
	// A write to stdout can only fail on a broken pipe, and there is nothing
	// useful to do about it: the command's own error is what matters.
	_, _ = fmt.Fprintf(p.out, format, args...)
}

// Println writes a line to stdout.
func (p *Printer) Println(args ...any) {
	_, _ = fmt.Fprintln(p.out, args...)
}

// Print writes to stdout without a trailing newline.
func (p *Printer) Print(args ...any) {
	_, _ = fmt.Fprint(p.out, args...)
}

// Statusf writes progress to stderr, so stdout stays parseable. It is
// suppressed by --quiet.
func (p *Printer) Statusf(format string, args ...any) {
	if p.quiet {
		return
	}
	_, _ = fmt.Fprintf(p.err, format+"\n", args...)
}

// Debugf writes diagnostics to stderr when -v/--verbose is set.
func (p *Printer) Debugf(format string, args ...any) {
	if p.verbose < 1 {
		return
	}
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(p.err, "%s %s\n", p.dim("debug:"), msg)
}

// Warnf writes a warning to stderr.
func (p *Printer) Warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintf(p.err, "%s %s\n", p.yellow("warning:"), msg)
}

// Errorf writes an error to stderr without exiting; use the returned error to
// carry the exit code.
func (p *Printer) Errorf(format string, args ...any) {
	_, _ = fmt.Fprintf(p.err, "%s %s\n", p.red("error:"), fmt.Sprintf(format, args...))
}

// ReportError prints an error and its hint exactly once, in the shape users and
// scripts expect: `error: …` on stderr, then an indented `hint: …`.
func (p *Printer) ReportError(err error) {
	if err == nil {
		return
	}
	p.Errorf("%s", err.Error())
	if hint := HintOf(err); hint != "" {
		_, _ = fmt.Fprintf(p.err, "hint: %s\n", hint)
	}
}

// JSON encodes v to stdout with a stable two-space indent. It is the only
// output path that is a documented schema, so it never contains styling.
func (p *Printer) JSON(v any) error {
	enc := json.NewEncoder(p.out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return &Error{Code: CodeError, Msg: "encode JSON output", Err: err}
	}
	return nil
}

func (p *Printer) bold(s string) string {
	if !p.color {
		return s
	}
	return p.styles.bold.Render(s)
}

func (p *Printer) dim(s string) string {
	if !p.color {
		return s
	}
	return p.styles.dim.Render(s)
}

func (p *Printer) red(s string) string {
	if !p.color {
		return s
	}
	return p.styles.red.Render(s)
}

func (p *Printer) green(s string) string {
	if !p.color {
		return s
	}
	return p.styles.green.Render(s)
}

func (p *Printer) yellow(s string) string {
	if !p.color {
		return s
	}
	return p.styles.yellow.Render(s)
}

// Successf prints a short confirmation with a green check on a TTY.
func (p *Printer) Successf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.quiet {
		return
	}
	prefix := ""
	if p.color {
		prefix = p.green("✓") + " "
	}
	_, _ = fmt.Fprintln(p.out, prefix+msg)
}

// IsTerminal reports whether w is an interactive terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// DetectInteractive decides whether prompts are allowed: both stdin and stdout
// must be terminals, --no-input must be off and CI must not be set. PLAN.md
// requires the automatic detection; it is our logic, not cobra's or MSAL's, so
// it is tested on its own.
func DetectInteractive(stdin io.Reader, stdout io.Writer, noInput bool) bool {
	if noInput || os.Getenv("CI") != "" {
		return false
	}
	in, ok := stdin.(*os.File)
	if !ok {
		return false
	}
	return IsTerminal(in) && IsTerminal(stdout)
}

// JoinNonEmpty joins the non-empty strings with sep; a small helper that keeps
// renderers from emitting stray separators for missing values.
func JoinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
