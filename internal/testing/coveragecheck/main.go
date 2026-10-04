// Command coveragecheck enforces the repo's coverage floor over the paths that
// count.
//
// It exists instead of a one-liner around `go tool cover -func` because the
// floor has to be statement-weighted and has to skip an explicit list of paths
// (PLAN.md "Coverage scope": only cmd/teams/main.go, internal/testing/... and
// generated code are excluded, and any change to that list needs review).
//
//	go run ./internal/testing/coveragecheck -profile coverage.out -min 80 \
//	    -exclude cmd/teams/main.go -exclude internal/testing
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

type options struct {
	profile  string
	minimum  float64
	excludes []string
	quiet    bool
}

func main() {
	var (
		profile = flag.String("profile", "coverage.out", "coverage profile to read (go test -coverprofile)")
		minimum = flag.Float64("min", 80, "minimum statement coverage percentage")
		exclude = flag.String("exclude", "", "comma or space separated path prefixes to exclude")
		quiet   = flag.Bool("quiet", false, "only print the summary line")
		byPkg   = flag.Bool("by-file", false, "print the per-file breakdown")
	)
	flag.Parse()
	excludes := splitList(*exclude)
	opts := options{profile: *profile, minimum: *minimum, excludes: excludes, quiet: *quiet}
	total, covered, perFile, err := analyze(opts.profile, opts.excludes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "coveragecheck: %v\n", err)
		os.Exit(1)
	}
	if !opts.quiet || *byPkg {
		names := make([]string, 0, len(perFile))
		for name := range perFile {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			stat := perFile[name]
			fmt.Printf("%6.1f%%  %s (%d/%d statements)\n", stat.percent(), name, stat.covered, stat.total)
		}
		fmt.Printf("%6.1f%%  TOTAL (excluded: %s)\n", opts.percent(total, covered), strings.Join(opts.excludes, ", "))
	}
	if total == 0 {
		fmt.Fprintf(os.Stderr, "coveragecheck: the profile has no statements to measure (is %s empty or all-excluded?)\n", opts.profile)
		os.Exit(1)
	}
	if opts.percent(total, covered) < opts.minimum {
		fmt.Fprintf(os.Stderr, "coveragecheck: %.1f%% is below the %.1f%% floor\n", opts.percent(total, covered), opts.minimum)
		os.Exit(1)
	}
}

func (o options) percent(total, covered int) float64 {
	if total == 0 {
		return 0
	}
	return float64(covered) / float64(total) * 100
}

type fileStat struct {
	total   int
	covered int
}

func (f fileStat) percent() float64 {
	if f.total == 0 {
		return 0
	}
	return float64(f.covered) / float64(f.total) * 100
}

// analyze parses a Go coverage profile. Each line is
//
//	<file>:<startLine>.<startCol>,<endLine>.<endCol> <numStatements> <count>
//
// A block counts as covered when count > 0.
func analyze(profile string, excludes []string) (total, covered int, perFile map[string]fileStat, err error) {
	f, err := os.Open(profile)
	if err != nil {
		return 0, 0, nil, err
	}
	defer func() { _ = f.Close() }()

	perFile = map[string]fileStat{}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		file, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		file = moduleRelative(file)
		if excluded(file, excludes) {
			continue
		}
		// rest is "<startLine>.<col>,<endLine>.<col> <numStatements> <count>".
		fields := strings.Fields(rest)
		if len(fields) != 3 {
			continue
		}
		statements, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		count, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		stat := perFile[file]
		stat.total += statements
		if count > 0 {
			stat.covered += statements
			covered += statements
		}
		total += statements
		perFile[file] = stat
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, nil, err
	}
	return total, covered, perFile, nil
}

// modulePrefix is the module path prefix in a coverage profile; the exclusion
// list in mise.toml (`mise run cover`) is written relative to the repository
// root.
const modulePrefix = "github.com/floriscornel/teams-cli/"

func moduleRelative(file string) string {
	normalized := strings.ReplaceAll(file, "\\", "/")
	if rest, ok := strings.CutPrefix(normalized, modulePrefix); ok {
		return rest
	}
	return strings.TrimPrefix(normalized, "./")
}

func excluded(file string, excludes []string) bool {
	normalized := strings.ReplaceAll(file, "\\", "/")
	for _, prefix := range excludes {
		prefix = strings.Trim(strings.ReplaceAll(prefix, "\\", "/"), "/")
		if prefix == "" {
			continue
		}
		if normalized == prefix || strings.HasPrefix(normalized, prefix+"/") {
			return true
		}
	}
	return false
}

func splitList(in string) []string {
	fields := strings.FieldsFunc(in, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if trimmed := strings.TrimSpace(f); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
