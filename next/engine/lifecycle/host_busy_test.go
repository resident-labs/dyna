package lifecycle

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// channelView says which sessions' box agents hold their channel.
type channelView struct {
	mu        sync.Mutex
	connected map[contract.SessionID]bool
}

func (v *channelView) set(id contract.SessionID, connected bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.connected[id] = connected
}

func (v *channelView) Connected(id contract.SessionID) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.connected[id]
}
func (*channelView) OpenTerminal(contract.SessionID) bool                       { return false }
func (*channelView) Stale(contract.SessionID) bool                              { return false }
func (*channelView) SessionClosed(contract.SessionID)                           {}
func (*channelView) BoxStopped(context.Context, contract.SessionID)             {}
func (*channelView) CompactBeforeStop(context.Context, contract.SessionID) bool { return false }

// runningBoxes is a host whose containers all run.
type runningBoxes struct{}

func (runningBoxes) MaintainHost(context.Context, contract.HostName) error { return nil }
func (runningBoxes) Inspect(context.Context, contract.BoxID) (box.ContainerStatus, bool, error) {
	return box.ContainerStatus{Running: true}, true, nil
}
func (runningBoxes) TerminalArgv(context.Context, contract.HostName, contract.BoxID) ([]string, error) {
	return nil, nil
}
func (runningBoxes) Create(context.Context, BoxSpec) (string, error) { return "image", nil }
func (runningBoxes) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{}, nil
}
func (runningBoxes) Remove(context.Context, contract.BoxID) error    { return nil }
func (runningBoxes) Start(context.Context, contract.BoxID) error     { return nil }
func (runningBoxes) Stop(context.Context, contract.BoxID) error      { return nil }
func (runningBoxes) Recreate(context.Context, BoxSpec, string) error { return nil }

type hostRig struct {
	t    *testing.T
	ctx  context.Context
	st   *store.Store
	view *channelView
	r    *Reconciler
	now  time.Time
}

// newHostRig holds one ready box, b of session s, on host h, with its agent connected.
func newHostRig(t *testing.T) *hostRig {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	rig := &hostRig{t: t, ctx: ctx, st: st, view: &channelView{connected: map[contract.SessionID]bool{"s": true}}, now: time.Unix(100000, 0)}
	at := rig.now.UnixMilli()
	if err := st.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(fmt.Sprintf(`INSERT INTO rooms(id,name,created_at,cpus,mem_bytes) VALUES('r','r',0,0,0);
INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at,maintained_at) VALUES('h','','ready',64,137438953472,1099511627776,%[1]d,%[1]d);
INSERT INTO sessions(id,room,name,profile,state,goal,created_at) VALUES('s','r','s','p','open','',%[1]d);
INSERT INTO boxes(id,session,host,image,size,state,reason,slice,created_at,updated_at) VALUES('b','s','h','image','agent','ready','','',%[1]d,%[1]d)`, at))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	events := 0
	event := func(room contract.RoomID, from contract.Principal, body contract.EventBody) (contract.Event, error) {
		events++
		return contract.Event{ID: contract.EventID(fmt.Sprintf("ev_%d", events)), Room: room, Time: rig.now, From: from, Body: body}, nil
	}
	rig.r = New(st, runningBoxes{}, rig.view, nil, event, func() time.Time { return rig.now })
	return rig
}

func (rig *hostRig) setHost(state box.HostState) {
	rig.t.Helper()
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE hosts SET state=? WHERE name='h'", state)
		return err
	}); err != nil {
		rig.t.Fatal(err)
	}
}

// pass runs one reconciler pass and the steps it starts, and returns the box.
func (rig *hostRig) pass() box.Box {
	rig.t.Helper()
	if err := rig.r.Pass(rig.ctx); err != nil {
		rig.t.Fatal(err)
	}
	rig.r.Wait()
	b, err := rig.st.Box(rig.ctx, "b")
	if err != nil {
		rig.t.Fatal(err)
	}
	return b
}

// A host too busy to answer its heartbeat is marked unreachable, but a box whose agent still holds its channel is running and
// reaches the engine, so it stays ready for as long as that lasts.
func TestBusyHostKeepsConnectedBoxesReady(t *testing.T) {
	rig := newHostRig(t)
	rig.setHost(box.HostUnreachable)
	for range 10 {
		if b := rig.pass(); b.State != contract.BoxReady {
			t.Fatalf("a connected box on a busy host became %s (%s)", b.State, b.Reason)
		}
		rig.now = rig.now.Add(30 * time.Second)
	}
	rig.setHost(box.HostReady)
	if b := rig.pass(); b.State != contract.BoxReady {
		t.Fatalf("after the host answered: %s (%s)", b.State, b.Reason)
	}
}

// A host that is really gone takes the box's channel with it, and that box is lost at once.
func TestGoneHostStillLosesItsBoxes(t *testing.T) {
	rig := newHostRig(t)
	rig.view.set("s", false)
	rig.setHost(box.HostUnreachable)
	if b := rig.pass(); b.State != contract.BoxLost || b.Reason != "host unreachable" {
		t.Fatalf("box on a gone host: %s (%s)", b.State, b.Reason)
	}
}

// A box kept on a busy host loses its channel later: the host is still unreachable, so there is no 60 s wait.
func TestBusyHostBoxThatDisconnectsIsLost(t *testing.T) {
	rig := newHostRig(t)
	rig.setHost(box.HostUnreachable)
	if b := rig.pass(); b.State != contract.BoxReady {
		t.Fatalf("connected: %s (%s)", b.State, b.Reason)
	}
	rig.view.set("s", false)
	rig.now = rig.now.Add(time.Second)
	if b := rig.pass(); b.State != contract.BoxLost || b.Reason != "host unreachable" {
		t.Fatalf("disconnected: %s (%s)", b.State, b.Reason)
	}
}

// On a reachable host a disconnected box still waits 60 s before it is lost.
func TestReachableHostWaitsForADisconnectedBox(t *testing.T) {
	rig := newHostRig(t)
	rig.view.set("s", false)
	if b := rig.pass(); b.State != contract.BoxReady {
		t.Fatalf("at once: %s (%s)", b.State, b.Reason)
	}
	rig.now = rig.now.Add(60 * time.Second)
	if b := rig.pass(); b.State != contract.BoxLost || b.Reason != boxDisconnectedReason {
		t.Fatalf("after 60 s: %s (%s)", b.State, b.Reason)
	}
}
