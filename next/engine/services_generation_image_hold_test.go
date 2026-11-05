package engine

import (
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

func TestServiceGenerationActionsReserveLatestImageBeforeCompetingPlacement(t *testing.T) {
	for _, action := range []string{"update", "secret"} {
		t.Run(action, func(t *testing.T) {
			r := setupPool(t)
			serviceHost(t, r, "h")
			if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
				if _, err := tx.Exec("DELETE FROM boxes"); err != nil {
					return err
				}
				_, err := tx.Exec("UPDATE hosts SET disk_floor=0,disk_bytes=?", 2<<30)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			host := fakehost.New()
			r.s.reconciler.SetServices(admissionServiceHosts{host}, lifecycle.ServiceTimings{Start: 50 * time.Millisecond, Stable: 20 * time.Millisecond, Health: 50 * time.Millisecond, Poll: time.Millisecond, CheckEvery: time.Hour})
			if _, e := setService(r, contract.ServiceSpec{Name: "web", Image: "app", Host: "h", Disk: "0GiB", Secrets: []string{"TOKEN"}}); e != nil {
				t.Fatal(e)
			}
			if _, e := r.s.ServiceSecretSet(r.ctx, r.owner, contract.ServiceSecretSetRequest{Room: r.room, Name: "web", Secret: "TOKEN", Value: "v1"}); e != nil {
				t.Fatal(e)
			}
			for range 3 {
				passLifecycle(t, r.s, r.ctx)
			}
			old, err := r.s.store.ServiceNamed(r.ctx, r.room, "web")
			if err != nil || old.State != contract.ServiceRunning || old.ImageBytes != 100<<20 || old.ImageEstimate != 0 {
				t.Fatalf("initial deployment must have cleared its estimate: %+v, %v", old, err)
			}
			if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
				_, err := tx.Exec("UPDATE hosts SET disk_bytes=?", 512<<20)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			unlock := sync.OnceFunc(r.s.store.LockAdmission())
			t.Cleanup(unlock)
			finished, result := make(chan struct{}), make(chan *contract.Error, 1)
			go func() {
				defer close(finished)
				switch action {
				case "update":
					_, e := r.s.ServiceUpdate(r.ctx, r.owner, contract.ServiceUpdateRequest{Room: r.room, Name: "web"})
					result <- e
				case "secret":
					_, e := r.s.ServiceSecretSet(r.ctx, r.owner, contract.ServiceSecretSetRequest{Room: r.room, Name: "web", Secret: "TOKEN", Value: "v2"})
					result <- e
				}
			}()
			awaitServiceAdmissionCompetitor(t, nil, finished)
			blocked, err := r.s.store.Service(r.ctx, old.ID)
			if err != nil || blocked.Generation != old.Generation || blocked.ImageEstimate != 0 {
				t.Errorf("generation wrote its reservation outside admission: %+v, %v", blocked, err)
			}
			// A measurement changes while the action waits for admission. It must read the source estimate after entering,
			// not carry the old deployment's cleared estimate (or an estimate read before the gate) into its write.
			if err := r.s.store.RecordServiceImage(r.ctx, old.Room, old.Parts, 1<<30, time.Now().Add(time.Minute)); err != nil {
				t.Fatal(err)
			}
			unlock()
			if e := <-result; e != nil {
				t.Fatal(e)
			}
			// This is a committed reservation before any reconciliation/preflight can repair a local copy.
			current, err := r.s.store.Service(r.ctx, old.ID)
			if err != nil || current.Generation != old.Generation+1 || current.ImageEstimate != 1<<30 || current.Live == nil {
				t.Errorf("generation did not persist its newest source estimate and live declaration: %+v, %v", current, err)
			}
			holds, err := r.s.store.ServiceHolds(r.ctx)
			if err != nil || len(holds) != 1 || holds[0].DiskBytes != 1<<30 {
				t.Errorf("reservation before pass: %+v, %v", holds, err)
			}

			// With only the old 100 MiB hold, this measured 256 MiB competitor would fit in 512 MiB.
			// The new 1 GiB hold must keep both services out of image work in this same pass.
			competitor := contract.ServiceSpec{Name: "zzz", Image: svcDigest, Disk: "0GiB"}
			parts, err := competitor.Parse()
			if err != nil {
				t.Fatal(err)
			}
			if err := r.s.store.RecordServiceImage(r.ctx, r.room, parts, 256<<20, time.Now()); err != nil {
				t.Fatal(err)
			}
			if _, e := setService(r, competitor); e != nil {
				t.Fatal(e)
			}
			before := len(host.Calls)
			passLifecycle(t, r.s, r.ctx)
			current, err = r.s.store.Service(r.ctx, old.ID)
			if err != nil || current.Reason != contract.ServiceNoDisk || current.ImageEstimate != 1<<30 || current.Deployed != old.Deployed {
				t.Errorf("new generation passed disk preflight: %+v, %v", current, err)
			}
			waiting, err := r.s.store.ServiceNamed(r.ctx, r.room, competitor.Name)
			if err != nil || waiting.Host != "" || waiting.Reason != contract.ServiceNoDisk {
				t.Errorf("same-pass placement spent the generation's disk hold: %+v, %v", waiting, err)
			}
			for _, call := range host.Calls[before:] {
				if call == fakehost.ImageSize || call == fakehost.PullService {
					t.Errorf("image work despite insufficient disk: %v", call)
				}
			}
		})
	}
}
