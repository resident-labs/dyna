package engine

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
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
