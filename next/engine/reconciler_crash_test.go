package engine

import (
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
	"github.com/resident-labs/dyna/engine/store"
)

type crashView struct{ session contract.SessionID }

func (v crashView) Connected(id contract.SessionID) bool { return id != v.session }
func (crashView) OpenTerminal(contract.SessionID) bool   { return true }
func (crashView) SessionClosed(contract.SessionID)       {}
func (crashView) Stale(contract.SessionID) bool          { return false }

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
			if !got.DueAt.IsZero() { // the first crash restarts with no delay
				now = got.DueAt
			}
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

func TestTurnOutcomeResetsCrashCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outcome contract.TurnOutcome
		cause   contract.FailureCause
		want    int
	}{
		{"completed", contract.TurnCompleted, "", 0},
		{"model failed", contract.TurnFailed, contract.CauseModelFailed, 2},
		{"stopped", contract.TurnStopped, "", 2},
		{"harness failed", contract.TurnFailed, contract.CauseHarnessFailed, 2},
		{"set model failed", contract.TurnFailed, contract.CauseSetModelFailed, 2},
		{"setup failed", contract.TurnFailed, contract.CauseSetupFailed, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, sid, ctx := setup(t)
			caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
			channel, problem := testChannel(t, s, ctx, caller)
			if problem != nil {
				t.Fatal(problem)
			}
			start := (<-channel).Body.(contract.TurnStart)
			if err := s.store.Write(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE boxes SET failures=2 WHERE session=?", sid)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if _, problem := s.TurnOutput(ctx, caller, contract.TurnOutputRequest{Turn: start.Turn, Outcome: tc.outcome, Cause: tc.cause}); problem != nil {
				t.Fatal(problem)
			}
			var failures int
			if err := s.store.Read().QueryRowContext(ctx, "SELECT failures FROM boxes WHERE session=?", sid).Scan(&failures); err != nil {
				t.Fatal(err)
			}
			if failures != tc.want {
				t.Fatalf("failures=%d, want %d", failures, tc.want)
			}
		})
	}
}
