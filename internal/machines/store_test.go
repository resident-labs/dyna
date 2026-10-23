package machines

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store/storetest"
)

// oldNodesSchema is the local CI node table this migration replaces. A data
// folder from before the machines registry still holds it.
const oldNodesSchema = `CREATE TABLE local_ci_nodes (
 name TEXT PRIMARY KEY,
 ssh TEXT NOT NULL,
 slots INTEGER NOT NULL CHECK(slots > 0),
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1))
);`

func fixture(t *testing.T) Store {
	t.Helper()
	s := Store{DataDir: storetest.DataDir(t)}
	t.Cleanup(func() { database.Close(s.DataDir) })
	return s
}

func TestMigrateImportsLocalCINodes(t *testing.T) {
	ctx := context.Background()

	// An enabled local node carries its CI slots into the local row, and every
	// SSH node becomes its own machine.
	t.Run("enabled local node", func(t *testing.T) {
		s := seed(t, `
INSERT INTO local_ci_nodes(name,ssh,slots,enabled) VALUES
 ('hermes','pip@hermeslab',2,1),
 ('local','',3,1);`)
		list, err := s.List(ctx)
		if err != nil || len(list) != 2 {
			t.Fatalf("machines = %#v, %v", list, err)
		}
		local := list[0]
		if !local.Local || !local.HasRole(RoleEngine) || !local.HasRole(RoleAgents) || !local.HasRole(RoleCI) {
			t.Fatalf("local machine = %#v", local)
		}
		if local.SSH != "" || local.CISlots != 3 || !local.Enabled {
			t.Fatalf("local machine did not take the old local node: %#v", local)
		}
		want := Machine{Name: "hermes", Roles: []Role{RoleCI}, SSH: "pip@hermeslab", CISlots: 2, Enabled: true, Health: HealthUnknown}
		if got := list[1]; !sameMachine(got, want) {
			t.Fatalf("imported machine = %#v, want %#v", got, want)
		}
	})

	// A disabled local node stays off: it gives the local row no ci role and no
	// slots, so the upgrade starts no CI the owner turned off.
	t.Run("disabled local node", func(t *testing.T) {
		s := seed(t, `
INSERT INTO local_ci_nodes(name,ssh,slots,enabled) VALUES ('local','',3,0);`)
		local, err := s.GetLocal(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if local.HasRole(RoleCI) || local.CISlots != 0 {
			t.Fatalf("disabled local node became CI work: %#v", local)
		}
	})
}

// seed builds a data folder that still holds the old node table, then migrates
// it the way an engine start does.
func seed(t *testing.T, rows string) Store {
	t.Helper()
	dir := storetest.DataDir(t)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(oldNodesSchema + rows); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	present, err := database.HasTable(context.Background(), connOf(t, db), "local_ci_nodes")
	if err != nil || present {
		t.Fatalf("local_ci_nodes remains: %v, %v", present, err)
	}
	return Store{DataDir: dir}
}

// connOf takes a connection for a table check.
func connOf(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// sameMachine compares the stored fields, not the generated ID and times.
func sameMachine(got, want Machine) bool {
	got.ID, got.Created, got.Updated, got.Local = "", want.Created, want.Updated, want.Local
	return reflect.DeepEqual(got, want)
}

func TestMachineValidateRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    Machine
		ok   bool
	}{
		{"remote ci", Machine{Name: "hermes", Roles: []Role{RoleCI}, SSH: "pip@hermeslab", CISlots: 2, Enabled: true}, true},
		{"remote agents", Machine{Name: "build", Roles: []Role{RoleAgents}, SSH: "ari@build"}, true},
		{"local engine", Machine{Name: "here", Roles: []Role{RoleEngine, RoleAgents, RoleCI}, CISlots: 1, Local: true}, true},
		{"empty name", Machine{Name: "", Roles: []Role{RoleCI}, SSH: "host", CISlots: 1}, false},
		{"option name", Machine{Name: "-option", Roles: []Role{RoleCI}, SSH: "host", CISlots: 1}, false},
		{"no roles", Machine{Name: "hermes", SSH: "host"}, false},
		{"unknown role", Machine{Name: "hermes", Roles: []Role{"computers"}, SSH: "host"}, false},
		{"engine off this machine", Machine{Name: "hermes", Roles: []Role{RoleEngine}, SSH: "host"}, false},
		{"local without engine", Machine{Name: "here", Roles: []Role{RoleAgents}, Local: true}, false},
		{"local with ssh", Machine{Name: "here", Roles: []Role{RoleEngine}, SSH: "host", Local: true}, false},
		{"ci without slots", Machine{Name: "hermes", Roles: []Role{RoleCI}, SSH: "host"}, false},
		{"remote without ssh", Machine{Name: "hermes", Roles: []Role{RoleAgents}}, false},
		{"ssh shell syntax", Machine{Name: "hermes", Roles: []Role{RoleCI}, SSH: "user@host; id", CISlots: 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.m.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate = %v, want ok %t", err, tc.ok)
			}
		})
	}
}

