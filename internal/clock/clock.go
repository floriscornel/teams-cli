// Package clock injects time into the CLI so that relative output, `--since`
// windows, token-expiry checks and retention pruning are deterministic in tests
// (PLAN.md, Layer 1: "an injected clock keeps relative times and --since
// deterministic").
package clock

import (
	"sync"
	"time"
)

// Clock is the seam every package takes instead of calling time.Now directly.
type Clock interface {
	Now() time.Time
}

// System is the real clock.
type System struct{}

// Now returns the current local time.
func (System) Now() time.Time { return time.Now() }

// New returns the real clock.
func New() Clock { return System{} }

// Fake is a manually advanced clock for tests.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a clock frozen at t (UTC).
func NewFake(t time.Time) *Fake { return &Fake{now: t.UTC()} }

// Now returns the frozen time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set moves the clock to t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t.UTC()
}

// Advance moves the clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
