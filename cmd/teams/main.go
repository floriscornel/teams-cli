// Command teams is the CLI entry point. It is deliberately tiny: every flag,
// every command and every exit code lives in internal/cli, so that the file the
// coverage floor excludes stays a wrapper.
package main

import (
	"os"

	"github.com/floriscornel/teams-cli/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
