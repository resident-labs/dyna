package lockrank

import (
	"strings"
	"sync"
	"testing"
)

func TestInOrderNestingPasses(t *testing.T) {
	release := []func(){Acquire(Names), Acquire(Session), Acquire(Session), Acquire(Gate), Acquire(RoomRoot), Acquire(Room)}
	for i := len(release) - 1; i >= 0; i-- {
		release[i]()
	}
}

func TestSessionAfterRoomPanics(t *testing.T) {
	release := Acquire(Room)
	defer release()
	defer func() {
		if r := recover(); r == nil || !strings.Contains(r.(string), "session lock taken while holding a room lock") {
			t.Fatalf("recover = %v", r)
		}
	}()
	Acquire(Session)
}

// Ranks are per goroutine: a room lock held elsewhere does not order this one.
func TestOtherGoroutinesDoNotCount(t *testing.T) {
	release := Acquire(Room)
	defer release()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		Acquire(Session)()
	}()
	wg.Wait()
}

func TestReleaseForgetsTheLock(t *testing.T) {
	Acquire(Room)()
	Acquire(Session)()
}
