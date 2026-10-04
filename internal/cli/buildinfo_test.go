package cli

import (
	"runtime/debug"
	"testing"
)

// The version a user sees is either injected by the linker or recovered from the
// build information; both paths are pinned here, because a released binary that
// reports "dev" is indistinguishable from a source build and leaves
// `version --check` with nothing to compare.

func TestVersionFromBuildInfo(t *testing.T) {
	tests := []struct {
		name        string
		version     string
		commit      string
		date        string
		info        *debug.BuildInfo
		wantVersion string
		wantCommit  string
		wantDate    string
	}{
		{
			name:    "a go install build reports its module version",
			version: versionUnset, commit: commitUnset, date: dateUnset,
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "v1.2.3"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "0123456789abcdef0123456789abcdef01234567"},
					{Key: "vcs.time", Value: "2026-10-04T09:00:00Z"},
				},
			},
			wantVersion: "v1.2.3", wantCommit: "0123456", wantDate: "2026-10-04T09:00:00Z",
		},
		{
			name:    "an injected version always wins",
			version: "v9.9.9", commit: "abc1234", date: "2026-01-01T00:00:00Z",
			info: &debug.BuildInfo{
				Main: debug.Module{Version: "v1.2.3"},
				Settings: []debug.BuildSetting{
					{Key: "vcs.revision", Value: "deadbeefdeadbeef"},
					{Key: "vcs.time", Value: "2026-10-04T09:00:00Z"},
				},
			},
			wantVersion: "v9.9.9", wantCommit: "abc1234", wantDate: "2026-01-01T00:00:00Z",
		},
		{
			name:    "a working-tree build keeps the placeholders",
			version: versionUnset, commit: commitUnset, date: dateUnset,
			info: &debug.BuildInfo{
				Main:     debug.Module{Version: develVersion},
				Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: ""}},
			},
			wantVersion: versionUnset, wantCommit: commitUnset, wantDate: dateUnset,
		},
		{
			name:    "a build with no settings at all is left alone",
			version: versionUnset, commit: commitUnset, date: dateUnset,
			info:        &debug.BuildInfo{},
			wantVersion: versionUnset, wantCommit: commitUnset, wantDate: dateUnset,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			Version, Commit, Date = tc.version, tc.commit, tc.date
			t.Cleanup(func() { Version, Commit, Date = versionUnset, commitUnset, dateUnset })

			versionFromBuildInfo(func() (*debug.BuildInfo, bool) { return tc.info, true })

			if Version != tc.wantVersion || Commit != tc.wantCommit || Date != tc.wantDate {
				t.Errorf("version = %q commit = %q date = %q, want %q/%q/%q",
					Version, Commit, Date, tc.wantVersion, tc.wantCommit, tc.wantDate)
			}
		})
	}
}

func TestVersionFromBuildInfoWithoutInformation(t *testing.T) {
	Version, Commit, Date = versionUnset, commitUnset, dateUnset
	t.Cleanup(func() { Version, Commit, Date = versionUnset, commitUnset, dateUnset })

	versionFromBuildInfo(func() (*debug.BuildInfo, bool) { return nil, false })
	if Version != versionUnset {
		t.Errorf("version = %q, want the placeholder when the build has no information", Version)
	}
}

// TestVersionPlaceholdersMatchRoot guards the coupling buildinfo.go documents:
// the fallback only fills a variable that still holds its placeholder, so the
// literals there have to be the ones root.go initialises the variables with.
func TestVersionPlaceholdersMatchRoot(t *testing.T) {
	// A fresh variable holds the placeholder root.go uses; the test binary gets
	// its own values from init(), so compare the constants against a copy of the
	// documented defaults instead of the live variables.
	if versionUnset != "dev" || commitUnset != "none" || dateUnset != "unknown" {
		t.Fatalf("placeholders = %q/%q/%q, want dev/none/unknown", versionUnset, commitUnset, dateUnset)
	}
}
