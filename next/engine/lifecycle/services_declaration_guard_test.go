package lifecycle

import (
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestServiceDeclarationJoinsInflightGenerationBeforeReplacingReservation(t *testing.T) {
	rig := newServiceRig(t)
	rig.sql("UPDATE hosts SET cpus=16") // service cap = 4 CPUs
	spec := web()
	rig.declare(spec)
	row := rig.settle() // last successful Live = 1 CPU, port 443
	rig.sql("DELETE FROM boxes")
	spec.CPUs, spec.Ports = 4, []contract.ServicePort{"8443"}
	row = rig.declare(spec)
	driver := &pausedServiceWork{ServiceDriver: rig.host, at: make(chan struct{}), release: make(chan struct{}), restart: true}
	release := sync.OnceFunc(func() { close(driver.release) })
	defer release()
	rig.r.svc.hosts = pausedServiceHosts{driver}
	if err := rig.r.Pass(rig.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-driver.at:
	case <-time.After(3 * time.Second):
		t.Fatal("deploy did not reach restart after PhaseStart CAS")
	}
	if current := rig.row(); current.Reason != contract.ServicePhaseStart {
		t.Fatalf("not past start CAS: %+v", current.Service)
	}

	// The declaration must acquire the production guard before reading current Live and deciding capacity, as ServiceSet does.
	spec.CPUs, spec.Ports = 1, []contract.ServicePort{"9443"}
	parts, err := spec.Parse()
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		row store.ServiceRow
		err error
	}
	updated := make(chan result, 1)
	go func() {
		change := func() (store.ServiceRow, error) {
			unlock, err := rig.r.LockServiceDeclaration(rig.ctx, row.Room, spec.Name)
			if err != nil {
				return store.ServiceRow{}, err
			}
			defer unlock()
			current, err := rig.st.ServiceNamed(rig.ctx, row.Room, spec.Name)
			if err != nil {
				return store.ServiceRow{}, err
			}
			hosts, err := rig.st.Hosts(rig.ctx)
			if err != nil {
				return store.ServiceRow{}, err
			}
			boxes, err := rig.st.Boxes(rig.ctx)
			if err != nil {
				return store.ServiceRow{}, err
			}
			limits, err := rig.st.RoomLimits(rig.ctx, row.Room)
			if err != nil {
				return store.ServiceRow{}, err
			}
			holds, err := rig.st.ServiceHolds(rig.ctx)
			if err != nil {
				return store.ServiceRow{}, err
			}
			candidate := current
			candidate.Parts = parts
			fitted := candidate.Reservation()
			fitted.Spec.Host = current.Host
			// Every generation in this fixture uses the same measured image.
			request := box.ServiceRequest{ID: current.ID, Room: current.Room, Parts: fitted, ImageEstimate: current.ImageBytes}
			if !box.ServiceFitsRoom(request, limits, boxes, holds) || box.AdmitService(hosts, boxes, request).Host == "" {
				return store.ServiceRow{}, fmt.Errorf("valid shrink did not fit")
			}
			return rig.st.SetService(rig.ctx, current.Room, parts, current.ID, contract.Principal{Kind: contract.PrincipalOwner}, rig.now, rig.r.serviceEvent)
		}
		next, err := change()
		updated <- result{next, err}
	}()

	work := rig.r.serviceHostWork(row.Host)
	var early *result
	deadline := time.After(3 * time.Second)
waiting:
	for {
		select {
		case got := <-updated:
			early = &got // broken guard changed the row while the older generation could still start
			break waiting
		case <-deadline:
			t.Fatal("declaration neither waited at the host gate nor returned")
		default:
		}
		if !work.TryRLock() {
			break
		}
		work.RUnlock()
		runtime.Gosched()
	}
	// The waiting declaration must not already own admission; otherwise it can stall all placement behind remote host I/O.
	passed := make(chan error, 1)
	go func() { passed <- rig.r.Pass(rig.ctx) }()
	select {
	case err := <-passed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("declaration held admission while waiting for the host worker")
	}
	release()
	rig.r.Wait()
	var got result
	if early != nil {
		got = *early
	} else {
		select {
		case got = <-updated:
		case <-time.After(3 * time.Second):
			t.Fatal("declaration did not finish after the worker")
		}
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.row.CPUMilli != 4000 || got.row.Live == nil || got.row.Live.CPUMilli != 4000 {
		t.Fatalf("declaration released an older generation that just started: hold=%d Live=%+v", got.row.CPUMilli, got.row.Live)
	}
	port := contract.PortBinding{Host: 8443, Container: 8443, Proto: "tcp"}
	if !slices.Contains(got.row.Reservation().Ports, port) {
		t.Fatalf("declaration released the just-started generation's port: %+v", got.row.Reservation().Ports)
	}
	other := web()
	other.Name, other.Host, other.CPUs, other.Ports = "zzz-other", row.Host, 3, []contract.ServicePort{"8443"}
	otherParts, err := other.Parse()
	if err != nil {
		t.Fatal(err)
	}
	_, err = rig.st.SetService(rig.ctx, row.Room, otherParts, "svc_other", contract.Principal{Kind: contract.PrincipalOwner}, rig.now, rig.r.serviceEvent)
	var taken *store.PortTakenError
	if !errors.As(err, &taken) {
		t.Fatalf("a second declaration reused the just-started port: %v", err)
	}
	hosts, err := rig.st.Hosts(rig.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := box.AdmitService(hosts, nil, box.ServiceRequest{ID: "svc_other", Room: row.Room, Parts: otherParts}); p.Host != "" {
		t.Fatalf("a second declaration spent the just-started generation's CPU reservation: %+v", p)
	}
}

func TestServiceIntentMutationsCannotInvalidateInflightLiveCapture(t *testing.T) {
	for _, action := range []string{"update", "start", "stop", "rollback", "restart"} {
		t.Run(action, func(t *testing.T) {
			rig := newServiceRig(t)
			spec := web()
			rig.declare(spec)
			rig.settle()
			spec.CPUs, spec.Image = 4, digestImage2
			row := rig.declare(spec)
			driver := &pausedServiceWork{ServiceDriver: rig.host, at: make(chan struct{}), release: make(chan struct{}), restart: true}
			release := sync.OnceFunc(func() { close(driver.release) })
			defer release()
			rig.r.svc.hosts = pausedServiceHosts{driver}
			if err := rig.r.Pass(rig.ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-driver.at:
			case <-time.After(3 * time.Second):
				t.Fatal("no restart barrier")
			}
			mutated := make(chan error, 1)
			go func() {
				var err error
				switch action {
				case "update":
					_, err = rig.r.UpdateService(rig.ctx, row.ID)
				case "start":
					_, err = rig.r.StartService(rig.ctx, row.ID)
				case "stop":
					_, err = rig.r.StopService(rig.ctx, row.ID)
				case "rollback":
					_, err = rig.r.RollbackService(rig.ctx, row.ID)
				case "restart":
					_, err = rig.r.RestartServiceUnit(rig.ctx, row)
				}
				mutated <- err
			}()
			work := rig.r.serviceHostWork(row.Host)
			var early error
			returned := false
			deadline := time.After(3 * time.Second)
		waiting:
			for {
				select {
				case early = <-mutated:
					returned = true
					break waiting
				case <-deadline:
					t.Fatal("mutation did not reach its work gate")
				default:
				}
				if !work.TryRLock() {
					break
				}
				work.RUnlock()
				runtime.Gosched()
			}
			release()
			rig.r.Wait()
			if !returned {
				early = <-mutated
			}
			if early != nil {
				t.Fatal(early)
			}
			current := rig.row()
			if current.Live == nil || current.Live.CPUMilli != 4000 || current.CPUMilli != 4000 {
				t.Fatalf("%s invalidated capture of the unit that started: %+v Live=%+v", action, current.Service, current.Live)
			}
		})
	}
}
