package state

import "time"

// Health is the write path's part of the engine's Health call.
type Health struct {
	// Writes finished in the last 5 minutes, with their percentiles.
	Writes      int           `json:"writes"`
	P50         time.Duration `json:"p50Ns"`
	P99         time.Duration `json:"p99Ns"`
	Slowest     time.Duration `json:"slowestNs"`
	SlowestKind string        `json:"slowestKind,omitempty"`
	// InFlight counts this Writer's writes that were called and have not
	// ended. The queue for the writer lock is database.Waiting.
	InFlight int           `json:"inFlight"`
	Oldest   time.Duration `json:"oldestNs"`
	// Degraded is set when the oldest write is older than 5 s.
	Degraded bool `json:"degraded"`
}
