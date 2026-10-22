package goalkeeper

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// Root and service sessions are outside the goal system. A keeper pass never
// asks them, and their goals from before that rule are dropped once.
func TestKeeperSkipsRootAndServiceAndDropsTheirOldGoals(t *testing.T) {
	e := newCheckInEnv(t)
	// Old data: root and the service session hold spawn goals marked Service.
	if err := e.s.Store.Update(func(st *goals.State) error {
		for _, g := range st.Room(e.rec.ID).Goals {
			g.Service = g.HeldBy(rootID) || g.HeldBy(svcID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	e.s.SessionState = func() ([]subagent.Info, error) { return e.mgr.Store.ListSnapshot(true) }
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	for _, g := range st.Room(e.rec.ID).Goals {
		special := g.HeldBy(rootID) || g.HeldBy(svcID)
		if special == g.Open() {
			t.Errorf("goal %s held by %v: state %s", g.ID, g.Holders, g.State)
		}
	}
	for _, m := range e.posts(room.GoalKeeperSender) {
		if m.To[0] == "work:root" || m.To[0] == "work:svc" {
			t.Errorf("the keeper asked %s", m.To[0])
		}
	}
	// No keeper post names a root or service session.
	for _, m := range e.posts(room.GoalKeeperSender) {
		if strings.Contains(m.Text, "work:root") || strings.Contains(m.Text, "work:svc") {
			t.Errorf("keeper post names %s", m.Text)
		}
	}
}
