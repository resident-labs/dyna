package goalkeeper

import (
	"testing"
	"time"

	"dyna-agent/internal/goals"
)

func TestRoomWorkIsNoticesOnly(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		r    *goals.RoomState
		work bool
		next time.Time
	}{
		{"no notices no work", &goals.RoomState{}, false, time.Time{}},
		{"goals alone schedule nothing", &goals.RoomState{Goals: map[string]*goals.Goal{"g1": {ID: "g1", State: "active"}}}, false, time.Time{}},
		{"notices are work now", &goals.RoomState{Notices: []goals.Notice{{ID: "n1"}}}, true, now.Add(checkInRetry)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work, next := roomWork(tc.r, now)
			if work != tc.work || !next.Equal(tc.next) {
				t.Fatalf("roomWork = %v, %v; want %v, %v", work, next, tc.work, tc.next)
			}
		})
	}
}

func TestNudgeNextStaysEmptyWithoutHolderWork(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		r    *goals.RoomState
	}{
		{"empty room", &goals.RoomState{}},
		{"finished goals only", &goals.RoomState{Goals: map[string]*goals.Goal{"g1": {ID: "g1", State: "done", Holders: []string{"a"}}}}},
		{"unowned goals only", &goals.RoomState{Goals: map[string]*goals.Goal{"g1": {ID: "g1", State: "active"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if at := nudgeNext(tc.r, now); !at.IsZero() {
				t.Fatalf("nudgeNext = %v, want zero", at)
			}
		})
	}
}