func TestStoreRenamesAndRemovesMachines(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	saved, err := s.Save(ctx, Machine{Name: "hermes", Roles: []Role{RoleCI}, SSH: "pip@hermeslab", CISlots: 2, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	saved.Name, saved.Enabled = "hermeslab", false
	renamed, err := s.Save(ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != saved.ID || renamed.Created.IsZero() {
		t.Fatalf("rename lost the row identity: %#v", renamed)
	}
	if _, err := s.Get(ctx, "hermes"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old name remains: %v", err)
	}
	stored, err := s.Get(ctx, "hermeslab")
	if err != nil || stored.Enabled || stored.CISlots != 2 {
		t.Fatalf("stored machine = %#v, %v", stored, err)
	}
	if _, err := s.Save(ctx, Machine{Name: "hermeslab", Roles: []Role{RoleCI}, SSH: "host", CISlots: 1}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("a new machine took a stored name: %v", err)
	}
	if err := s.RemoveRole(ctx, "hermeslab", RoleCI); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "hermeslab"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("machine with no roles remains: %v", err)
	}
	local, err := s.GetLocal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, local.Name); !errors.Is(err, ErrLocal) {
		t.Fatalf("removed this machine: %v", err)
	}
	local.Roles = []Role{RoleAgents}
	if _, err := s.Save(ctx, local); !errors.Is(err, ErrLocal) {
		t.Fatalf("dropped the engine role from this machine: %v", err)
	}
}

// machinesVersionOneSchema is the registry before the probes, so a data folder
// from that version still migrates.
const machinesVersionOneSchema = `CREATE TABLE machines (
	id         TEXT    PRIMARY KEY,
	name       TEXT    NOT NULL UNIQUE,
	roles      TEXT    NOT NULL CHECK(json_valid(roles)),
	ssh        TEXT    NOT NULL,
	ci_slots   INTEGER NOT NULL CHECK(ci_slots >= 0),
	enabled    INTEGER NOT NULL CHECK(enabled IN (0,1)),
	local      INTEGER NOT NULL CHECK(local IN (0,1)),
	created_ms INTEGER NOT NULL,
	updated_ms INTEGER NOT NULL
);
CREATE UNIQUE INDEX machines_one_local ON machines(local) WHERE local = 1;
INSERT INTO machines(id,name,roles,ssh,ci_slots,enabled,local,created_ms,updated_ms) VALUES
 ('m_old','hermes','["ci"]','pip@hermeslab',2,1,0,1000,1000);
INSERT INTO meta(key,value) VALUES('machines.version','1');`

func TestMigrateAddsProbeColumns(t *testing.T) {
	ctx := context.Background()
	dir := storetest.DataDir(t)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(machinesVersionOneSchema); err != nil {
		t.Fatal(err)
	}

	s := Store{DataDir: dir}
	m, err := s.Get(ctx, "hermes")
	if err != nil {
		t.Fatal(err)
	}
	if m.Health != HealthUnknown || m.SSH != "pip@hermeslab" || m.CISlots != 2 || !m.Enabled {
		t.Fatalf("migrated machine = %#v", m)
	}
	if !m.LastProbe.IsZero() || !m.LastSeen.IsZero() || m.Capabilities.CPUs != 0 {
		t.Fatalf("migrated machine has a probe: %#v", m)
	}
	if _, err := s.SaveProbe(ctx, m.ID, ProbeResult{Health: HealthOnline, Capabilities: Capabilities{OS: "Linux", CPUs: 4}, Load: Load{CPUs: 4}}); err != nil {
		t.Fatal(err)
	}
	stored, err := s.Get(ctx, "hermes")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status() != HealthOnline || stored.Capabilities.CPUs != 4 || stored.LastSeen.IsZero() {
		t.Fatalf("stored probe = %#v", stored)
	}
}
