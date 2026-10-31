package service

import (
	"testing"
	"time"

	"dyna-agent/internal/account/store"
)

func TestModelWindowReturnsAfterCachedReset(t *testing.T) {
	reset := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cached := Snapshot{Accounts: []Account{{ObservedAt: reset.Add(-time.Hour), Windows: []Window{
		{ID: "seven_day", Label: "Weekly (7-day)", UsedPercent: pct(45)},
		{ID: store.ModelWindowPrefix + "fable", Label: "Weekly · Fable", UsedPercent: pct(0), ResetsAt: reset.Format(time.RFC3339)},
		{ID: store.ModelWindowPrefix + "opus", Label: "Weekly · Opus", UsedPercent: pct(3)},
	}}}}

	shown := visibleSnapshot(cached).Accounts[0].Windows
	if len(shown) != 2 || shown[0].ID != "seven_day" || shown[1].ID != store.ModelWindowPrefix+"opus" {
		t.Fatalf("visibleSnapshot kept %+v, want the account window and the used Opus window", shown)
	}
	if len(cached.Accounts[0].Windows) != 3 {
		t.Fatalf("visibleSnapshot changed the cached windows to %+v", cached.Accounts[0].Windows)
	}

	later := cached.Accounts[0]
	resetWindows(&later, reset.Add(time.Minute))
	for _, w := range visibleSnapshot(Snapshot{Accounts: []Account{later}}).Accounts[0].Windows {
		if w.ID == store.ModelWindowPrefix+"fable" {
			if w.Reason != store.WindowReasonProbableReset {
				t.Fatalf("after reset the Fable window is %+v, want a probable-reset window", w)
			}
			return
		}
	}
	t.Fatal("after reset the Fable window stayed hidden, want it shown as probable reset")
}
