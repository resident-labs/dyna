package database

import (
	"sync/atomic"
	"time"
)

// DurationCounts contains cumulative counts in the <=1 ms, <=10 ms,
// <=100 ms, <=1 s and >1 s buckets.
type DurationCounts [5]uint64

type durationHistogram [5]atomic.Uint64

func (h *durationHistogram) record(d time.Duration) {
	i := 4
	switch {
	case d <= time.Millisecond:
		i = 0
	case d <= 10*time.Millisecond:
		i = 1
	case d <= 100*time.Millisecond:
		i = 2
	case d <= time.Second:
		i = 3
	}
	h[i].Add(1)
}

func (h *durationHistogram) snapshot() (counts DurationCounts) {
	for i := range h {
		counts[i] = h[i].Load()
	}
	return
}

// Metrics reports process-local transaction and scheduled checkpoint measurements.
type Metrics struct {
	WriteHold            DurationCounts `json:"writeHold"`
	Commit               DurationCounts `json:"commit"`
	CheckpointDuration   DurationCounts `json:"checkpointDuration"`
	CheckpointFrames     uint64         `json:"checkpointFrames"`
	CheckpointMoved      uint64         `json:"checkpointMoved"`
	CheckpointIncomplete uint64         `json:"checkpointIncomplete"`
	CheckpointErrors     uint64         `json:"checkpointErrors"`
}

var writeHold, commitDuration, checkpointDuration durationHistogram
var checkpointFrames, checkpointMoved, checkpointIncomplete, checkpointErrors atomic.Uint64

// recordCheckpoint accumulates the results returned by the scheduled PASSIVE call.
func recordCheckpoint(d time.Duration, c Checkpointed, err error) {
	checkpointDuration.record(d)
	if err != nil {
		checkpointErrors.Add(1)
		return
	}
	// SQLite returns -1 for both counts when there is no WAL.
	if c.Frames >= 0 && c.Moved >= 0 {
		checkpointFrames.Add(uint64(c.Frames))
		checkpointMoved.Add(uint64(c.Moved))
		if c.Busy || c.Moved < c.Frames {
			checkpointIncomplete.Add(1)
		}
	}
}

// Measurements returns cumulative metrics since process start. Concurrent updates
// can appear in adjacent snapshots; this is not a transactional snapshot.
func Measurements() Metrics {
	return Metrics{
		WriteHold: writeHold.snapshot(), Commit: commitDuration.snapshot(),
		CheckpointDuration: checkpointDuration.snapshot(),
		CheckpointFrames:   checkpointFrames.Load(), CheckpointMoved: checkpointMoved.Load(),
		CheckpointIncomplete: checkpointIncomplete.Load(), CheckpointErrors: checkpointErrors.Load(),
	}
}
