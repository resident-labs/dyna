package engine

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type movingHost struct {
	fakeBoxes
	mu        sync.Mutex
	stopped   []contract.BoxID
	oldSlices []string
	recreated []BoxSpec
	failStop  bool
}

func (h *movingHost) Stop(_ context.Context, id contract.BoxID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped = append(h.stopped, id)
	if h.failStop {
		return errors.New("stop failed")
	}
	return nil
}
func (h *movingHost) Recreate(_ context.Context, spec BoxSpec, _, oldSlice string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.oldSlices = append(h.oldSlices, oldSlice)
	h.recreated = append(h.recreated, spec)
	return nil
}

func TestMoveIdleSubtree(t *testing.T) {
	s, room, parent, ctx := setup(t)
	owner := contract.Principal{Kind: contract.PrincipalOwner}
	host := &movingHost{}
	s.boxes = host
	resetLifecycle(s)
	start := func(name string, parent contract.SessionID) contract.SessionStartResponse {
		t.Helper()
		r, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: name, Profile: "test", Goal: "goal"})
		if e != nil {
			t.Fatal(e)
		}
		passLifecycle(t, s, ctx)
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
	passLifecycle(t, s, ctx)
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
	b, err := s.store.Box(ctx, child.Box)
	if err != nil || b.BudgetParent != destination.Box || b.Slice == "" || b.State != contract.BoxStopped {
		t.Fatalf("moved box: %+v %v", b, err)
	}
	oldSlice := b.Slice
	wakeSession(t, s, ctx, room, leaf.Session)
	passLifecycle(t, s, ctx)
	passLifecycle(t, s, ctx)
	if len(host.recreated) != 2 || host.recreated[0].Box != child.Box || host.recreated[1].Box != leaf.Box {
		t.Fatalf("recreate order: %+v", host.recreated)
	}
	wantParent, _ := box.SliceName([]contract.BoxID{destination.Box, child.Box})
	wantLeaf, _ := box.SliceName([]contract.BoxID{destination.Box, child.Box, leaf.Box})
	if len(host.stopped) != 2 || len(host.oldSlices) != 2 || host.oldSlices[0] != oldSlice {
		t.Fatalf("cleanup did not preserve old slice: stops=%v slices=%v", host.stopped, host.oldSlices)
	}
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
	child, e := s.SessionStart(ctx, owner, contract.SessionStartRequest{Room: room, Parent: parent, Name: "child", Profile: "test", Goal: "goal"})
	if e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	if _, e := s.BoxHello(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: child.Session}, contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	s.boxes = &movingHost{failStop: true}
	resetLifecycle(s)
	if _, e := s.SessionMove(ctx, contract.Principal{Kind: contract.PrincipalSession, Session: parent}, contract.SessionMoveRequest{Session: child.Session, NewParent: parent}); e != nil {
		t.Fatal(e)
	}
	passLifecycle(t, s, ctx)
	host := s.boxes.(*movingHost)
	if len(host.stopped) != 1 || stateOf(t, s, ctx, child.Session) != contract.BoxReady {
		t.Fatal("failed stop changed box state")
	}
	host.failStop = false
	passLifecycle(t, s, ctx)
	if len(host.stopped) != 1 {
		t.Fatal("stop retried before its deadline")
	}
	s.now = func() time.Time { return time.Unix(1031, 0) }
	passLifecycle(t, s, ctx)
	if stateOf(t, s, ctx, child.Session) != contract.BoxStopped {
		t.Fatal("box did not stop after retry")
	}
	wakeSession(t, s, ctx, room, child.Session)
	passLifecycle(t, s, ctx)
	if stateOf(t, s, ctx, child.Session) != contract.BoxStarting {
		t.Fatal("box did not wake after retry")
	}
}
