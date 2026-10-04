//go:build race

package contract

// raceEnabled reports whether this test binary was built with the race
// detector. `go test -race` sets the `race` build constraint, so the two files
// with this tag give the tests a compile-time answer.
const raceEnabled = true
