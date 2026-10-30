package database

import "time"

// CallerCount is the writer transactions and their lock hold time for one caller.
type CallerCount struct {
	Caller       string        `json:"caller"`
	Transactions uint64        `json:"transactions"`
	Hold         time.Duration `json:"hold"`
}

// DurationCounts contains cumulative counts in the <=1 ms, <=10 ms,
// <=100 ms, <=1 s and >1 s buckets.
type DurationCounts [5]uint64

// Metrics reports process-local transaction and scheduled checkpoint measurements.
type Metrics struct {
	WriteHold            DurationCounts `json:"writeHold"`
	Commit               DurationCounts `json:"commit"`
	CheckpointDuration   DurationCounts `json:"checkpointDuration"`
	CheckpointFrames     uint64         `json:"checkpointFrames"`
	CheckpointMoved      uint64         `json:"checkpointMoved"`
	CheckpointIncomplete uint64         `json:"checkpointIncomplete"`
	CheckpointErrors     uint64         `json:"checkpointErrors"`
	// Callers lists writer transactions by caller. It is empty unless SetCallerDetail is on.
	Callers []CallerCount `json:"callers,omitempty"`
}
