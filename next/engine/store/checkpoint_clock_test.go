package store

import (
	"testing"
	"time"
)

func TestCheckpointLoopUsesWallClock(t *testing.T) {
	s, err := Open(t.TempDir(), func() time.Time { panic("checkpoint read the injected clock") })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
