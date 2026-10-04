package cli

import (
	"context"

	"github.com/floriscornel/teams-cli/internal/update"
)

// The update check is the only thing the CLI does outside the configured cloud,
// so this file keeps it in one place: `teams version --check` for an explicit
// answer, and a once-a-day notice that only appears on a terminal (PLAN.md:351).
//
// Everything about it is opt-out and failure-tolerant: a disabled check, a
// missing version, an offline machine or a GitHub rate limit must never turn
// into a failed command.

// updateChecker builds the checker for this run.
func (a *App) updateChecker() (*update.Checker, error) {
	paths, err := a.Paths()
	if err != nil {
		return nil, err
	}
	return update.New(update.Options{
		Current:    Version,
		APIBase:    a.Hooks.UpdateBaseURL,
		HTTPClient: a.Hooks.UpdateHTTPClient,
		// One cached answer per profile, like every other piece of state.
		CachePath: paths.UpdateFile(),
		Now:       a.Clock.Now,
		UserAgent: "teams-cli/" + Version,
	}), nil
}

// updateCheckEnabled reports whether the check may run at all.
func (a *App) updateCheckEnabled() bool {
	eff, err := a.Effective()
	if err != nil {
		return false
	}
	return eff.UpdateCheck
}

// noticeNewerVersion prints one line on stderr when a newer release exists.
//
// It runs after a successful command on a terminal only, and the cache keeps it
// to one request a day. A failure is silent: the user asked for the command
// they just ran, not for this.
func (a *App) noticeNewerVersion(ctx context.Context) {
	if !a.Printer.Interactive() || !a.updateCheckEnabled() {
		return
	}
	checker, err := a.updateChecker()
	if err != nil || checker.Unversioned() {
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, update.DefaultTimeout)
	defer cancel()
	result, err := checker.Check(checkCtx)
	if err != nil || !result.Outdated {
		if err != nil {
			a.Printer.Debugf("update check: %v", err)
		}
		return
	}
	a.Printer.Statusf("a newer release is available: %s (this is %s) — run `teams version --check`",
		result.Latest, result.Current)
}
