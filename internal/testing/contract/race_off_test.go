//go:build !race

package contract

// raceEnabled reports whether this test binary was built with the race
// detector; see race_on_test.go.
const raceEnabled = false
