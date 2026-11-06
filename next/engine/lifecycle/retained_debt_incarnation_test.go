package lifecycle

import (
	"context"
	"database/sql"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
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

type expiredCachePreparation struct{ runningBoxes }

func (expiredCachePreparation) Remove(ctx context.Context, _ contract.BoxID) error {
	return ctx.Err()
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
	// A preparation timeout terminates the first creation directly (failures=3),
	// so FinishCreate can insert same-host debt in the first post-reset Pass.
	rig.r.boxes = expiredCachePreparation{}
	rig.r.createContext = func(ctx context.Context) (context.Context, context.CancelFunc) {
		return context.WithDeadline(ctx, time.Now().Add(-time.Second))
	}
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
	deadline := time.Now().Add(3 * time.Second)
	for {
		current, err := rig.st.BoxLeftovers(rig.ctx, "b")
		if err != nil {
			t.Fatal(err)
		}
		if len(current) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("new create did not generate debt before roomPass")
		}
		time.Sleep(time.Millisecond)
	}
	release.Do(func() { close(gate.release) })
	if err := <-passResult; err != nil {
		t.Fatal(err)
	}
	rig.r.Wait()
	if got := hosts.calls.Load(); got != 2 {
		t.Fatalf("actual first post-reset Pass generated a new attempt and new identical debt but skipped its runnable cleanup: calls=%d, expected=2", got)
	}
}
