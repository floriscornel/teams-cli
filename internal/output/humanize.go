package output

import (
	"fmt"
	"time"
)

// HumanAge renders how long ago t was, at a granularity that is useful at a
// glance. Future timestamps read as "in …", which matters for token expiry.
func HumanAge(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	future := d < 0
	if future {
		d = -d
	}
	out := HumanDuration(d)
	if out == "now" {
		// "now ago" reads badly, and this is exactly what a fresh login sees.
		return "now"
	}
	if future {
		return "in " + out
	}
	return out + " ago"
}

// HumanDuration renders a duration as the largest one or two units.
func HumanDuration(d time.Duration) string {
	switch {
	case d < 10*time.Second:
		return "now"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		hours := int(d.Hours())
		minutes := int(d.Minutes()) - hours*60
		if minutes == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh%dm", hours, minutes)
	default:
		days := int(d.Hours()) / 24
		return fmt.Sprintf("%dd", days)
	}
}

// HumanDurationSeconds renders a duration with a unit, for documented windows
// such as the 15-minute device-code sign-in window.
func HumanDurationSeconds(d time.Duration) string {
	if d <= 0 {
		return "0 minutes"
	}
	if d < time.Minute {
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	}
	minutes := int(d.Minutes())
	if minutes == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}
