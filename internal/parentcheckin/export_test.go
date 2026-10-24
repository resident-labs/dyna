package parentcheckin

import "time"

// SetNowForTest gives both check-in triggers one fake clock.
func SetNowForTest(now func() time.Time) func() {
	previous := systemNow
	systemNow = now
	return func() { systemNow = previous }
}
