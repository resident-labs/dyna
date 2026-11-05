package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type stepTimeoutBoxes struct{ runningBoxes }

func (stepTimeoutBoxes) Create(context.Context, BoxSpec) (string, error) {
	return "", box.StepTimeout{Host: "h", Step: contract.StartCreate}
}
func TestTypedHostStepTimeoutCountsCreateFailure(t *testing.T) {
	rig := newHostRig(t)
	rig.r.boxes = stepTimeoutBoxes{}
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='creating',image='',failures=2")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := rig.st.LifecycleBoxes(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := rig.r.step(rig.ctx, rows[0]); err == nil {
		t.Fatal("hung step did not fail")
	}
	var count int
	if err := rig.st.Read().QueryRow("SELECT create_failures FROM hosts WHERE name='h'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("host failures %d: %v", count, err)
	}
	rows, _ = rig.st.LifecycleBoxes(rig.ctx)
	if rows[0].State != contract.BoxFailed || rows[0].Reason != (box.StepTimeout{Host: "h", Step: contract.StartCreate}).Error() {
		t.Fatalf("typed failure not visible: %+v", rows[0])
	}
}

type stoppedStartBoxes struct {
	runningBoxes
	entered chan struct{}
	release chan struct{}
}

func (b stoppedStartBoxes) Create(ctx context.Context, _ BoxSpec) (string, error) {
	close(b.entered)
	<-ctx.Done()
	<-b.release
	return "late-image", nil
}
func TestStopCreatingJoinsAndRejectsLateReady(t *testing.T) {
	rig := newHostRig(t)
	b := stoppedStartBoxes{entered: make(chan struct{}), release: make(chan struct{})}
	rig.r.boxes = b
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='creating',image='',want='run'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := rig.r.Pass(rig.ctx); err != nil {
		t.Fatal(err)
	}
	<-b.entered
	id, accepted, err := rig.st.RequestStartStop(rig.ctx, "s", time.Now())
	if err != nil || !accepted || id != "b" {
		t.Fatalf("stop: %s %v %v", id, accepted, err)
	}
	rig.r.CancelStart(id)
	if err := rig.r.Pass(rig.ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := rig.st.LifecycleBoxes(rig.ctx)
	if rows[0].Want != store.WantStop {
		t.Fatalf("desired state: %+v", rows[0])
	}
	close(b.release)
	rig.r.Wait()
	// An old hello races a stopped start before and after the observed stop.
	old, _ := rig.st.Box(rig.ctx, "b")
	_, err = rig.st.TransitionBox(rig.ctx, old, contract.BoxReady, "", contract.Event{Time: time.Now(), Body: contract.BoxChanged{}})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("late hello accepted: %v", err)
	}
	if err := rig.r.Pass(rig.ctx); err != nil {
		t.Fatal(err)
	}
	rig.r.Wait()
	rows, _ = rig.st.LifecycleBoxes(rig.ctx)
	if rows[0].State != contract.BoxStopped || rows[0].Failures != 0 {
		t.Fatalf("stopped start: %+v", rows[0])
	}
	var count int
	_ = rig.st.Read().QueryRow("SELECT create_failures FROM hosts WHERE name='h'").Scan(&count)
	if count != 0 {
		t.Fatalf("owner stop counted %d failures", count)
	}
	if _, accepted, err := rig.st.RequestStartStop(rig.ctx, "s", time.Now()); err != nil || !accepted {
		t.Fatalf("repeat stop: %v %v", accepted, err)
	}
}

func TestDegradedAdmissionKeepsConnectedBoxes(t *testing.T) {
	rig := newHostRig(t)
	rig.r.SetHostHealth(func(contract.HostName) contract.HostHealth { return contract.HostDegraded })
	if err := rig.r.Pass(rig.ctx); err != nil {
		t.Fatal(err)
	}
	rig.r.Wait()
	row, _ := rig.st.Box(rig.ctx, "b")
	if row.State != contract.BoxReady {
		t.Fatalf("connected box changed: %+v", row)
	}
}
