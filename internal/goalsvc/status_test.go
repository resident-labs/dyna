package goalsvc

import (
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/subagent"
)

// A holder without a session record still has PR facts: its merged PR makes
// the goal ready to close.
func TestGoalFactsKeepsPRFactsForHolderWithoutSessionRecord(t *testing.T) {
	created := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	s := &Service{
		Notify: &notify.Service{Sessions: &subagent.Manager{Store: &subagent.Store{Root: t.TempDir()}}},
		PRState: func() (state.State, error) {
			return state.State{Records: map[string]*state.Record{
				"1": {RoomID: "r1", Author: "ses_20261020-090000-aaaaaaaa", State: "merged", ClosedAt: created.Add(time.Hour)},
			}}, nil
		},
	}
	g := goals.Goal{ID: "g1", State: "active", Holders: []string{"ses_20261020-090000-aaaaaaaa"}, CreatedAt: created}
	f, err := s.goalFacts("r1", g)
	if err != nil {
		t.Fatalf("goalFacts error = %v, want nil for a missing session record", err)
	}
	if got := goals.ComputeStatus(g, nil, f, created.Add(2*time.Hour)).Label; got != goals.StatusReadyToClose {
		t.Fatalf("label = %q, want %q", got, goals.StatusReadyToClose)
	}
}
