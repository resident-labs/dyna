package notify

import (
	"reflect"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/providerfailure"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func TestQuotaReportReplaysFrozenCandidates(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profiles := &profile.Store{Path: profile.DefaultPath(), Profiles: []profile.Profile{{Name: "other", Disabled: true}}}
	if err := profiles.Save(); err != nil {
		t.Fatal(err)
	}
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &parent)
	if _, err := e.mgr.Store.UpdateTurn(child.ID, 1, func(turn *subagent.Turn) {
		turn.Status = subagent.TurnError
		turn.Error = "quota exhausted"
		turn.FailureReason = providerfailure.Quota
	}); err != nil {
		t.Fatal(err)
	}
	unblock := blockCompletionAcknowledgement(t, e.ctx, e, child.ID)
	first, err := e.svc.CompleteSessionTurn(e.ctx, child.ID, 1)
	if err == nil {
		t.Fatal("expected acknowledgement failure")
	}
	event, ok := first.Message.TurnResult()
	if !ok || len(event.Candidates) != 1 || event.Candidates[0].State != room.CandidateUnknown {
		t.Fatalf("candidate event = %+v", event)
	}
	profiles.Remove("other")
	if err := profiles.Save(); err != nil {
		t.Fatal(err)
	}
	unblock()
	replayed, err := reopenCompletionService(t, e).CompleteSessionTurn(e.ctx, child.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Message, replayed.Message) {
		t.Fatalf("replay changed report")
	}
}
