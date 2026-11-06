package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
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
	if rows[0].State != contract.BoxFailed || rows[0].Host != "h" || rows[0].RetryHost != "" || rows[0].Reason != (box.StepTimeout{Host: "h", Step: contract.StartCreate}).Error() {
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

type failedStartupStopBoxes struct {
	runningBoxes
	calls        atomic.Int32
	beforeRemove func() error
}

func (b *failedStartupStopBoxes) Remove(context.Context, contract.BoxID) error {
	b.calls.Add(1)
	if b.beforeRemove != nil {
		if err := b.beforeRemove(); err != nil {
			return err
		}
	}
	return errors.New("host unavailable")
}
func (b *failedStartupStopBoxes) Stop(context.Context, contract.BoxID) error {
	b.calls.Add(1)
	return errors.New("host unavailable")
}
func TestStartupStopCleanupHonorsPersistedRetryDeadline(t *testing.T) {
	for _, state := range []contract.BoxState{contract.BoxPending, contract.BoxCreating, contract.BoxStarting} {
		for _, image := range []string{"", "saved-image"} {
			name := string(state) + "/no-image"
			if image != "" {
				name = string(state) + "/saved-image"
			}
			t.Run(name, func(t *testing.T) {
				rig := newHostRig(t)
				runtime := &failedStartupStopBoxes{}
				rig.r.boxes = runtime
				if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec("UPDATE boxes SET state=?,image=?,want='stop',due_at=0", state, image)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				rig.pass()
				rows, err := rig.st.LifecycleBoxes(rig.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if !rows[0].DueAt.After(rig.now) || rows[0].Failures != 1 || runtime.calls.Load() != 1 {
					t.Fatalf("failure did not schedule one cleanup backoff: %+v; calls=%d", rows[0], runtime.calls.Load())
				}
				for range 3 {
					rig.pass()
					if after := runtime.calls.Load(); after != 1 {
						t.Fatalf("future due_at=%v ignored at now=%v: host calls 1 -> %d", rows[0].DueAt, rig.now, after)
					}
				}
				rig.now = rows[0].DueAt
				rig.pass()
				if after := runtime.calls.Load(); after != 2 {
					t.Fatalf("due startup stop did not retry exactly once: %d", after)
				}
				rows, err = rig.st.LifecycleBoxes(rig.ctx)
				if err != nil {
					t.Fatal(err)
				}
				if rows[0].Failures != 2 || !rows[0].DueAt.Equal(rig.now.Add(time.Minute)) {
					t.Fatalf("second cleanup did not persist exponential backoff: %+v", rows[0])
				}
			})
		}
	}
}

func TestStartupStopRetryCannotDelayNewIntentOrSchedule(t *testing.T) {
	for _, want := range []store.BoxWant{store.WantRun, store.WantClose, store.WantDiscard, store.WantStop} {
		t.Run(string(want), func(t *testing.T) {
			rig := newHostRig(t)
			if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE boxes SET state='creating',image='',want='stop',due_at=0,failures=0")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var savedDue time.Time
			if want == store.WantStop {
				savedDue = rig.now.Add(5 * time.Minute)
			}
			var dueMS int64
			if !savedDue.IsZero() {
				dueMS = savedDue.UnixMilli()
			}
			runtime := &failedStartupStopBoxes{beforeRemove: func() error {
				return rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE boxes SET want=?,due_at=?", want, dueMS); return err })
			}}
			rig.r.boxes = runtime
			rig.pass()
			rows, err := rig.st.LifecycleBoxes(rig.ctx)
			if err != nil {
				t.Fatal(err)
			}
			row := rows[0]
			if runtime.calls.Load() != 1 || row.Want != want || !row.DueAt.Equal(savedDue) || row.Failures != 0 {
				t.Fatalf("stale cleanup delayed a newer intent or schedule: %+v; calls=%d", row, runtime.calls.Load())
			}
		})
	}
}

type heldStartupStopBoxes struct {
	runningBoxes
	entered chan context.Context
	release chan struct{}
}

func (b heldStartupStopBoxes) Remove(ctx context.Context, _ contract.BoxID) error {
	b.entered <- ctx
	<-b.release
	return errors.New("host unavailable")
}
func (b heldStartupStopBoxes) Stop(ctx context.Context, id contract.BoxID) error {
	return b.Remove(ctx, id)
}
func TestStartupStopCleanupSurvivesUnrelatedPass(t *testing.T) {
	for _, state := range []contract.BoxState{contract.BoxPending, contract.BoxCreating, contract.BoxStarting} {
		for _, image := range []string{"", "saved-image"} {
			t.Run(string(state)+"/"+image, func(t *testing.T) {
				rig := newHostRig(t)
				runtime := heldStartupStopBoxes{entered: make(chan context.Context, 1), release: make(chan struct{})}
				rig.r.boxes = runtime
				defer close(runtime.release)
				if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec("UPDATE boxes SET state=?,image=?,want='stop',due_at=0", state, image)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if err := rig.r.Pass(rig.ctx); err != nil {
					t.Fatal(err)
				}
				var cleanup context.Context
				select {
				case cleanup = <-runtime.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("stop cleanup did not start")
				}
				for range 3 {
					if err := rig.r.Pass(rig.ctx); err != nil {
						t.Fatal(err)
					}
					if err := cleanup.Err(); err != nil {
						t.Fatalf("an unrelated reconciliation pass canceled already-owned stop cleanup: %v", err)
					}
				}
			})
		}
	}
}
