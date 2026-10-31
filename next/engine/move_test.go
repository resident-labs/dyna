package engine

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type movingHost struct {
	fakeBoxes
	removed   []contract.BoxID
	recreated []BoxSpec
	failStop  bool
}

func (h *movingHost) Stop(context.Context, contract.BoxID) error {
	if h.failStop {
		return errors.New("stop failed")
	}
	return nil
}
func (h *movingHost) RemoveContainer(_ context.Context, id contract.BoxID) error {
	h.removed = append(h.removed, id)
	return nil
}
func (h *movingHost) Recreate(_ context.Context, spec BoxSpec, _ string) error {
	h.recreated = append(h.recreated, spec)
	return nil
}

func TestMoveIdleSubtree(t *testing.T) {
	s, room, parent, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	host := &movingHost{}
	s.boxes = host
	start := func(name string, parent contract.SessionID) contract.SessionStartResponse {
		t.Helper()
		r, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: name, Profile: "test"})
		if e != nil {
			t.Fatal(e)
		}
		if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: r.Session}, contract.BoxHelloRequest{}); e != nil {
			t.Fatal(e)
		}
		return r
	}
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	child := start("child", parent)
	leaf := start("leaf", child.Session)
	destination := start("destination", "")
	if _, e := s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: child.Session, NewParent: leaf.Session}); e == nil {
		t.Fatal("cycle accepted")
	}
	if err := s.store.AddHost(ctx, box.Host{Name: "other", State: box.HostReady, CPUs: 32, MemBytes: 64 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: s.now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET host='other' WHERE id=?", destination.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: child.Session, NewParent: destination.Session}); e == nil {
		t.Fatal("other host accepted")
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE boxes SET host='host' WHERE id=?", destination.Box)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	event, _ := s.event(room, owner, contract.TurnStarted{})
	turn, _, err := s.store.Dispatch(ctx, leaf.Session, "turn_busy", event, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: child.Session, NewParent: destination.Session}); e == nil || e.Code != contract.ErrMoveBusy || !strings.Contains(e.Message, string(leaf.Session)) {
		t.Fatalf("busy: %v", e)
	}
	if _, e := s.TurnOutput(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: leaf.Session}, contract.TurnOutputRequest{Turn: turn, Outcome: contract.TurnCompleted, ShellJobs: 1}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: child.Session, NewParent: destination.Session}); e == nil || e.Code != contract.ErrMoveBusy || !strings.Contains(e.Message, string(leaf.Session)) {
		t.Fatalf("shell job: %v", e)
	}
	if err := s.store.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE turns SET shell_jobs=0 WHERE id=?", turn)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, e := s.SessionMove(ctx, owner, contract.SessionMoveRequest{Session: child.Session, NewParent: destination.Session}); e != nil {
		t.Fatal(e)
	}
	events, err := s.store.Events(ctx, room, 0, 256, nil)
	if err != nil {
		t.Fatal(err)
	}
	changed := 0
	for _, event := range events {
		if b, ok := event.Body.(contract.BoxChanged); ok && b.To == contract.BoxStopped && (b.Box == child.Box || b.Box == leaf.Box) {
			changed++
		}
	}
	if changed != 2 {
		t.Fatalf("expected two stopped box events, got %d", changed)
	}
	if len(host.removed) != 2 || host.removed[0] != leaf.Box || host.removed[1] != child.Box {
		t.Fatalf("remove order: %v", host.removed)
	}
	b, err := s.store.Box(ctx, child.Box)
	if err != nil || b.BudgetParent != destination.Box || b.Slice != "" || b.State != contract.BoxStopped {
		t.Fatalf("moved box: %+v %v", b, err)
	}
	wakeSession(t, s, ctx, room, leaf.Session)
	s.ReconcileDormancy(ctx, false, true)
	if len(host.recreated) != 2 || host.recreated[0].Box != child.Box || host.recreated[1].Box != leaf.Box {
		t.Fatalf("recreate order: %+v", host.recreated)
	}
	wantParent, _ := box.SliceName([]contract.BoxID{destination.Box, child.Box})
	wantLeaf, _ := box.SliceName([]contract.BoxID{destination.Box, child.Box, leaf.Box})
	if host.recreated[0].Slice != wantParent || host.recreated[1].Slice != wantLeaf {
		t.Fatalf("wrong new slices: %+v", host.recreated)
	}
}

func TestMoveUnderCallerAfterStopFailure(t *testing.T) {
	s, room, parent, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: "child", Profile: "test"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	s.boxes = &movingHost{failStop: true}
	if _, e := s.SessionMove(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.SessionMoveRequest{Session: child.Session, NewParent: parent}); e != nil {
		t.Fatal(e)
	}
}
