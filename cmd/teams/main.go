// Command teams is the CLI entry point. It is deliberately tiny: every flag,
// every command and every exit code lives in internal/cli, so that the file the
// coverage floor excludes stays a wrapper.
package main

import (
	"os"

	// The calendar commands resolve a day in an IANA zone (--tz,
	// /etc/localtime), so the zone database has to travel with the binary: a
	// Windows machine has no zoneinfo directory to read. The import is blank
	// because only its init side effect (registering the embedded copy with
	// time.LoadLocation) is wanted (plans/calendar.md §4.2).
	_ "time/tzdata"

	"github.com/floriscornel/teams-cli/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
