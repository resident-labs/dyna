package engine

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type crashView struct{ session contract.SessionID }

func (v crashView) Connected(id contract.SessionID) bool { return id != v.session }
func (crashView) OpenTerminal(contract.SessionID) bool   { return true }

func TestRepeatedExitedBoxesFailAndCompletedTurnResets(t *testing.T) {
	s, room, sid, ctx := setup(t)
	now := time.Unix(1000, 0)
	s.now = func() time.Time { return now }
	caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	host := &inspectHost{found: true}
	r := lifecycle.New(s.store, host, crashView{sid}, nil, s.event, s.now)
	pass := func() {
		t.Helper()
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
	}
	row := func() store.LifecycleBox {
		t.Helper()
		rows, err := s.store.LifecycleBoxes(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, b := range rows {
			if b.ID == session.Box {
				return b
			}
		}
		t.Fatal("box missing")
		return store.LifecycleBox{}
	}
	hello := func() {
		t.Helper()
		if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
			t.Fatal(problem)
		}
	}
	crash := func(want int) {
		t.Helper()
		pass()
		now = now.Add(time.Minute)
		pass()
		got := row()
		state := contract.BoxStopped
		if want == 3 {
			state = contract.BoxFailed
		}
		if got.State != state || got.Failures != want {
			t.Fatalf("crash %d: state=%s failures=%d due=%v", want, got.State, got.Failures, got.DueAt)
		}
		if state == contract.BoxStopped {
			now = got.DueAt
			pass()
			if got = row(); got.State != contract.BoxStarting {
				t.Fatalf("restart: %+v", got)
			}
			hello()
			if got = row(); got.Failures != want {
				t.Fatalf("hello cleared failures: %+v", got)
			}
		}
	}
	hello()
	crash(1)
	crash(2)
	started, err := s.event(room, contract.Principal{Kind: contract.PrincipalEngine}, contract.TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	turn, inputs, _, err := s.store.Dispatch(ctx, sid, "successful", started, false)
	if err != nil || turn == "" || len(inputs) == 0 {
		t.Fatalf("dispatch %q %+v %v", turn, inputs, err)
	}
	if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted}); problem != nil {
		t.Fatal(problem)
	}
	if got := row(); got.Failures != 0 {
		t.Fatalf("completed turn did not reset failures: %+v", got)
	}
	crash(1)
	crash(2)
	crash(3)
}
