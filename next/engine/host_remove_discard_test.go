package engine

import (
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// onSpare puts sessions' boxes on a host of their own, "spare", and lifts the room's rule again, so only the boxes hold it.
func (r *poolRig) onSpare(names ...string) (*fakehost.Host, []contract.SessionID) {
	r.t.Helper()
	if err := r.s.store.AddHost(r.ctx, box.Host{Name: "spare", State: box.HostReady, CPUs: 16, MemBytes: 32 * box.GiB, DiskBytes: 100 * box.GiB, CheckedAt: r.s.now()}); err != nil {
		r.t.Fatal(err)
	}
	machine := fakehost.New()
	r.nodes.mu.Lock()
	r.nodes.machines["spare"] = machine
	r.nodes.mu.Unlock()
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room, Only: []string{"spare"}}); e != nil {
		r.t.Fatal(e)
	}
	var sessions []contract.SessionID
	for _, name := range names {
		sessions = append(sessions, r.start(name))
	}
	r.pass(2)
	for _, session := range sessions {
		if got := r.box(session); got.Host != "spare" || got.State != contract.BoxStarting {
			r.t.Fatalf("box %+v", got)
		}
	}
	if _, e := r.s.RoomHosts(r.ctx, r.owner, contract.RoomHostsRequest{Room: r.room}); e != nil {
		r.t.Fatal(e)
	}
	return machine, sessions
}

// removeSpare removes "spare" with its boxes and checks that each box failed as on a host lost for good, and let go of it.
func (r *poolRig) removeSpare(sessions []contract.SessionID) {
	r.t.Helper()
	_, e := r.s.HostRemove(r.ctx, r.owner, contract.HostRemoveRequest{Host: "spare"})
	if e == nil || e.Code != contract.ErrConflict || !strings.Contains(e.Message, "--discard-boxes") {
		r.t.Fatalf("a removal without --discard-boxes: %v", e)
	}
	got, e := r.s.HostRemove(r.ctx, r.owner, contract.HostRemoveRequest{Host: "spare", DiscardBoxes: true})
	if e != nil {
		r.t.Fatal(e)
	}
	slices.Sort(got.Failed)
	if want := slices.Sorted(slices.Values(sessions)); !slices.Equal(got.Failed, want) {
		r.t.Fatalf("failed %v, want %v", got.Failed, want)
	}
	hosts, err := r.s.store.Hosts(r.ctx)
	if err != nil || slices.ContainsFunc(hosts, func(h box.Host) bool { return h.Name == "spare" }) {
		r.t.Fatalf("%+v %v", hosts, err)
	}
	changes := eventsOf[contract.BoxChanged](r.t, r.s, r.ctx, r.room)
	for _, session := range sessions {
		b := r.box(session)
		if b.State != contract.BoxFailed || b.Reason != store.HostRemovedReason || b.Host != "" {
			r.t.Fatalf("box %+v", b)
		}
		if !slices.ContainsFunc(changes, func(c contract.BoxChanged) bool {
			return c.Box == b.ID && c.To == contract.BoxFailed && c.Cause == contract.CauseHostLost && c.Reason == store.HostRemovedReason && c.Host == "spare"
		}) {
			r.t.Fatalf("no host_lost failure for %s in %+v", b.ID, changes)
		}
	}
	// Each parent hears once, however often the notices run.
	for range 2 {
		if err := r.s.FailureNotices(r.ctx); err != nil {
			r.t.Fatal(err)
		}
	}
	for _, session := range sessions {
		told := 0
		for _, notice := range eventsOf[contract.SessionFailed](r.t, r.s, r.ctx, r.room) {
			told += len(slices.DeleteFunc(slices.Clone(notice.Failures), func(f contract.SessionFailure) bool { return f.Session != session }))
		}
		if told != 1 {
			r.t.Fatalf("session %s was told %d times", session, told)
		}
	}
}

// restartElsewhere restarts a session and checks that its new box starts on the other host.
func (r *poolRig) restartElsewhere(session contract.SessionID) {
	r.t.Helper()
	if _, e := r.s.SessionRestart(r.ctx, r.owner, contract.SessionRestartRequest{Session: session}); e != nil {
		r.t.Fatal(e)
	}
	r.pass(2)
	if got := r.box(session); got.Host != "host" || got.State != contract.BoxStarting {
		r.t.Fatalf("box %+v", got)
	}
}

