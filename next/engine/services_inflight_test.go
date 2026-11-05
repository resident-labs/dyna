package engine

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/box/fakehost"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/lifecycle"
)

type serviceRestartBarrier struct {
	box.ServiceDriver
	at, resume chan struct{}
	once       sync.Once
}

func (d *serviceRestartBarrier) RestartService(ctx context.Context, id contract.ServiceID) error {
	d.once.Do(func() { close(d.at); <-d.resume })
	return d.ServiceDriver.RestartService(ctx, id)
}

// A secret edit changes revision/generation too. It must wait for the in-flight unit to be captured as Live, or a later shrink
// could discard the newly started unit's reservation even though the declaration path itself waits for host work.
func TestSecretEditCannotInvalidateInflightLiveCapture(t *testing.T) {
	r := setupPool(t)
	serviceHost(t, r, "h")
	if err := r.s.store.Write(r.ctx, func(tx *sql.Tx) error { _, err := tx.Exec("DELETE FROM boxes"); return err }); err != nil {
		t.Fatal(err)
	}
	host := fakehost.New()
	timings := lifecycle.ServiceTimings{Start: 50 * time.Millisecond, Stable: 20 * time.Millisecond, Health: 50 * time.Millisecond, Poll: time.Millisecond, CheckEvery: time.Hour}
	r.s.reconciler.SetServices(admissionServiceHosts{host}, timings)
	spec := contract.ServiceSpec{Name: "web", Image: svcDigest, Host: "h", CPUs: 1, Secrets: []string{"TOKEN"}}
	if _, e := setService(r, spec); e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.ServiceSecretSet(r.ctx, r.owner, contract.ServiceSecretSetRequest{Room: r.room, Name: "web", Secret: "TOKEN", Value: "v1"}); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		passLifecycle(t, r.s, r.ctx)
	}
	row, err := r.s.store.ServiceNamed(r.ctx, r.room, "web")
	if err != nil || row.Live == nil {
		t.Fatalf("initial deploy: %+v, %v", row.Service, err)
	}
	spec.CPUs = 4
	if _, e := setService(r, spec); e != nil {
		t.Fatal(e)
	}
	driver := &serviceRestartBarrier{ServiceDriver: host, at: make(chan struct{}), resume: make(chan struct{})}
	release := sync.OnceFunc(func() { close(driver.resume) })
	defer release()
	r.s.reconciler.SetServices(admissionServiceHosts{driver}, timings)
	if err := r.s.reconciler.Pass(r.ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-driver.at:
	case <-time.After(3 * time.Second):
		t.Fatal("unit did not reach restart barrier")
	}
	finished, result := make(chan struct{}), make(chan *contract.Error, 1)
	go func() {
		defer close(finished)
		_, e := r.s.ServiceSecretSet(r.ctx, r.owner, contract.ServiceSecretSetRequest{Room: r.room, Name: "web", Secret: "TOKEN", Value: "v2"})
		result <- e
	}()
	awaitServiceAdmissionCompetitor(t, nil, finished)
	release()
	r.s.reconciler.Wait()
	if e := <-result; e != nil {
		t.Fatal(e)
	}
	row, err = r.s.store.ServiceNamed(r.ctx, r.room, "web")
	if err != nil || row.Live == nil || row.Live.CPUMilli != 4000 {
		t.Fatalf("secret edit invalidated live capture: %+v Live=%+v err=%v", row.Service, row.Live, err)
	}
	spec.CPUs = 1
	response, e := setService(r, spec)
	if e != nil || response.Service.CPUMilli != 4000 {
		t.Fatalf("shrink dropped the started unit's hold: %+v, %v", response.Service, e)
	}
}
