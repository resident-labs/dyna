package lifecycle

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/resident-labs/dyna/harness/testutil"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestQuiesceRestartJoinsOldStepAndDebtWithoutHoldingPass(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	step := &activeStep{cancel: cancel, want: store.WantRun, done: make(chan struct{})}
	debt := make(chan struct{})
	st, openErr := store.Open(t.TempDir())
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer st.Close()
	if err := st.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO hosts(name,ssh,state,cpus,mem_bytes,disk_bytes,checked_at) VALUES('earlier','','ready',1,1,1,0); INSERT INTO room_leftovers(host,room,box,slice) VALUES('earlier','r','b','')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	earlierDebt := make(chan struct{})
	r := &Reconciler{store: st, active: map[contract.BoxID]*activeStep{"b": step}, roomSteps: map[string]chan struct{}{"host old": debt, "host earlier": earlierDebt}, quiesced: map[contract.BoxID]bool{}, kick: make(chan struct{}, 1)}
	old := box.Box{ID: "b", Host: "old", State: contract.BoxFailed}
	joined := make(chan func(), 1)
	go func() {
		release, claimed, err := r.QuiesceRestart(context.Background(), old)
		if err != nil || !claimed {
			joined <- nil
			return
		}
		joined <- release
	}()
	<-ctx.Done() // ownership is installed before Cancel is delivered to the old step
	if release, claimed, err := r.QuiesceRestart(context.Background(), old); err != nil || claimed || release != nil {
		t.Fatalf("duplicate claim: %v %v", claimed, err)
	}
	pass := make(chan struct{})
	go func() {
		r.pass.Lock()
		//lint:ignore SA2001 Acquiring the lock is the probe: the join must not hold the global pass lock.
		r.pass.Unlock()
		close(pass)
	}()
	select {
	case <-pass:
	case <-time.After(time.Second):
		t.Fatal("join holds the global pass lock")
	}
	select {
	case <-joined:
		t.Fatal("old step not joined")
	default:
	}
	close(step.done)
	select {
	case <-joined:
		t.Fatal("same-ID old-host debt not joined")
	default:
	}
	close(debt)
	select {
	case <-joined:
		t.Fatal("earlier-host same-ID debt was not joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(earlierDebt)
	release := <-joined
	if release == nil {
		t.Fatal("claim failed")
	}
	r.pass.Lock()
	fenced := r.quiesced["b"]
	r.pass.Unlock()
	if !fenced {
		t.Fatal("fence released before deletion/reset")
	}
	release()
	r.pass.Lock()
	fenced = r.quiesced["b"]
	r.pass.Unlock()
	if fenced {
		t.Fatal("fence leaked")
	}
}

func TestQuiesceRestartJoinsCurrentHostDebt(t *testing.T) {
	rig := newHostRig(t)
	debt := make(chan struct{})
	rig.r.roomSteps["host h"] = debt
	old, err := rig.st.Box(rig.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	joined := make(chan func(), 1)
	go func() {
		release, claimed, err := rig.r.QuiesceRestart(rig.ctx, old)
		if err != nil || !claimed {
			joined <- nil
			return
		}
		joined <- release
	}()
	testutil.Eventually(t, time.Second, "restart claim", func() bool {
		rig.r.pass.Lock()
		defer rig.r.pass.Unlock()
		return rig.r.quiesced["b"]
	})
	select {
	case <-joined:
		t.Fatal("current-host debt not joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(debt)
	release := <-joined
	if release == nil {
		t.Fatal("join failed")
	}
	release()
}

func TestQuiesceRestartCancellationRetainsOldWorkerOwnership(t *testing.T) {
	stepCtx, cancelStep := context.WithCancel(context.Background())
	step := &activeStep{cancel: cancelStep, want: store.WantRun, done: make(chan struct{})}
	st, openErr := store.Open(t.TempDir())
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer st.Close()
	r := &Reconciler{store: st, active: map[contract.BoxID]*activeStep{"b": step}, roomSteps: map[string]chan struct{}{}, quiesced: map[contract.BoxID]bool{}, kick: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		release, claimed, err := r.QuiesceRestart(ctx, box.Box{ID: "b"})
		if claimed || release != nil {
			result <- nil
			return
		}
		result <- err
	}()
	<-stepCtx.Done()
	cancel()
	if err := <-result; err == nil {
		t.Fatal("cancelled join succeeded")
	}
	if r.active["b"] != step || r.quiesced["b"] {
		t.Fatal("failed join lost active ownership or leaked fence")
	}
	close(step.done)
}

func TestQuiesceRestartDoesNotCancelOwnedStopOrClose(t *testing.T) {
	for _, want := range []store.BoxWant{store.WantStop, store.WantClose, store.WantDiscard} {
		t.Run(string(want), func(t *testing.T) {
			st, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			step := &activeStep{cancel: cancel, want: want, done: make(chan struct{})}
			r := &Reconciler{store: st, active: map[contract.BoxID]*activeStep{"b": step}, roomSteps: map[string]chan struct{}{}, quiesced: map[contract.BoxID]bool{}, kick: make(chan struct{}, 1)}
			result := make(chan func(), 1)
			go func() {
				release, claimed, err := r.QuiesceRestart(context.Background(), box.Box{ID: "b"})
				if err != nil || !claimed {
					result <- nil
					return
				}
				result <- release
			}()
			testutil.Eventually(t, time.Second, "restart claim", func() bool {
				r.pass.Lock()
				defer r.pass.Unlock()
				return r.quiesced["b"]
			})
			if err := ctx.Err(); err != nil {
				t.Fatalf("restart cancelled owned %s: %v", want, err)
			}
			close(step.done)
			release := <-result
			if release == nil {
				t.Fatal("join failed")
			}
			release()
		})
	}
}
