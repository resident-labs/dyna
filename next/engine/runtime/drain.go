package runtime

import "time"

// waitForDrain waits for the admitted calls or the injected deadline.
func waitForDrain(done <-chan struct{}, deadline <-chan time.Time) {
	select {
	case <-done:
	case <-deadline:
	}
}
