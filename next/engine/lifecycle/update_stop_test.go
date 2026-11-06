package lifecycle

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func TestExpectedStopOnUnreachableHostIsNotLoss(t *testing.T) {
	rig := newHostRig(t)
	stopped, err := rig.st.StopStale(rig.ctx, []contract.SessionID{"s"})
	if err != nil || len(stopped) != 1 {
		t.Fatalf("update stop = %v, %v", stopped, err)
	}
	rig.view.set("s", false)
	rig.setHost(box.HostUnreachable)
	if b := rig.pass(); b.State != contract.BoxStopped {
		t.Fatalf("expected stop on an unreachable host: %s (%s)", b.State, b.Reason)
	}
}

// A box channel can request its update stop after Pass read want=run. The lifecycle
// write must recheck that intent rather than publish loss from the stale snapshot.
func TestStopIntentRejectsStaleLossObservation(t *testing.T) {
	rig := newHostRig(t)
	old, err := rig.st.Box(rig.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := rig.st.StopStale(rig.ctx, []contract.SessionID{"s"})
	if err != nil || len(stopped) != 1 {
		t.Fatalf("update stop = %v, %v", stopped, err)
	}
	next := old
	next.State, next.Reason = contract.BoxLost, boxDisconnectedReason
	changed, err := rig.r.change(rig.ctx, old, next, time.Time{}, 0, contract.CauseHostLost, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("stale disconnect observation overwrote the planned stop")
	}
	var events int
	if err := rig.st.Read().QueryRowContext(rig.ctx, `SELECT count(*) FROM events WHERE type='box.changed'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("rejected observation published %d box changes", events)
	}
}
