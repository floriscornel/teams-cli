// Command docgen regenerates docs/commands (per-command markdown) and docs/man
// (man pages) from the cobra command tree, so the documentation cannot drift
// from the CLI.
//
// It lives under internal/testing because it is developer tooling, not something
// a user runs: `mise run docs` invokes it, and `mise run docs-check` fails when
// the generated files are out of date.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra/doc"

	"github.com/floriscornel/teams-cli/internal/cli"
)

func main() {
	dir := flag.String("dir", "docs", "directory to write docs/commands and docs/man into")
	flag.Parse()

	root := cli.New(os.Stdin, os.Stdout, os.Stderr).Root()
	root.DisableAutoGenTag = true // the timestamp would dirty every regeneration

	commandsDir := filepath.Join(*dir, "commands")
	manDir := filepath.Join(*dir, "man")
	for _, d := range []string{commandsDir, manDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail(err)
		}
	}
	if err := doc.GenMarkdownTree(root, commandsDir); err != nil {
		fail(err)
	}
	header := &doc.GenManHeader{
		Title:   "TEAMS",
		Section: "1",
		Source:  "teams " + cli.Version,
		Manual:  "teams Manual",
	}
	if err := doc.GenManTree(root, header, manDir); err != nil {
		fail(err)
	}
	_, _ = fmt.Fprintf(os.Stdout, "wrote %s and %s\n", commandsDir, manDir)
}

func fail(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "docgen: %v\n", err)
	os.Exit(1)
}