func TestHostRemoveDiscardsStuckPendingBoxes(t *testing.T) {
	r := setupPool(t)
	machine, sessions := r.onSpare("hook-waves", "hooks-lead", "dormant")
	// The host is wiped and gone: the boxes that want to run wait for it, and a dormant one is stopped.
	if err := r.s.store.RecordHostHeartbeat(r.ctx, "spare", false, nil, 0, r.s.now()); err != nil {
		t.Fatal(err)
	}
	dormant := r.box(sessions[2])
	err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.ctx, "UPDATE boxes SET state='pending',reason=?,want='run' WHERE host='spare'", box.PendingNoReadyHost); err != nil {
			return err
		}
		_, err := tx.ExecContext(r.ctx, "UPDATE boxes SET state='stopped',reason='',want='stop' WHERE id=?", dormant.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	r.pass(1)
	r.removeSpare(sessions)
	if len(machine.Containers) != len(sessions) {
		t.Fatalf("a host that does not answer was asked to remove boxes: %v", machine.Calls)
	}
	r.restartElsewhere(sessions[1])
	r.pass(2)
	if got := r.box(sessions[2]); got.State != contract.BoxFailed {
		t.Fatalf("a session not restarted came back: %+v", got)
	}
}

func TestHostRemoveDiscardsAReadyBoxAndItsVolume(t *testing.T) {
	r := setupPool(t)
	machine, sessions := r.onSpare("worker")
	if _, e := r.s.BoxHello(r.ctx, boxCaller(sessions[0]), contract.BoxHelloRequest{}); e != nil {
		t.Fatal(e)
	}
	if got := r.box(sessions[0]); got.State != contract.BoxReady {
		t.Fatalf("box %+v", got)
	}
	r.removeSpare(sessions)
	if len(machine.Containers) != 0 {
		t.Fatalf("the box was left on a host that answers: %+v", machine.Containers)
	}
	r.restartElsewhere(sessions[0])
}

func TestHostRemoveDiscardsBoxesOnAnUnreachableHost(t *testing.T) {
	r := setupPool(t)
	machine, sessions := r.onSpare("worker", "closer")
	for _, session := range sessions {
		if _, e := r.s.BoxHello(r.ctx, boxCaller(session), contract.BoxHelloRequest{}); e != nil {
			t.Fatal(e)
		}
	}
	if err := r.s.store.RecordHostHeartbeat(r.ctx, "spare", false, nil, 0, r.s.now()); err != nil {
		t.Fatal(err)
	}
	r.pass(1)
	for _, session := range sessions {
		if got := r.box(session); got.State != contract.BoxLost {
			t.Fatalf("box %+v", got)
		}
	}
	// A session being closed cannot archive without its host, so it closes with its box discarded.
	closer := sessions[1]
	if _, e := r.s.SessionClose(r.ctx, r.owner, contract.SessionCloseRequest{Session: closer}); e != nil {
		t.Fatal(e)
	}
	open := slices.DeleteFunc(slices.Clone(sessions), func(s contract.SessionID) bool { return s == closer })
	closing := r.box(closer)
	r.removeSpareClosing(open, closing.ID)
	if len(machine.Containers) != len(sessions) {
		t.Fatalf("a host that does not answer was asked to remove boxes: %v", machine.Calls)
	}
	r.pass(3)
	if got := r.closed(closing.ID); got.State != contract.BoxDeleted {
		t.Fatalf("the closing box %+v", got)
	}
	r.restartElsewhere(open[0])
}

// removeSpareClosing is removeSpare with one box being closed as well, which fails with the rest but is not returned.
func (r *poolRig) removeSpareClosing(open []contract.SessionID, closing contract.BoxID) {
	r.t.Helper()
	r.removeSpare(open)
	if got := r.closed(closing); got.Host != "" || (got.State != contract.BoxFailed && got.State != contract.BoxClosing && got.State != contract.BoxDeleted) {
		r.t.Fatalf("the closing box %+v", got)
	}
}
