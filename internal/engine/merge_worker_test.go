package engine

import (
	"dyna-agent/internal/roomsvc"
	"testing"
)

func TestMergeWorkerSignalsOnlyEligibilityChanges(t *testing.T) {
	signals := newWorkerSignals()
	signals.wake(roomsvc.Event{Kind: "prs"})
	select {
	case <-signals.channels["merges"]:
		t.Fatal("output-only PR event woke merging")
	default:
	}
	signals.wake(roomsvc.Event{Kind: "prs", WorkerWake: true})
	select {
	case <-signals.channels["merges"]:
	default:
		t.Fatal("eligibility event did not wake merging")
	}
	signals.wake(roomsvc.Event{Kind: "goals", WorkerWake: true})
	for _, queue := range []string{"prs", "merges"} {
		select {
		case <-signals.channels[queue]:
		default:
			t.Fatalf("goal priority change did not wake %s", queue)
		}
	}
	signals.wake(roomsvc.Event{Kind: "room.resumed"})
	select {
	case <-signals.channels["merges"]:
	default:
		t.Fatal("room resume did not wake merging")
	}
}
