package lifecycle

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

type blockedCreate struct {
	runningBoxes
	entered chan struct{}
}

func (b blockedCreate) Get(ctx context.Context, _ contract.SessionID, _ contract.HostName, _ bool) ([]contract.BoxRepo, error) {
	b.entered <- struct{}{}
	<-ctx.Done()
	return nil, box.RuntimeCreateError{Err: ctx.Err()}
}

func TestCreateDeadlineAndShutdown(t *testing.T) {
	rig := newHostRig(t)
	boxes := blockedCreate{entered: make(chan struct{})}
	rig.r.boxes = boxes
	rig.r.repos = boxes
	triggers := make(chan context.CancelCauseFunc)
	rig.r.createContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
		child, cancel := context.WithCancelCause(ctx)
		triggers <- cancel
		return child, func() { cancel(context.Canceled) }
	}
	setCreating := func() {
		t.Helper()
		if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
			_, err := tx.Exec("UPDATE boxes SET state='creating', want='run' WHERE id='b'")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	run := func(parent context.Context, cause error) {
		t.Helper()
		setCreating()
		rows, err := rig.st.LifecycleBoxes(rig.ctx)
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- rig.r.step(parent, rows[0]) }()
		trigger := <-triggers
		<-boxes.entered
		trigger(cause)
		<-done
	}
	// Cancellation of the parent is not a failed attempt.
	parent, cancel := context.WithCancel(rig.ctx)
	setCreating()
	rows, err := rig.st.LifecycleBoxes(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- rig.r.step(parent, rows[0]) }()
	<-triggers
	<-boxes.entered
	cancel()
	<-done
	rows, err = rig.st.LifecycleBoxes(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Failures != 0 || rows[0].State != contract.BoxCreating {
		t.Fatalf("shutdown counted: %+v", rows[0])
	}
	run(rig.ctx, context.DeadlineExceeded)
	rows, err = rig.st.LifecycleBoxes(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := rows[0]
	if b.State != contract.BoxFailed || b.Failures != 3 || !strings.HasPrefix(b.Reason, string(contract.CauseCreateFailed)) {
		t.Fatalf("deadline: state=%s failures=%d reason=%q", b.State, b.Failures, b.Reason)
	}
	// A deadline that cut a step short says nothing about the host, even when the step failed as a runtime step.
	var hostFailures int
	if err := rig.st.Read().QueryRow("SELECT create_failures FROM hosts WHERE name=?", b.Host).Scan(&hostFailures); err != nil || hostFailures != 0 {
		t.Fatalf("deadline counted against host: %d %v", hostFailures, err)
	}
}

type shareDownCreate struct{ runningBoxes }

func (shareDownCreate) Create(context.Context, BoxSpec) (string, error) {
	return "", box.ShareDown{Host: "h", Reason: "fusermount3 is not setuid", Permanent: true}
}

// A create that finds the host's shared folder down is the host's fault, and one such failure is enough to keep session
// boxes off it, as the host check does for a host that cannot mount it.
func TestCreateWithoutTheSharedFolderCountsAgainstTheHost(t *testing.T) {
	rig := newHostRig(t)
	rig.r.boxes = shareDownCreate{}
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='creating', want='run', image='' WHERE id='b'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := rig.st.LifecycleBoxes(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = rig.r.step(rig.ctx, rows[0])
	var problem string
	if err := rig.st.Read().QueryRow("SELECT create_problem FROM hosts WHERE name=?", rows[0].Host).Scan(&problem); err != nil || problem != string(contract.HostProblemShareUnavailable) {
		t.Fatalf("host problem %q: %v", problem, err)
	}
}

type transientShareDown struct{ runningBoxes }

func (transientShareDown) Create(context.Context, BoxSpec) (string, error) {
	return "", box.ShareDown{Host: "h", Reason: "the mount is down", Permanent: false}
}

// A mount that is down for now is not a reason to keep the host out until the owner clears it.
func TestCreateWithATransientlyDownMountDoesNotQuarantineAtOnce(t *testing.T) {
	rig := newHostRig(t)
	rig.r.boxes = transientShareDown{}
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE boxes SET state='creating', want='run', image='' WHERE id='b'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := rig.st.LifecycleBoxes(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = rig.r.step(rig.ctx, rows[0])
	var problem string
	if err := rig.st.Read().QueryRow("SELECT create_problem FROM hosts WHERE name=?", rows[0].Host).Scan(&problem); err != nil || problem != "" {
		t.Fatalf("host problem %q: %v", problem, err)
	}
}
