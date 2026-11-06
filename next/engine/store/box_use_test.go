package store

import (
	"database/sql"
	"github.com/resident-labs/dyna/contract"
	"testing"
	"time"
)

func TestRetryAndStopShareWholeHistoryUnusedProof(t *testing.T) {
	for _, scenario := range []struct {
		name                                             string
		known, ready, turn, restarted, recovered, unused bool
	}{
		{name: "first-create", known: true, unused: true},
		{name: "unknown"},
		{name: "ever-ready", known: true, ready: true},
		{name: "old-turn-no-ready-event", known: true, turn: true},
		{name: "never-ready-owner-restart", known: true, restarted: true},
		{name: "recovery-without-ready-event", known: true, recovered: true},
		{name: "ready-before-owner-allocation", known: true, ready: true, restarted: true},
		{name: "turn-before-owner-allocation", known: true, turn: true, restarted: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var placement *contract.HostName
			if scenario.known {
				placement = new(contract.HostName)
			}
			r, b := newCreateRetryRig(t, placement)
			if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
				if scenario.ready {
					if _, err := r.s.AppendEvent(tx, contract.Event{ID: "used-ready", Room: "r", Time: r.now.Add(-time.Minute), From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: b.ID, Session: b.Session, From: contract.BoxCreating, To: contract.BoxReady}}); err != nil {
						return err
					}
				}
				if scenario.turn {
					if _, err := tx.Exec(`INSERT INTO turns(id,session,started_at,ended_at,outcome,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES('past-turn',?,?,?,'completed','p',0,0,0,0)`, b.Session, r.now.Add(-time.Hour).UnixMilli(), r.now.Add(-time.Minute).UnixMilli()); err != nil {
						return err
					}
				}
				if scenario.recovered {
					if _, err := r.s.AppendEvent(tx, contract.Event{ID: "recovery", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.BoxChanged{Box: b.ID, Session: b.Session, From: contract.BoxStopped, To: contract.BoxStarting}}); err != nil {
						return err
					}
				}
				if scenario.restarted {
					if _, err := r.s.AppendEvent(tx, contract.Event{ID: "owner-allocation", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.BoxChanged{Box: b.ID, Session: b.Session, From: contract.BoxFailed, To: contract.BoxPending}}); err != nil {
						return err
					}
				}
				_, err := tx.Exec("UPDATE boxes SET want='stop',image='' WHERE id=?", b.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			allowed, err := r.s.CanRemoveBoxVolumes(r.ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			if allowed != scenario.unused {
				t.Errorf("Stop unused proof=%v, wanted %v", allowed, scenario.unused)
			}
			if err := r.s.Write(r.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("UPDATE boxes SET want='run' WHERE id=?", b.ID); return err }); err != nil {
				t.Fatal(err)
			}
			r.finishAllocation(b, contract.CauseCreateFailed, "failed create", "")
			after, err := r.s.Box(r.ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			retried := after.Host == "" && after.State == contract.BoxPending
			if retried != scenario.unused {
				t.Errorf("retry did not share Stop's whole-history unused proof: retried=%v wanted=%v box=%+v", retried, scenario.unused, after)
			}
			for _, want := range []BoxWant{WantClose, WantDiscard} {
				if err := r.s.Write(r.ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec("UPDATE boxes SET want=? WHERE id=?", want, b.ID)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				allowed, err = r.s.CanRemoveBoxVolumes(r.ctx, b.ID)
				if err != nil || !allowed {
					t.Errorf("explicit owner %s lost existing deletion semantics: %v %v", want, allowed, err)
				}
			}
		})
	}
}

func TestLeftoverVolumePermissionRequiresExplicitDeletion(t *testing.T) {
	r, b := newCreateRetryRig(t, new(contract.HostName))
	if err := r.s.AddBoxLeftover(r.ctx, "arch", "r", b.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.s.AddBoxLeftover(r.ctx, "arch", "r", "legacy-unknown", ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []contract.BoxID{b.ID, "legacy-unknown", "missing-without-debt"} {
		allowed, err := r.s.CanRemoveBoxVolumes(r.ctx, id)
		if err != nil || allowed {
			t.Fatalf("ordinary/unknown failure debt got deletion permission for %s: %v %v", id, allowed, err)
		}
	}
	event := contract.Event{ID: "owner-delete-room", Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalOwner}, Body: contract.RoomDeleting{}}
	if err := r.s.MarkRoomDeleting(r.ctx, "r", event); err != nil {
		t.Fatal(err)
	}
	for _, id := range []contract.SessionID{"worker", "lead", "root"} {
		e := contract.Event{ID: contract.EventID("close-" + string(id)), Room: "r", Time: r.now, From: contract.Principal{Kind: contract.PrincipalEngine}, Body: contract.SessionClosed{Session: id}}
		if err := r.s.CloseSession(r.ctx, id, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.s.DeleteRoom(r.ctx, "r"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []contract.BoxID{b.ID, "legacy-unknown"} {
		allowed, err := r.s.CanRemoveBoxVolumes(r.ctx, id)
		if err != nil || !allowed {
			t.Fatalf("explicit deleted-room debt lost deletion permission for %s: %v %v", id, allowed, err)
		}
	}
	allowed, err := r.s.CanRemoveBoxVolumes(r.ctx, "missing-without-debt")
	if err != nil || allowed {
		t.Fatalf("absence alone became deletion permission: %v %v", allowed, err)
	}
}
