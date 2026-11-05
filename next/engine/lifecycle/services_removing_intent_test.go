package lifecycle

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

func TestIntentCannotReviveRemovingInflightServiceAndDropItsHold(t *testing.T) {
	rig := newServiceRig(t)
	spec := web()
	rig.declare(spec)
	rig.settle()
	rig.sql("DELETE FROM boxes")
	spec.CPUs, spec.Ports = 4, []contract.ServicePort{"8443"}
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
		t.Fatal("not paused after start CAS")
	}
	// An owner action can have resolved this ID before another request removes the service.
	if err := rig.r.RemoveService(rig.ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	// changeService's ID read includes this removing row, but its name-based guard excludes it and takes no host gate.
	if _, err := rig.r.StartService(rig.ctx, row.ID); err != nil {
		if !errors.Is(err, ErrServiceRemoving) {
			t.Fatalf("wrong refusal: %v", err)
		}
		release()
		rig.r.Wait()
		return // Refusing stale owner intent would close the hole.
	}
	release()
	rig.r.Wait()
	spec.CPUs, spec.Ports = 1, []contract.ServicePort{"9443"}
	parts, err := spec.Parse()
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := rig.r.LockServiceDeclaration(rig.ctx, row.Room, spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	shrunk, err := rig.st.SetService(rig.ctx, row.Room, parts, row.ID, contract.Principal{Kind: contract.PrincipalOwner}, rig.now, rig.r.serviceEvent)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	if shrunk.CPUMilli < 4000 && strings.Contains(rig.host.Service.Files[row.ID], "--cpus=4") {
		t.Fatalf("owner intent bypassed host work on a removing row, revived it, and released live capacity: hold=%d actual unit=%s", shrunk.CPUMilli, rig.host.Service.Files[row.ID])
	}
}
