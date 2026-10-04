package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// genCompletions writes the shell completion scripts for every shell cobra
// supports, so a release archive can carry them and a user can install the one
// they need (`teams completion bash` prints the same script on demand).
//
// They are generated rather than hand-written, like the man pages and the
// command docs in this tool: the completion of a flag can only be right if it
// comes from the command tree.
func genCompletions(root *cobra.Command, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// The file names are the ones shell users expect: bash uses `_teams`,
	// zsh a `_teams` function file, fish a `teams.fish`, and PowerShell a
	// `teams.ps1` that `Register-ArgumentCompleter` can source.
	scripts := []struct {
		name string
		gen  func(*cobra.Command, *os.File) error
	}{
		{"teams.bash", func(c *cobra.Command, f *os.File) error { return c.GenBashCompletion(f) }},
		{"_teams", func(c *cobra.Command, f *os.File) error { return c.GenZshCompletion(f) }},
		{"teams.fish", func(c *cobra.Command, f *os.File) error { return c.GenFishCompletion(f, true) }},
		{"teams.ps1", func(c *cobra.Command, f *os.File) error { return c.GenPowerShellCompletionWithDesc(f) }},
	}
	for _, script := range scripts {
		path := filepath.Join(dir, script.name)
		file, err := os.Create(path) //nolint:gosec // a generated file under the docs tree
		if err != nil {
			return err
		}
		if err := script.gen(root, file); err != nil {
			_ = file.Close()
			return fmt.Errorf("write %s: %w", path, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("close %s: %w", path, err)
		}
	}
	return nil
}
