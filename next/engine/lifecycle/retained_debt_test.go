package lifecycle

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

type retainedDebtHosts struct {
	st    *store.Store
	calls atomic.Int32
}

func (*retainedDebtHosts) RemoveRoom(context.Context, contract.HostName, contract.RoomID) error {
	return nil
}
func (*retainedDebtHosts) ForgetBox(contract.BoxID) error { return nil }
func (h *retainedDebtHosts) RemoveLeftoverBox(ctx context.Context, _ contract.HostName, id contract.BoxID, _ string) (LeftoverCleanup, error) {
	h.calls.Add(1)
	allowed, err := h.st.CanRemoveBoxVolumes(ctx, id)
	if err != nil {
		return LeftoverDeferred, err
	}
	if allowed {
		return LeftoverComplete, nil
	}
	return LeftoverContainersRemoved, nil
}

type retainedDebtData struct{}

func (retainedDebtData) RemoveRoomData(contract.RoomID) error { return nil }
func (retainedDebtData) RoomDeleted(contract.RoomID)          {}

func TestContainerOnlyDebtStaysKnownWithoutRepeatedHostRPC(t *testing.T) {
	rig := newHostRig(t)
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE boxes SET state='failed',want='run' WHERE id='b'; INSERT INTO room_leftovers(host,room,box,slice) VALUES('h','r','b','')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hosts := &retainedDebtHosts{st: rig.st}
	rig.r.SetRoomHosts(hosts)
	rig.r.SetRoomData(retainedDebtData{})
	passDebt := func() {
		t.Helper()
		rows, err := rig.st.Hosts(rig.ctx)
		if err != nil {
			t.Fatal(err)
		}
		rig.r.pass.Lock()
		err = rig.r.roomPass(rig.ctx, rows)
		rig.r.pass.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		rig.r.Wait()
	}
	for range 5 {
		passDebt()
	}
	if got := hosts.calls.Load(); got != 1 {
		t.Fatalf("container-only debt caused repeated host RPC: %d", got)
	}
	debts, err := rig.st.BoxLeftovers(rig.ctx, "b")
	if err != nil || len(debts) != 1 {
		t.Fatalf("retained volumes lost their host record: %+v %v", debts, err)
	}
	// This is a completion cache, not permission. A newer owner discard causes a
	// real second cleanup and only complete volume deletion retires the record.
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error { _, err := tx.Exec(`UPDATE boxes SET want='discard' WHERE id='b'`); return err }); err != nil {
		t.Fatal(err)
	}
	passDebt()
	if got := hosts.calls.Load(); got != 2 {
		t.Fatalf("owner discard did not invalidate container-only cache: %d", got)
	}
	debts, err = rig.st.BoxLeftovers(rig.ctx, "b")
	if err != nil || len(debts) != 0 {
		t.Fatalf("confirmed destructive cleanup left debt: %+v %v", debts, err)
	}
}

func TestRestartClaimFiltersRetainedDebtFromNewWorkers(t *testing.T) {
	rig := newHostRig(t)
	if err := rig.st.Write(rig.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE boxes SET state='failed',want='run' WHERE id='b'; INSERT INTO room_leftovers(host,room,box,slice) VALUES('h','r','b','')`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	hosts := &retainedDebtHosts{st: rig.st}
	rig.r.SetRoomHosts(hosts)
	rig.r.SetRoomData(retainedDebtData{})
	old, err := rig.st.Box(rig.ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	release, claimed, err := rig.r.QuiesceRestart(rig.ctx, old)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	defer release()
	if err := rig.r.Pass(rig.ctx); err != nil {
		t.Fatal(err)
	}
	rig.r.Wait()
	if got := hosts.calls.Load(); got != 0 {
		t.Fatalf("claimed ID reached a new leftover worker: %d", got)
	}
}
