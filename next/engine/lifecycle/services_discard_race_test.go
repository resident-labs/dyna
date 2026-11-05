package lifecycle

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
)

// pausedServiceWork pauses after the reconciler has decided to write/start, but before the host does it. The operation log
// records the side effects, not just the calls: discard must remove last even when a revision check has already succeeded.
type pausedServiceWork struct {
	box.ServiceDriver
	at, release chan struct{}
	restart     bool
	paused      atomic.Bool
	mu          sync.Mutex
	ops         []string
}

func (d *pausedServiceWork) record(op string) {
	d.mu.Lock()
	d.ops = append(d.ops, op)
	d.mu.Unlock()
}

func (d *pausedServiceWork) pause() {
	if d.paused.CompareAndSwap(false, true) {
		close(d.at)
		<-d.release
	}
}

func (d *pausedServiceWork) WriteServiceUnit(ctx context.Context, spec box.ServiceUnitSpec) (string, error) {
	if !d.restart {
		d.pause()
	}
	sha, err := d.ServiceDriver.WriteServiceUnit(ctx, spec)
	d.record("write")
	return sha, err
}

func (d *pausedServiceWork) RestartService(ctx context.Context, id contract.ServiceID) error {
	if d.restart {
		d.pause()
	}
	err := d.ServiceDriver.RestartService(ctx, id)
	d.record("restart")
	return err
}

func (d *pausedServiceWork) RemoveService(ctx context.Context, id contract.ServiceID) error {
	err := d.ServiceDriver.RemoveService(ctx, id)
	d.record("remove")
	return err
}

type pausedServiceHosts struct{ driver box.ServiceDriver }

func (h pausedServiceHosts) ServiceDriver(context.Context, contract.HostName) (box.ServiceDriver, error) {
	return h.driver, nil
}
func (h pausedServiceHosts) ServiceRoomImage(context.Context, contract.HostName, contract.RoomID, string) (string, error) {
	return "localhost/room-image:1", nil
}

