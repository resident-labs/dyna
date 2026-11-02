package contract

import (
	"strings"
	"time"
)

// Duration is a span of time as Go's time.ParseDuration reads one, such as "1h", "15m" or "90s".
type Duration string

// Parse reads d. Empty is zero.
func (d Duration) Parse() (time.Duration, error) {
	if d == "" {
		return 0, nil
	}
	return time.ParseDuration(string(d))
}

// DurationOf writes d without zero units, so an hour is "1h" and 90 seconds is "1m30s". Zero is empty.
func DurationOf(d time.Duration) Duration {
	if d == 0 {
		return ""
	}
	text := d.String()
	if strings.HasSuffix(text, "m0s") {
		text = strings.TrimSuffix(text, "0s")
	}
	if strings.HasSuffix(text, "h0m") {
		text = strings.TrimSuffix(text, "0m")
	}
	return Duration(text)
}
