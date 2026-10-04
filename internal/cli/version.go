package cli

import (
	"runtime"

	"github.com/spf13/cobra"
)

// newVersionCmd prints the build information. `--check` (the update check) is
// Phase 5; it is never automatic in non-interactive mode and can be disabled
// with TEAMS_NO_UPDATE_CHECK.
func (a *App) newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version and build information",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			info := versionInfo{
				Version: Version,
				Commit:  Commit,
				Date:    Date,
				Go:      runtime.Version(),
				OS:      runtime.GOOS,
				Arch:    runtime.GOARCH,
			}
			if a.Printer.JSONMode() {
				return a.Printer.JSON(info)
			}
			a.Printer.Printf("teams %s (commit %s, built %s, %s %s/%s)\n",
				info.Version, info.Commit, info.Date, info.Go, info.OS, info.Arch)
			return nil
		},
	}
}

type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}
