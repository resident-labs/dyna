package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// restartedBoxes is a host whose container is stopped, and whose current image is a new one when image says so.
type restartedBoxes struct {
	runningBoxes
	image    string
	startErr error
}

func (b restartedBoxes) Start(context.Context, contract.BoxID) error { return b.startErr }

func (restartedBoxes) Inspect(context.Context, contract.BoxID) (box.ContainerStatus, bool, error) {
	return box.ContainerStatus{}, true, nil
}
func (b restartedBoxes) CurrentImage(context.Context, contract.SessionID, contract.HostName) (box.Image, error) {
	return box.Image{Tag: b.image}, nil
}

// notices reads the restart notices stored for session s, each with whether it wakes.
func (rig *hostRig) notices() (wakes []bool) {
	rig.t.Helper()
	rows, err := rig.st.Read().QueryContext(rig.ctx, `SELECT d.wake FROM deliveries d JOIN events e ON e.seq=d.seq WHERE d.to_id='s' AND e.type='session.restarted' ORDER BY d.seq`)
	if err != nil {
		rig.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var wake bool
		if err := rows.Scan(&wake); err != nil {
			rig.t.Fatal(err)
		}
		wakes = append(wakes, wake)
	}
	return wakes
}

// stop leaves the box of s stopped and meant to run, as after a host reboot, with an open goal if goal is set.
func (rig *hostRig) stop(goal bool, reason string) {
	rig.t.Helper()
	q := `UPDATE boxes SET state='stopped',want='run',reason='` + reason + `' WHERE id='b'`
	if goal {
		q += `;INSERT INTO goals(room,number,session,text,done_when,state,created_at,updated_at) VALUES('r',1,'s','g','d','active',0,0)`
	}
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(q); return err }); err != nil {
		rig.t.Fatal(err)
	}
}

// An idle box that restarts on a new image tells its session, and the notice starts a turn only when the session holds an open
// goal.
func TestImageUpdateRecreateWakesOnlyForAnOpenGoal(t *testing.T) {
	for _, goal := range []bool{true, false} {
		rig := newHostRig(t)
		rig.r.boxes = restartedBoxes{image: "new"}
		rig.stop(goal, "")
		rig.pass()
		wakes := rig.notices()
		if len(wakes) != 1 || wakes[0] != goal {
			t.Errorf("open goal %v: notices wake %v", goal, wakes)
		}
	}
}

// A box that stopped and starts again on the same image, as after a host reboot, tells its session the same way.
func TestRestartAfterStopWakesOnlyForAnOpenGoal(t *testing.T) {
	for _, goal := range []bool{true, false} {
		rig := newHostRig(t)
		rig.r.boxes = restartedBoxes{image: "image"}
		rig.stop(goal, containerStoppedReason)
		rig.pass()
		wakes := rig.notices()
		if len(wakes) != 1 || wakes[0] != goal {
			t.Errorf("open goal %v: notices wake %v", goal, wakes)
		}
	}
}

// A box stopped for being quiet that starts again for a wake is not a restart to tell about: the wake is the news.
func TestStartAfterQuietStopSaysNothing(t *testing.T) {
	rig := newHostRig(t)
	rig.r.boxes = restartedBoxes{image: "image"}
	rig.stop(true, "")
	rig.pass()
	if wakes := rig.notices(); len(wakes) != 0 {
		t.Fatalf("notices %v", wakes)
	}
}

// One box start wakes its session once, however often the same restart is reported.
func TestOneWakePerBoxStart(t *testing.T) {
	rig := newHostRig(t)
	rig.stop(true, "")
	for i := range 2 {
		event, err := rig.r.event("r", contract.Principal{Kind: contract.PrincipalEngine}, contract.SessionRestarted{Session: "s", Stopped: true, ClientID: "box-restart:b:1"})
		if err != nil {
			t.Fatal(err)
		}
		if err := rig.st.NoticeRestarted(rig.ctx, event); err != nil {
			t.Fatalf("report %d: %v", i, err)
		}
	}
	if wakes := rig.notices(); len(wakes) != 1 || !wakes[0] {
		t.Fatalf("notices %v", wakes)
	}
}

// A start that works uses up the reason the box was found stopped, so the reason does not outlast the start into later states or
// tell the session again.
func TestStartClearsTheStoppedReasonAndTellsOnce(t *testing.T) {
	rig := newHostRig(t)
	rig.r.boxes = restartedBoxes{image: "image"}
	rig.stop(true, containerStoppedReason)
	if b := rig.pass(); b.Reason != "" {
		t.Fatalf("reason after the start %q", b.Reason)
	}
	rig.stop(false, "")
	rig.pass()
	if wakes := rig.notices(); len(wakes) != 1 {
		t.Fatalf("notices %v", wakes)
	}
}

// A start that fails again tells nothing and keeps the reason, so the start that works tells once.
func TestFailedStartTellsNothingUntilOneWorks(t *testing.T) {
	rig := newHostRig(t)
	rig.r.boxes = restartedBoxes{image: "image", startErr: errors.New("no")}
	rig.stop(true, containerStoppedReason)
	if b := rig.pass(); b.State != contract.BoxStopped || b.Reason != containerStoppedReason {
		t.Fatalf("after the failed start: %s %q", b.State, b.Reason)
	}
	if wakes := rig.notices(); len(wakes) != 0 {
		t.Fatalf("notices after a failed start %v", wakes)
	}
	rig.r.boxes = restartedBoxes{image: "image"}
	rig.now = rig.now.Add(time.Hour)
	rig.pass()
	if wakes := rig.notices(); len(wakes) != 1 || !wakes[0] {
		t.Fatalf("notices after the start that worked %v", wakes)
	}
}

// The same image update reported twice, as when the box is put back on the old image and recreated again, wakes once.
func TestImageUpdateReportedTwiceWakesOnce(t *testing.T) {
	rig := newHostRig(t)
	rig.r.boxes = restartedBoxes{image: "new"}
	rig.stop(true, "")
	rig.pass()
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE boxes SET state='stopped',image='image',want='run',due_at=0 WHERE id='b'`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rig.pass()
	if wakes := rig.notices(); len(wakes) != 1 || !wakes[0] {
		t.Fatalf("notices %v", wakes)
	}
}

// A stale image found on a box that was stopped tells of the image, and leaves no stopped reason to tell of a later start.
func TestStaleImageRecreateClearsTheStoppedReason(t *testing.T) {
	rig := newHostRig(t)
	rig.r.boxes = restartedBoxes{image: "new"}
	rig.stop(true, containerStoppedReason)
	if b := rig.pass(); b.Reason != "" {
		t.Fatalf("reason %q", b.Reason)
	}
}
