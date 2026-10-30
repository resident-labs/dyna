package native

import "time"

// windowStart returns when a window of a fixed length began, so the meter can
// show even pace. It returns "" when the reset is unknown or the length is not fixed.
func windowStart(resetsAt string, length time.Duration) string {
	end, err := time.Parse(time.RFC3339, resetsAt)
	if err != nil || length <= 0 {
		return ""
	}
	return end.Add(-length).UTC().Format(time.RFC3339)
}
