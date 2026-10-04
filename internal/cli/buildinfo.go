package cli

import (
	"runtime/debug"
	"strings"
)

// The version variables (root.go) are injected at build time:
//
//	-ldflags "\
//	  -X github.com/floriscornel/teams-cli/internal/cli.Version=… \
//	  -X github.com/floriscornel/teams-cli/internal/cli.Commit=… \
//	  -X github.com/floriscornel/teams-cli/internal/cli.Date=…"
//
// The symbol path is the *package the variables live in*, not `main`. cmd/teams
// is a wrapper that only calls cli.Main, so `-X main.version=…` sets nothing at
// all — silently, because the linker ignores an -X for a symbol that does not
// exist — and every released binary then reports "dev". v1.0.0 shipped that way:
// `teams version` on the released archive printed `teams dev (commit none, built
// unknown)`, which also left `teams version --check` with nothing to compare.
// mise.toml and .goreleaser.yaml spell the path out; the CI smoke job asserts
// that a built binary reports a version, so the mistake cannot ship twice.
//
// A build with no ldflags at all still has something to report: every Go binary
// carries its module information, which is where `go install
// github.com/floriscornel/teams-cli/cmd/teams@v1.2.3` gets its version from
// (PLAN.md "Version info"). That is the fallback below, and it is the difference
// between `go install` users seeing "v1.2.3" and seeing "dev".
func init() {
	versionFromBuildInfo(debug.ReadBuildInfo)
}

// versionFromBuildInfo fills the version variables from the build information
// when the linker did not, so a `go install` build reports its module version
// and its VCS revision instead of the placeholders.
//
// The reader is a parameter so a test can hand in synthetic build information;
// `debug.ReadBuildInfo` is the real one.
func versionFromBuildInfo(read func() (*debug.BuildInfo, bool)) {
	info, ok := read()
	if !ok || info == nil {
		return
	}
	// `(devel)` is what the toolchain reports for a build from a working tree
	// rather than from a module version, and it says nothing a user can compare.
	if Version == versionUnset {
		if v := strings.TrimSpace(info.Main.Version); v != "" && v != develVersion {
			Version = v
		}
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if Commit == commitUnset && setting.Value != "" {
				Commit = shortRevision(setting.Value)
			}
		case "vcs.time":
			if Date == dateUnset && setting.Value != "" {
				Date = setting.Value
			}
		}
	}
}

// The placeholders root.go initialises the version variables with. A build that
// sets none of them reports these, which is why they are worth naming: the
// fallback above only fills a variable that still holds its placeholder, so an
// injected value always wins.
const (
	versionUnset = "dev"
	commitUnset  = "none"
	dateUnset    = "unknown"
	// develVersion is the module version the toolchain reports for a build from
	// a source tree, which is not something to show a user.
	develVersion = "(devel)"
)

// shortRevision shortens a full VCS revision to the seven characters git itself
// abbreviates to.
func shortRevision(revision string) string {
	if len(revision) > 7 {
		return revision[:7]
	}
	return revision
}
