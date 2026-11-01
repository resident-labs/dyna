package engine

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type inspectHost struct {
	fakeBoxes
	status      box.ContainerStatus
	found       bool
	inspections int
}

func (h *inspectHost) Inspect(context.Context, contract.BoxID) (box.ContainerStatus, bool, error) {
	h.inspections++
	return h.status, h.found, nil
}

type disconnectedView struct{}

func (disconnectedView) Connected(contract.SessionID) bool    { return false }
func (disconnectedView) OpenTerminal(contract.SessionID) bool { return false }
func (disconnectedView) SessionClosed(contract.SessionID)     {}
func (disconnectedView) Stale(contract.SessionID) bool        { return false }

func TestReconcilerRecoversLostHost(t *testing.T) {
	for _, tc := range []struct {
		name           string
		found, running bool
		want           contract.BoxState
	}{
		{"running", true, true, contract.BoxReady},
		{"exited", true, false, contract.BoxStopped},
		{"missing", false, false, contract.BoxFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, sid, ctx := setup(t)
			caller := contract.Principal{Kind: contract.PrincipalSession, Session: sid}
			if _, problem := s.BoxHello(ctx, caller, contract.BoxHelloRequest{}); problem != nil {
				t.Fatal(problem)
			}
			channelCtx, cancel := context.WithCancel(ctx)
			messages, problem := s.BoxChannel(channelCtx, caller, "")
			if problem != nil {
				cancel()
				t.Fatal(problem)
			}
			defer func() {
				cancel()
				for range messages {
				}
			}()
			session, err := s.store.Session(ctx, sid)
			if err != nil {
				t.Fatal(err)
			}
			host := &inspectHost{status: box.ContainerStatus{Running: tc.running}, found: tc.found}
			now := time.Unix(1000, 0)
			r := lifecycle.New(s.store, host, s, nil, s.event, func() time.Time { return now })
			setHost := func(state string) {
				t.Helper()
				if err := s.store.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "UPDATE hosts SET state=? WHERE name='host'", state)
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			pass := func() {
				t.Helper()
				if err := r.Pass(ctx); err != nil {
					t.Fatal(err)
				}
				r.Wait()
			}
			setHost("unreachable")
			pass()
			b, err := s.store.Box(ctx, session.Box)
			if err != nil || b.State != contract.BoxLost || host.inspections != 0 {
				t.Fatalf("unreachable with channel: box=%+v inspections=%d err=%v", b, host.inspections, err)
			}
			setHost("ready")
			pass()
			b, err = s.store.Box(ctx, session.Box)
			if err != nil || b.State != tc.want || host.inspections != 1 {
				t.Fatalf("recovery: box=%+v inspections=%d want=%s err=%v", b, host.inspections, tc.want, err)
			}
			rows, err := s.store.LifecycleBoxes(ctx)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range rows {
				if row.ID == session.Box && row.Failures != 0 {
					t.Fatalf("host loss counted as crash: %+v", row)
				}
			}
		})
	}
}

func TestReconcilerInspectsDisconnectedDeadContainer(t *testing.T) {
	s, _, sid, ctx := setup(t)
	if _, problem := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: sid}, contract.BoxHelloRequest{}); problem != nil {
		t.Fatal(problem)
	}
	session, err := s.store.Session(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	host := &inspectHost{found: true}
	r := lifecycle.New(s.store, host, disconnectedView{}, nil, s.event, func() time.Time { return now })
	pass := func() {
		t.Helper()
		if err := r.Pass(ctx); err != nil {
			t.Fatal(err)
		}
		r.Wait()
	}
	pass()
	now = now.Add(59 * time.Second)
	pass()
	b, err := s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxReady || host.inspections != 0 {
		t.Fatalf("before deadline: box=%+v inspections=%d err=%v", b, host.inspections, err)
	}
	// A short connection between passes restarts the disconnected deadline.
	r.Connected(session.Box)
	pass()
	now = now.Add(time.Second)
	pass()
	b, err = s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxReady || host.inspections != 0 {
		t.Fatalf("after reconnect: box=%+v inspections=%d err=%v", b, host.inspections, err)
	}
	now = now.Add(59 * time.Second)
	pass()
	b, err = s.store.Box(ctx, session.Box)
	if err != nil || b.State != contract.BoxStopped || host.inspections != 1 {
		t.Fatalf("after deadline: box=%+v inspections=%d err=%v", b, host.inspections, err)
	}
}
