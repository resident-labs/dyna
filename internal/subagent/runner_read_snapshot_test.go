package subagent

import (
	"testing"

	"dyna-agent/internal/profile"
)

func TestRunnerContextReadsDoNotRepairOtherTurns(t *testing.T) {
	for _, read := range []string{"goals", "report target", "posted root report"} {
		t.Run(read, func(t *testing.T) {
			e := newTestEnv(t)
			info := completedFixtureSession(t, e)
			other := completedFixtureSession(t, e)
			if _, err := e.m.Store.UpdateTurn(other.ID, 1, func(turn *Turn) { turn.Status = TurnRunning }); err != nil {
				t.Fatal(err)
			}
			switch read {
			case "goals":
				if frame := e.m.goalFrame(info.Meta, ""); frame == "" {
					t.Fatal("goal frame is empty")
				}
			case "report target":
				meta := info.Meta
				meta.ParentSession = other.ID
				if got := e.m.Store.ReportTarget(meta); got != other.Address() {
					t.Fatalf("report target = %q; want %q", got, other.Address())
				}
			case "posted root report":
				root, err := e.m.Store.UpdateMeta(other.ID, func(meta *Meta) { meta.Role, meta.Name = RoleRoot, RoleRoot })
				if err != nil {
					t.Fatal(err)
				}
				if _, err := e.m.Store.UpdateTurn(info.ID, 1, func(turn *Turn) {
					turn.Harness = profile.HarnessMock
					turn.ReportTo = []string{info.Room}
					turn.PostedTo = []string{root.Address()}
				}); err != nil {
					t.Fatal(err)
				}
				if !e.m.turnAcceptsEmptyOutput(info.ID, 1) {
					t.Fatal("the posted root report was not accepted")
				}
			}
			turn, err := e.m.Store.ReadTurn(other.ID, 1)
			if err != nil || turn.Status != TurnRunning {
				t.Fatalf("context read changed another turn: status %q, error %v", turn.Status, err)
			}
			if read == "posted root report" {
				root, open, err := e.m.RootSession(info.RoomID)
				if err != nil || !open || root.ID != other.ID {
					t.Fatalf("engine root lookup = %+v, open %v, error %v", root, open, err)
				}
				turn, err := e.m.Store.ReadTurn(other.ID, 1)
				if err != nil || turn.Status != TurnError {
					t.Fatalf("engine root lookup did not repair its stale turn: status %q, error %v", turn.Status, err)
				}
			}
		})
	}
}
