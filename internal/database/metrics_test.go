package database

import (
	"errors"
	"testing"
	"time"
)

func TestScheduledCheckpointMeasurements(t *testing.T) {
	before := Measurements()
	recordCheckpoint(2*time.Millisecond, Checkpointed{Frames: 8, Moved: 3}, nil)
	recordCheckpoint(time.Millisecond, Checkpointed{Frames: -1, Moved: -1}, nil)
	recordCheckpoint(time.Millisecond, Checkpointed{}, errors.New("failed"))
	after := Measurements()
	if after.CheckpointDuration[1]-before.CheckpointDuration[1] != 1 ||
		after.CheckpointDuration[0]-before.CheckpointDuration[0] != 2 ||
		after.CheckpointFrames-before.CheckpointFrames != 8 ||
		after.CheckpointMoved-before.CheckpointMoved != 3 ||
		after.CheckpointIncomplete-before.CheckpointIncomplete != 1 ||
		after.CheckpointErrors-before.CheckpointErrors != 1 {
		t.Fatalf("before: %+v, after: %+v", before, after)
	}
}

func TestDurationHistogram(t *testing.T) {
	var h durationHistogram
	for _, d := range []time.Duration{0, time.Millisecond, time.Millisecond + 1, 10 * time.Millisecond, 100 * time.Millisecond, time.Second, time.Second + 1} {
		h.record(d)
	}
	if got, want := h.snapshot(), (DurationCounts{2, 2, 1, 1, 1}); got != want {
		t.Fatalf("counts = %v, want %v", got, want)
	}
}