func TestDiscardWaitsForServiceHostWorkBeforeRemoving(t *testing.T) {
	for _, action := range []string{"deploy", "sync", "rollback", "restart"} {
		t.Run(action, func(t *testing.T) {
			rig := newServiceRig(t)
			spec := web()
			rig.declare(spec)
			row := rig.settle()
			// Keep this about services; the existing box discard tests cover their own cleanup.
			rig.sql("DELETE FROM boxes")
			switch action {
			case "deploy", "rollback":
				spec.Env = map[string]string{"NEW": "yes"}
				row = rig.declare(spec)
				if action == "rollback" {
					rig.host.Service.Fail[fakehost.PullService] = errors.New("pull failed")
				}
			case "sync":
				delete(rig.host.Service.Files, row.ID)
				rig.r.SetServices(serviceHosts{rig.host}, rig.r.svc.timings)
			}
			driver := &pausedServiceWork{ServiceDriver: rig.host, at: make(chan struct{}), release: make(chan struct{}), restart: action == "restart"}
			release := sync.OnceFunc(func() { close(driver.release) })
			defer release()
			rig.r.svc.hosts = pausedServiceHosts{driver}
			work := rig.r.serviceHostWork(row.Host)
			workerDone := make(chan error, 1)
			if action == "restart" {
				go func() {
					_, err := rig.r.RestartServiceUnit(rig.ctx, row)
					workerDone <- err
				}()
			} else {
				// Exercise launchRoomStep, not just serviceStep called directly.
				if err := rig.r.Pass(rig.ctx); err != nil {
					t.Fatal(err)
				}
				go func() { rig.r.Wait(); workerDone <- nil }()
			}
			select {
			case <-driver.at:
			case <-time.After(3 * time.Second):
				t.Fatal("service never reached its host side-effect barrier")
			}
			// With the host call paused, its worker must own the gate: a read lock for serviceStep, or an exclusive lock
			// for direct restart. A later failed TryRLock detects queued cleanup for readers; restart also checks durable drain.
			if work.TryLock() {
				work.Unlock()
				release()
				<-workerDone
				t.Fatal("host side effect was not covered by the work gate")
			}
			// For deploy this is after PhaseUnit's successful CAS, not before a revision guard.
			if action == "deploy" && rig.row().Reason != contract.ServicePhaseUnit {
				t.Fatalf("not paused after the unit phase CAS: %+v", rig.row().Service)
			}
			discardDone := make(chan struct{})
			var discardErr error
			go func() {
				_, discardErr = rig.r.discardHost(rig.ctx, row.Host)
				close(discardDone)
			}()

			// Wait for positive evidence that discard has either queued its exclusive cleanup or (in the broken version)
			// already returned. No sleep is used to guess whether cleanup had time to race the paused host call.
			deadline := time.After(3 * time.Second)
		waiting:
			for {
				select {
				case <-discardDone:
					break waiting
				case <-deadline:
					t.Fatal("discard neither joined host work nor returned")
				default:
				}
				if !work.TryRLock() {
					// Direct restart itself holds an exclusive gate. In that case a denied read alone does not prove discard
					// has committed its drain; require the durable drain before releasing the paused host call.
					hosts, err := rig.st.Hosts(rig.ctx)
					if err != nil {
						t.Fatal(err)
					}
					if len(hosts) == 1 && hosts[0].State == box.HostDraining {
						break
					}
				} else {
					work.RUnlock()
				}
				runtime.Gosched()
			}
			// The drain committed before waiting for I/O. Admission stays shut while that I/O finishes.
			hosts, err := rig.st.Hosts(rig.ctx)
			if err != nil || len(hosts) != 1 || hosts[0].State != box.HostDraining {
				t.Fatalf("host was not drained before joining work: %+v, %v", hosts, err)
			}
			if action == "deploy" {
				// A slow host call must not hold the pass lock or admit another service to the draining host.
				pending := web()
				pending.Name, pending.Ports = "zzz-pending", []contract.ServicePort{"8443"}
				waiting := rig.declare(pending)
				passed := make(chan error, 1)
				go func() { passed <- rig.r.Pass(rig.ctx) }()
				select {
				case err := <-passed:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("discard held the pass lock while waiting for host work")
				}
				rows, err := rig.st.RoomServices(rig.ctx, row.Room)
				if err != nil {
					t.Fatal(err)
				}
				for _, placed := range rows {
					if placed.ID == waiting.ID && placed.Host != "" {
						t.Fatalf("a new service was placed after the drain: %+v", placed.Service)
					}
				}
			}
			release()
			select {
			case err := <-workerDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("worker did not finish")
			}
			select {
			case <-discardDone:
			case <-time.After(3 * time.Second):
				t.Fatal("discard did not finish after host work")
			}
			if discardErr != nil {
				t.Fatal(discardErr)
			}
			if len(driver.ops) == 0 || driver.ops[len(driver.ops)-1] != "remove" || len(rig.host.Service.Files) != 0 {
				t.Fatalf("discard was not the last host side effect: ops=%v files=%v", driver.ops, rig.host.Service.Files)
			}
			if failed := rig.row(); failed.State != contract.ServiceFailed || failed.Reason != contract.ServiceHostRemoved {
				t.Fatalf("an old worker replaced the discarded state: %+v", failed.Service)
			}
		})
	}
}

func TestQueuedServiceWorkCannotUseADiscardedHostSnapshot(t *testing.T) {
	rig := newServiceRig(t)
	rig.declare(web())
	row := rig.settle()
	hosts, err := rig.st.Hosts(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldHost := hosts[0]
	rig.sql("DELETE FROM boxes")
	if _, err := rig.r.discardHost(rig.ctx, row.Host); err != nil {
		t.Fatal(err)
	}
	// Force the rewrite path if an old ready-host snapshot gets through.
	rig.r.ObserveServices([]box.ServiceUnit{{ID: row.ID, State: box.UnitAbsent}})
	for _, state := range []string{"draining", "removed", "re-added"} {
		switch state {
		case "removed":
			if err := rig.st.RemoveHost(rig.ctx, row.Host); err != nil {
				t.Fatal(err)
			}
		case "re-added":
			if err := rig.st.AddHost(rig.ctx, oldHost); err != nil {
				t.Fatal(err)
			}
		}
		calls := slices.Clone(rig.host.Calls)
		if err := rig.r.serviceStep(rig.ctx, row, oldHost); err != nil {
			t.Fatal(err)
		}
		if _, err := rig.r.RestartServiceUnit(rig.ctx, row); !errors.Is(err, ErrNotPlaced) {
			t.Fatalf("%s: restart of discarded placement: %v", state, err)
		}
		if !slices.Equal(calls, rig.host.Calls) {
			t.Fatalf("%s: stale work reached the host: %v -> %v", state, calls, rig.host.Calls)
		}
	}
}
