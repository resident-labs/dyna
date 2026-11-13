package lifecycle

import (
	"context"
	"database/sql"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/harness/testutil"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

// Gate only the driver's roomPass query, not database/sql's pool mutex. This
// selects the legal schedule where the worker launched earlier in this Pass
// finishes before roomPass snapshots the debt. No lifecycle source is changed.
type cacheRoomPassGate struct {
	context.Context
	entered, release chan struct{}
	once             sync.Once
}

func (c *cacheRoomPassGate) Done() <-chan struct{} {
	stack := make([]byte, 16384)
	n := runtime.Stack(stack, false)
	s := string(stack[:n])
	if strings.Contains(s, "(*Store).RoomsReadyToDelete") && strings.Contains(s, "database/sql.ctxDriverQuery") {
		c.once.Do(func() { close(c.entered); <-c.release })
	}
	return c.Context.Done()
}

type failedCacheCreation struct {
	runningBoxes
	st *store.Store
}

func (f failedCacheCreation) Create(ctx context.Context, spec BoxSpec) (string, error) {
	if err := f.st.AddBoxLeftover(ctx, spec.Host, spec.Room, spec.Box, spec.Slice); err != nil {
		return "", err
	}
	return "", box.RuntimeCreateError{Err: context.DeadlineExceeded}
}

func TestRetainedCacheCannotHideDebtFromSamePassNewCreate(t *testing.T) {
	rig := newHostRig(t)
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		slice, err := box.SliceName("b")
		if err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE boxes SET state='failed',want='run',image='',slice=? WHERE id='b'; INSERT INTO room_leftovers(host,room,box,slice) VALUES('h','r','b',?)`, slice, slice)
		if err != nil {
			return err
		}
		_, err = rig.st.AppendEvent(tx, contract.Event{ID: "independent-pending", Room: "r", Time: rig.now.Add(-time.Minute), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: "b", Session: "s", To: contract.BoxPending}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hosts := &retainedDebtHosts{st: rig.st}
	rig.r.SetRoomHosts(hosts)
	rig.r.SetRoomData(retainedDebtData{})
	if err := rig.r.Pass(rig.ctx); err != nil {
		t.Fatal(err)
	}
	rig.r.Wait()
	if hosts.calls.Load() != 1 {
		t.Fatal("initial container-only cleanup did not run")
	}
	// This stage represents exactly the durable post-confirmation boundary of the
	// actual SessionRestart already exercised by the companion runtime probe.
	old, err := rig.st.Box(rig.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	debts, err := rig.st.BoxLeftovers(rig.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	for _, debt := range debts {
		if err := rig.st.ForgetLeftover(rig.ctx, debt); err != nil {
			t.Fatal(err)
		}
	}
	event := contract.Event{ID: "independent-owner-reset", Room: "r", Time: rig.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{}}
	notice := contract.Event{ID: "independent-reset-notice", Room: "r", Time: rig.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionRestarted{Session: "s"}}
	if ok, err := rig.st.RestartSession(rig.ctx, old, event, notice); err != nil || !ok {
		t.Fatalf("reset: %v %v", ok, err)
	}
	// Restart is not first-create proof and must not grant an automatic retry.
	// Record actual captured-host debt in the new failing worker instead. The
	// same-Pass scheduling and identical-key cache assertions below stay intact.
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE boxes SET failures=2 WHERE id='b'"); return err }); err != nil {
		t.Fatal(err)
	}
	rig.r.boxes = failedCacheCreation{st: rig.st}
	gate := &cacheRoomPassGate{Context: rig.ctx, entered: make(chan struct{}), release: make(chan struct{})}
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate.release) }); rig.r.Wait() })
	passResult := make(chan error, 1)
	go func() { passResult <- rig.r.Pass(gate) }()
	select {
	case <-gate.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("roomPass query did not reach scheduler barrier")
	}
	testutil.Eventually(t, 3*time.Second, "new creation debt before room pass", func() bool {
		current, err := rig.st.BoxLeftovers(rig.ctx, "b")
		if err != nil {
			t.Fatal(err)
		}
		return len(current) == 1
	})
	release.Do(func() { close(gate.release) })
	if err := <-passResult; err != nil {
		t.Fatal(err)
	}
	rig.r.Wait()
	if got := hosts.calls.Load(); got != 2 {
		t.Fatalf("actual first post-reset Pass generated a new attempt and new identical debt but skipped its runnable cleanup: calls=%d, expected=2", got)
	}
}
