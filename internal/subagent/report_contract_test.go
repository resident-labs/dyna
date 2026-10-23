package subagent

import (
	"context"
	"testing"
)

func TestTerminalReportOwnershipIsDurable(t *testing.T) {
	s := rollupStore(t)
	const id = "ses_20261019-120000-aaaaaaaa"
	cutoff := int64(42)
	seedRollupSession(t, s, id, "room", "", Turn{Status: TurnRunning, InputCutoff: &cutoff})
	m := &Manager{Store: s}
	if err := m.endTurn(context.Background(), id, TurnEnd{Turn: 1, Status: TurnOK, InputCutoff: &cutoff}); err != nil {
		t.Fatal(err)
	}
	turn, err := s.ReadTurn(id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !turn.ReportPending || turn.InputCutoff == nil || *turn.InputCutoff != cutoff {
		t.Errorf("terminal intent = %+v", turn)
	}
	if _, err := s.SaveReportPlan(context.Background(), id, 1, ReportPlan{RoomID: "room"}); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishReport(context.Background(), id, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveReportPlan(context.Background(), id, 1, ReportPlan{RoomID: "late"}); err == nil {
		t.Error("late report plan accepted")
	}
	turn, err = s.ReadTurn(id, 1)
	if err != nil || !turn.Reported || turn.ReportPending || turn.ReportPlan != nil {
		t.Fatalf("finished report changed: %+v, %v", turn, err)
	}
}
