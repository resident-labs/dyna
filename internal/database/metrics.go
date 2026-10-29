package database

import (
	"sync"
	"sync/atomic"
	"time"
)

// maxCallers bounds the named callers kept per process. Writers from any
// further caller count under the name "other".
const maxCallers = 64

// callerDetail turns on per-caller transaction counts. It is off by default,
// because each writer transaction then walks its stack to find the caller.
var callerDetail atomic.Bool

// SetCallerDetail turns per-caller transaction counts on or off.
func SetCallerDetail(on bool) { callerDetail.Store(on) }

// CallerCount is the writer transactions and their lock hold time for one caller.
type CallerCount struct {
	Caller       string        `json:"caller"`
	Transactions uint64        `json:"transactions"`
	Hold         time.Duration `json:"hold"`
}

var callers struct {
	sync.Mutex
	byName map[string]*CallerCount
}

// recordCaller adds one writer transaction, and the time it held the lock, to its caller.
func recordCaller(name string, hold time.Duration) {
	callers.Lock()
	defer callers.Unlock()
	if callers.byName == nil {
		callers.byName = map[string]*CallerCount{}
	}
	c := callers.byName[name]
	if c == nil && len(callers.byName) >= maxCallers {
		name = "other"
		c = callers.byName[name]
	}
	if c == nil {
		c = &CallerCount{Caller: name}
		callers.byName[name] = c
	}
	c.Transactions++
	c.Hold += hold
}

// callerSnapshot copies the caller table in no particular order.
func callerSnapshot() []CallerCount {
	callers.Lock()
	defer callers.Unlock()
	out := make([]CallerCount, 0, len(callers.byName))
	for _, c := range callers.byName {
		out = append(out, *c)
	}
	return out
}

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
	// Callers lists writer transactions by caller. It is empty unless SetCallerDetail is on.
	Callers []CallerCount `json:"callers,omitempty"`
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
		Callers: callerSnapshot(),
	}
}
