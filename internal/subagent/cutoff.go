package subagent

import (
	"context"
	"fmt"
	"time"
)

// roomSizer measures a room message log's current append offset.
type roomSizer interface {
	FileSize(roomID string) (int64, error)
}

// terminalCutoff measures the room log that bounds the input a terminal turn
// may have read. Every terminal turn writer uses it, so no terminal turn is
// stored without a cutoff. Callers must not write the turn when this fails:
// a cutoff they did not measure would let a later wake retry input the turn
// already read.
func terminalCutoff(rooms roomSizer, roomID string) (*int64, error) {
	size, err := rooms.FileSize(roomID)
	if err != nil {
		return nil, fmt.Errorf("measure the terminal input cutoff: %w", err)
	}
	return &size, nil
}

// cutoffAttempts bounds the runner's retries of a room-log measurement before
// the terminal write takes over.
const cutoffAttempts = 3

// measureCutoff measures the terminal input boundary, retrying a room-log
// error a few times. A failure here no longer drops the turn: the terminal
// write carries every fact and the engine measures the cutoff when it applies
// them, retrying the same request on a transient error.
func (m *Manager) measureCutoff(ctx context.Context, roomID string) (*int64, error) {
	var last error
	delay := 10 * time.Millisecond
	for attempt := 0; attempt < cutoffAttempts; attempt++ {
		cutoff, err := terminalCutoff(m, roomID)
		if err == nil {
			return cutoff, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, last
		case <-time.After(delay):
		}
		delay *= 2
	}
	return nil, last
}
