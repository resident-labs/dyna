package store

import (
	"context"
	"database/sql"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestVolumeRemovalRequiresCurrentOrRecordedTarget(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE boxes SET want='stop' WHERE id=?`, b.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	allowed, err := r.s.CanRemoveBoxVolumes(r.ctx, b.ID)
	if err != nil || !allowed {
		t.Fatalf("unused Stop policy precondition: %v %v", allowed, err)
	}
	called := false
	removed, err := r.s.RemoveBoxVolumes(r.ctx, b.ID, "arch", func(context.Context) error { called = true; return nil })
	if err != nil || !removed || !called {
		t.Fatalf("current target was refused: %v %v %v", removed, called, err)
	}
	called = false
	removed, err = r.s.RemoveBoxVolumes(r.ctx, b.ID, "spare", func(context.Context) error { called = true; return nil })
	if err != nil || removed || called {
		t.Fatalf("unrecorded target inherited another host's unused Stop proof: %v %v %v", removed, called, err)
	}
	debt := Leftover{Host: "spare", Room: "r", Box: b.ID}
	if err := r.s.AddBoxLeftover(r.ctx, debt.Host, debt.Room, debt.Box, debt.Slice); err != nil {
		t.Fatal(err)
	}
	called = false
	removed, err = r.s.RemoveBoxVolumes(r.ctx, b.ID, "spare", func(context.Context) error { called = true; return nil })
	if err != nil || !removed || !called {
		t.Fatalf("recorded prior target was refused: %v %v %v", removed, called, err)
	}
	if err := r.s.ForgetLeftover(r.ctx, debt); err != nil {
		t.Fatal(err)
	}
	called = false
	removed, err = r.s.RemoveBoxVolumes(r.ctx, b.ID, "spare", func(context.Context) error { called = true; return nil })
	if err != nil || removed || called {
		t.Fatalf("retired target regained physical ownership: %v %v %v", removed, called, err)
	}
}

func TestDeletedRoomVolumeProofStaysWithItsRecordedTarget(t *testing.T) {
	r, _ := newCreateRetryRig(t, new(contract.HostName))
	const id contract.BoxID = "gone-room-box"
	if err := r.s.AddBoxLeftover(r.ctx, "arch", "gone-room", id, ""); err != nil {
		t.Fatal(err)
	}
	allowed, err := r.s.CanRemoveBoxVolumes(r.ctx, id)
	if err != nil || !allowed {
		t.Fatalf("deleted-room policy precondition: %v %v", allowed, err)
	}
	for _, target := range []struct {
		host contract.HostName
		want bool
	}{{"arch", true}, {"spare", false}} {
		called := false
		removed, err := r.s.RemoveBoxVolumes(r.ctx, id, target.host, func(context.Context) error { called = true; return nil })
		if err != nil || removed != target.want || called != target.want {
			t.Fatalf("deleted-room proof escaped its target %s: removed=%v called=%v want=%v err=%v", target.host, removed, called, target.want, err)
		}
	}
}
