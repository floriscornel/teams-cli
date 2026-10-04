package cli

import (
	"context"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/floriscornel/teams-cli/internal/output"
)

// newVersionCmd prints the build information, and with --check asks GitHub
// Releases whether a newer version exists (PLAN.md:351).
//
// The check is opt-out (`TEAMS_NO_UPDATE_CHECK` or `update_check = false`) and
// never automatic outside a terminal; `--check` is an explicit request, so it
// reports a disabled check instead of quietly doing nothing.
func (a *App) newVersionCmd() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "version [--check]",
		Short: "Print the CLI version and build information",
		Long: "Print the version, the commit and the build date.\n\n" +
			"With --check, ask GitHub Releases whether a newer `teams` exists. The check is\n" +
			"opt-out (`TEAMS_NO_UPDATE_CHECK=1` or `update_check = false` in the config), and\n" +
			"outside a terminal it only ever runs because --check asked for it.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := versionInfo{
				Version: Version,
				Commit:  Commit,
				Date:    Date,
				Go:      runtime.Version(),
				OS:      runtime.GOOS,
				Arch:    runtime.GOARCH,
			}
			if !check {
				if a.Printer.JSONMode() {
					return a.Printer.JSON(info)
				}
				a.Printer.Printf("teams %s (commit %s, built %s, %s %s/%s)\n",
					info.Version, info.Commit, info.Date, info.Go, info.OS, info.Arch)
				return nil
			}
			return a.reportUpdateCheck(cmd.Context(), info)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "check GitHub Releases for a newer version")
	return cmd
}

// updateView is the documented --json shape of `teams version --check`: the
// build information plus what the check found.
type updateView struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	Date        string `json:"date"`
	Go          string `json:"go"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Latest      string `json:"latest,omitempty"`
	URL         string `json:"url,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Outdated    bool   `json:"outdated"`
	Unversioned bool   `json:"unversioned,omitempty"`
	CheckedAt   string `json:"checkedAt,omitempty"`
}

// reportUpdateCheck runs the check and prints its answer.
func (a *App) reportUpdateCheck(ctx context.Context, info versionInfo) error {
	if !a.updateCheckEnabled() {
		return output.WithHint(output.Usagef("the update check is disabled"),
			"drop TEAMS_NO_UPDATE_CHECK, or set update_check = true in the config")
	}
	checker, err := a.updateChecker()
	if err != nil {
		return err
	}
	a.Printer.Statusf("checking GitHub Releases for a newer version...")
	result, err := checker.CheckNow(ctx)
	if err != nil {
		return output.WithHint(output.Errorf("%v", err),
			"the check needs github.com; unset TEAMS_NO_UPDATE_CHECK to re-enable it, or ignore this offline")
	}
	view := updateView{
		Version: info.Version, Commit: info.Commit, Date: info.Date,
		Go: info.Go, OS: info.OS, Arch: info.Arch,
		Latest: result.Latest, URL: result.URL,
		Outdated: result.Outdated, Unversioned: result.Unversioned,
	}
	if !result.PublishedAt.IsZero() {
		view.PublishedAt = result.PublishedAt.UTC().Format(time.RFC3339)
	}
	if !result.CheckedAt.IsZero() {
		view.CheckedAt = result.CheckedAt.UTC().Format(time.RFC3339)
	}
	if a.Printer.JSONMode() {
		return a.Printer.JSON(view)
	}
	switch {
	case result.Latest == "":
		a.Printer.Successf("no release has been published yet")
	case result.Unversioned:
		a.Printer.Successf("the latest release is %s; this build reports no version, so nothing was compared", result.Latest)
	case result.Outdated:
		a.Printer.Successf("%s is available (this is %s)", result.Latest, result.Current)
		if result.URL != "" {
			a.Printer.Printf("  %s\n", a.Printer.Dim(result.URL))
		}
		a.Printer.Printf("  upgrade with `go install github.com/floriscornel/teams-cli/cmd/teams@%s`,\n", result.Latest)
		a.Printer.Println("  or download the archive from that page")
	default:
		a.Printer.Successf("teams %s is up to date", result.Current)
	}
	return nil
}

type versionInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}
