package clock

import (
	"testing"
	"time"
)

func TestSystemClockTracksWallTime(t *testing.T) {
	c := New()
	before := time.Now()
	got := c.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("Now() = %v, want it between %v and %v", got, before, after)
	}
}

func TestSystemClockIsTheZeroValue(t *testing.T) {
	var c System
	if c.Now().IsZero() {
		t.Error("System.Now() returned the zero time")
	}
}

func TestFakeClockIsDeterministic(t *testing.T) {
	start := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	c := NewFake(start)
	if got := c.Now(); !got.Equal(start) {
		t.Errorf("Now() = %v, want %v", got, start)
	}
	c.Advance(90 * time.Minute)
	if got := c.Now(); !got.Equal(start.Add(90 * time.Minute)) {
		t.Errorf("after Advance = %v", got)
	}
	c.Set(start)
	if got := c.Now(); !got.Equal(start) {
		t.Errorf("after Set = %v", got)
	}
}

func TestFakeClockNormalizesToUTC(t *testing.T) {
	loc := time.FixedZone("JST", 9*60*60)
	c := NewFake(time.Date(2026, 10, 4, 12, 0, 0, 0, loc))
	if got := c.Now(); got.Location() != time.UTC {
		t.Errorf("location = %v, want UTC so relative output is stable", got.Location())
	}
}

func TestFakeClockIsSafeForConcurrentUse(t *testing.T) {
	c := NewFake(time.Unix(0, 0))
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			c.Advance(time.Second)
		}
	}()
	for range 100 {
		_ = c.Now()
	}
	<-done
	if got := c.Now(); !got.Equal(time.Unix(100, 0).UTC()) {
		t.Errorf("Now() = %v, want %v", got, time.Unix(100, 0).UTC())
	}
}
