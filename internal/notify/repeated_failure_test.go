package notify

import (
	"testing"

	"dyna-agent/internal/subagent"
)

func TestRepeatedTurnFailureReportsOnlyWhenErrorChangesOrSessionRecovers(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	worker := e.start(rec, "worker", nil)
	for i, tc := range []struct {
		status, message string
		wantPost        bool
	}{
		{subagent.TurnError, "GitHub HTTP 503", true},
		{subagent.TurnError, "GitHub HTTP 503", false},
		{subagent.TurnError, "GitHub HTTP 503", false},
		{subagent.TurnError, "GitHub HTTP 504", true},
		{subagent.TurnError, "GitHub HTTP 504", false},
		{subagent.TurnOK, "", true},
		{subagent.TurnError, "GitHub HTTP 504", true},
		{subagent.TurnError, "GitHub HTTP 504", false},
	} {
		n := i + 1
		if n > 1 {
			if _, err := e.svc.Post(e.ctx, PostInput{
				Sender:   Sender{Kind: SenderOwner, Room: rec.Name},
				Mentions: []string{"@" + worker.Address()},
				Text:     "SLEEP:0 RESPOND: next",
			}); err != nil {
				t.Fatal(err)
			}
			worker = e.wait(worker.ID, n)
		}
		if _, err := e.mgr.Store.UpdateTurn(worker.ID, n, func(turn *subagent.Turn) {
			turn.Status, turn.Error = tc.status, tc.message
		}); err != nil {
			t.Fatal(err)
		}
		// A new router must use persisted turn history, not an in-memory cache.
		router := New(e.svc.Rooms, e.mgr)
		result, err := router.CompleteSessionTurn(e.ctx, worker.ID, n)
		if err != nil {
			t.Fatal(err)
		}
		if (result.Message.ID != "") != tc.wantPost {
			t.Errorf("turn %d: post=%v, want %v; error=%q", n, result.Message.ID != "", tc.wantPost, tc.message)
		}
		turn, err := e.mgr.Store.ReadTurn(worker.ID, n)
		if err != nil || !turn.Reported {
			t.Fatalf("turn %d was not claimed: %+v, %v", n, turn, err)
		}
	}
	other := e.start(rec, "other", nil)
	if _, err := e.mgr.Store.UpdateTurn(other.ID, 1, func(turn *subagent.Turn) {
		turn.Status, turn.Error = subagent.TurnError, "GitHub HTTP 504"
	}); err != nil {
		t.Fatal(err)
	}
	if result, err := e.svc.CompleteSessionTurn(e.ctx, other.ID, 1); err != nil || result.Message.ID == "" {
		t.Fatalf("another session's first failure was suppressed: %+v, %v", result, err)
	}
}

func TestSilentOrUnreportedFailureDoesNotSuppressNextAlert(t *testing.T) {
	for _, reason := range []string{"unreported", "wrap-up", "transfer", "consolidation", "close"} {
		t.Run(reason, func(t *testing.T) {
			e := newTestEnv(t, withImmediateTurns)
			rec := e.room()
			worker := e.start(rec, "worker", nil)
			if _, err := e.mgr.Store.UpdateTurn(worker.ID, 1, func(turn *subagent.Turn) {
				turn.Status, turn.Error = subagent.TurnError, "GitHub HTTP 503"
				turn.Reported = reason != "unreported"
				turn.WrapUp = reason == "wrap-up"
				if reason != "wrap-up" && reason != "unreported" {
					turn.StopReason = reason
				}
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := e.svc.Post(e.ctx, PostInput{
				Sender:   Sender{Kind: SenderOwner, Room: rec.Name},
				Mentions: []string{"@" + worker.Address()},
				Text:     "SLEEP:0 RESPOND: next",
			}); err != nil {
				t.Fatal(err)
			}
			e.wait(worker.ID, 2)
			if _, err := e.mgr.Store.UpdateTurn(worker.ID, 2, func(turn *subagent.Turn) {
				turn.Status, turn.Error = subagent.TurnError, "GitHub HTTP 503"
			}); err != nil {
				t.Fatal(err)
			}
			if result, err := e.svc.CompleteSessionTurn(e.ctx, worker.ID, 2); err != nil || result.Message.ID == "" {
				t.Fatalf("silent or unreported turn hid the next failure: %+v, %v", result, err)
			}
		})
	}
}
