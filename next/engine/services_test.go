package engine

import (
	"context"
	"database/sql"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

const svcDigest = "ghcr.io/o/r@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func serviceHost(t *testing.T, r *poolRig, name contract.HostName) {
	t.Helper()
	engine := false
	if err := r.s.store.AddHost(r.ctx, box.Host{Name: name, State: box.HostReady, EngineHost: &engine, CPUs: 47, MemBytes: 128 << 30, DiskBytes: 800 << 30, CheckedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
}

func setService(r *poolRig, spec contract.ServiceSpec) (contract.ServiceSetResponse, *contract.Error) {
	return r.s.ServiceSet(r.ctx, r.owner, contract.ServiceSetRequest{Room: r.room, Spec: spec})
}

func TestServiceSetRefusesWhatPlacementCouldNotKeep(t *testing.T) {
	r := setupPool(t)
	serviceHost(t, r, "h")
	serviceHost(t, r, "other")
	base := contract.ServiceSpec{Name: "web", Image: svcDigest, Ports: []contract.ServicePort{"8443"}, Host: "h"}

	// A host that does not exist, and a host outside the room's rule.
	missing := base
	missing.Host = "nowhere"
	if _, e := setService(r, missing); e == nil || e.Code != contract.ErrInvalid {
		t.Fatalf("unknown host: %v", e)
	}
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE rooms SET only_hosts='["h"]' WHERE id=?`, r.room)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	outside := base
	outside.Host = "other"
	if _, e := setService(r, outside); e == nil || e.Code != contract.ErrInvalid || !strings.Contains(e.Message, "rule") {
		t.Fatalf("host outside the rule: %v", e)
	}

	// The same host port on one host is port_taken at set.
	if _, e := setService(r, base); e != nil {
		t.Fatalf("set: %v", e)
	}
	second := base
	second.Name = "api"
	if _, e := setService(r, second); e == nil || e.Code != contract.ErrPortTaken {
		t.Fatalf("port taken: %v", e)
	}

	// An update to a placed service goes through admission with its own reservation set aside: web at 8 CPUs fits a quarter of 47
	// (11.75), and a second service growing to 8 CPUs beside it does not.
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE services SET host='h'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	big := base
	big.CPUs = 8
	if _, e := setService(r, big); e != nil {
		t.Fatalf("8 CPUs on 47 fit a quarter: %v", e)
	}
	api := contract.ServiceSpec{Name: "api", Image: svcDigest, Host: "h"}
	if _, e := setService(r, api); e != nil {
		t.Fatalf("api: %v", e)
	}
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE services SET host='h'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	api.CPUs = 8
	if _, e := setService(r, api); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("8 more CPUs beside web exceed a quarter, got %v", e)
	}
}

func TestPlacedServiceUpdatesShareTheRoomCPUAndMemoryBudgets(t *testing.T) {
	r := setupPool(t)
	serviceHost(t, r, "h")
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM boxes"); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE rooms SET cpus=3,mem_bytes=? WHERE id=?", 3<<30, r.room)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	web := contract.ServiceSpec{Name: "web", Image: svcDigest, Host: "h", CPUs: 2, Memory: "1GiB"}
	api := contract.ServiceSpec{Name: "api", Image: svcDigest, Host: "h", CPUs: 1, Memory: "1GiB"}
	for _, spec := range []contract.ServiceSpec{web, api} {
		if _, e := setService(r, spec); e != nil {
			t.Fatal(e)
		}
	}
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE services SET host='h'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// No-op update excludes its own reservation, while another service counts at 1x, not a box's quarter.
	if _, e := setService(r, web); e != nil {
		t.Fatalf("unchanged size: %v", e)
	}
	web.CPUs = 3
	if _, e := setService(r, web); e == nil || e.Code != contract.ErrConflict {
		t.Errorf("room CPU budget ignored: %v", e)
	}
	web.CPUs, web.Memory = 2, "2GiB"
	if _, e := setService(r, web); e != nil {
		t.Fatalf("2+1 GiB should fit: %v", e)
	}
	web.Memory = "3GiB"
	if _, e := setService(r, web); e == nil || e.Code != contract.ErrConflict {
		t.Errorf("room memory budget ignored: %v", e)
	}
}

func TestPlacedServiceUpdateCountsCIRunners(t *testing.T) {
	r := setupPool(t)
	serviceHost(t, r, "h")
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM boxes"); err != nil {
			return err
		}
		_, err := tx.Exec("UPDATE hosts SET mem_bytes=? WHERE name='h'", 121<<30)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	spec := contract.ServiceSpec{Name: "web", Image: svcDigest, Host: "h", Memory: "512MiB"}
	if _, e := setService(r, spec); e != nil {
		t.Fatal(e)
	}
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE services SET host='h'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	runners := &fakeRunners{}
	for i := range 7 {
		runners.placed = append(runners.placed, box.Box{ID: contract.BoxID(fmt.Sprintf("runner-%d", i)), Host: "h", Image: box.RunnerImage, State: contract.BoxReady, Size: contract.BoxSize{CPUs: 1, MemBytes: 16 << 30}})
	}
	r.s.reconciler.SetRunners(runners)
	spec.Memory = "16GiB"
	if _, e := setService(r, spec); e == nil || e.Code != contract.ErrConflict {
		t.Fatalf("placed service grew past live runner memory: %v", e)
	}
}

// Pause the first request after its read/admission but before its write. Let the competing operation either reach its own
// post-admission boundary (the broken code), or park on the shared admission mutex (the fixed code), before releasing the
// first. Stack inspection is only a barrier observer here: there is no sleep and the assertions are on committed reservations.
func serviceAdmissionBarrier(t *testing.T, r *poolRig) (entered, secondAtWrite <-chan struct{}, release func()) {
	t.Helper()
	first, second, resume := make(chan struct{}), make(chan struct{}), make(chan struct{})
	clock := r.s.now
	var n atomic.Int32
	r.s.now = func() time.Time {
		switch n.Add(1) {
		case 1:
			close(first)
			<-resume
		case 2:
			close(second)
		}
		return clock()
	}
	release = sync.OnceFunc(func() { close(resume) })
	t.Cleanup(release)
	return first, second, release
}

func awaitServiceAdmissionCompetitor(t *testing.T, secondAtWrite <-chan struct{}, operationDone <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	buf := make([]byte, 1<<20)
	for {
		select {
		case <-secondAtWrite:
			return
		case <-operationDone:
			return
		case <-ctx.Done():
			t.Fatal("competing admission neither completed nor reached its barrier")
		default:
		}
		stack := string(buf[:runtime.Stack(buf, true)])
		if strings.Contains(stack, "(*Store).LockAdmission") || strings.Contains(stack, "(*Reconciler).LockServiceDeclaration") {
			return
		}
		runtime.Gosched()
	}
}

func TestConcurrentPlacedServiceUpsizesCannotBothSpendTheSameCapacity(t *testing.T) {
	r := setupPool(t)
	serviceHost(t, r, "h")
	for _, name := range []contract.ServiceName{"a", "b"} {
		if _, e := setService(r, contract.ServiceSpec{Name: name, Image: svcDigest, Host: "h", CPUs: .25}); e != nil {
			t.Fatal(e)
		}
	}
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE services SET host='h'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	entered, secondAtWrite, release := serviceAdmissionBarrier(t, r)
	results := make(chan *contract.Error, 2)
	go func() {
		_, e := setService(r, contract.ServiceSpec{Name: "a", Image: svcDigest, Host: "h", CPUs: 8})
		results <- e
	}()
	<-entered
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		_, e := setService(r, contract.ServiceSpec{Name: "b", Image: svcDigest, Host: "h", CPUs: 8})
		results <- e
	}()
	awaitServiceAdmissionCompetitor(t, secondAtWrite, secondDone)
	release()
	accepted := 0
	for range 2 {
		if e := <-results; e == nil {
			accepted++
		} else if e.Code != contract.ErrConflict {
			t.Errorf("unexpected refusal: %v", e)
		}
	}
	if accepted != 1 {
		t.Fatalf("%d concurrent 8-CPU upsizes accepted on a 47-CPU host (quarter cap 11.75)", accepted)
	}
}

type admissionServiceHosts struct{ host box.ServiceDriver }

func (s admissionServiceHosts) ServiceDriver(context.Context, contract.HostName) (box.ServiceDriver, error) {
	return s.host, nil
}
func (s admissionServiceHosts) ServiceRoomImage(context.Context, contract.HostName, contract.RoomID, string) (string, error) {
	return "room-image", nil
}

func TestServicePlacementCannotSpendCapacityAnInFlightUpdateIsReserving(t *testing.T) {
	r := setupPool(t)
	serviceHost(t, r, "h")
	for _, spec := range []contract.ServiceSpec{
		{Name: "a", Image: svcDigest, Host: "h", CPUs: .25},
		{Name: "b", Image: svcDigest, Host: "h", CPUs: 8},
	} {
		if _, e := setService(r, spec); e != nil {
			t.Fatal(e)
		}
	}
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec("UPDATE services SET host='h',state='failed' WHERE name='a'")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r.s.reconciler.SetServices(admissionServiceHosts{fakehost.New()}, lifecycle.ServiceTimings{Start: 50 * time.Millisecond, Stable: 20 * time.Millisecond, Health: 50 * time.Millisecond, Poll: time.Millisecond, CheckEvery: time.Hour})
	entered, secondAtWrite, release := serviceAdmissionBarrier(t, r)
	updated := make(chan *contract.Error, 1)
	go func() {
		_, e := setService(r, contract.ServiceSpec{Name: "a", Image: svcDigest, Host: "h", CPUs: 8})
		updated <- e
	}()
	<-entered
	passed, passDone := make(chan error, 1), make(chan struct{})
	go func() { defer close(passDone); passed <- r.s.reconciler.Pass(r.ctx) }()
	awaitServiceAdmissionCompetitor(t, secondAtWrite, passDone)
	release()
	if e := <-updated; e != nil {
		t.Fatal(e)
	}
	if err := <-passed; err != nil {
		t.Fatal(err)
	}
	r.s.reconciler.Wait()
	rows, err := r.s.store.Services(r.ctx, r.room)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Spec.Name == "b" && row.Host != "" {
			t.Fatalf("placement spent concurrently reserved capacity: %+v", row.Service)
		}
	}
}
