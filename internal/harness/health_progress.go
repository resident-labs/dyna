package harness

import (
	"time"

	harnesshealth "dyna-agent/internal/harness/health"
	"dyna-agent/internal/profile"
)

// Progress writes run off the stdout reader. At most one write per second makes
// unreadable output visible while a turn is running without delaying tools.
func startHealthProgress(p profile.Profile, version string, state *runState) func() {
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		previousRecords := -1
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				state.mu.Lock()
				observation := state.health
				observation.PositiveUsageSeen = state.usage.Total() > 0
				state.mu.Unlock()
				if !observation.StdoutSeen || observation.RecognizedRecords == previousRecords {
					continue
				}
				previousRecords = observation.RecognizedRecords
				harnesshealth.New("").RecordProgress(p, version, observation)
			}
		}
	}()
	return func() { close(stop); <-done }
}
