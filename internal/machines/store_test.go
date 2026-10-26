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
	if saved.HasHostChecksGrants() {
		t.Fatal("host checks were allowed by default")
	}
	saved.Name, saved.Enabled = "hermeslab", false
	renamed, err := s.Save(ctx, saved)
	if err != nil {
		t.Fatal(err)
	}
	// The engine writes a grant, and the rename above keeps it.
	if _, err := s.UpdateHostChecksGrants(ctx, saved.ID, func(Machine) ([]HostChecksGrant, error) {
		return []HostChecksGrant{{By: GrantorOwner}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if renamed.ID != saved.ID || renamed.Created.IsZero() {
		t.Fatalf("rename lost the row identity: %#v", renamed)
	}
	if _, err := s.Get(ctx, "hermes"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old name remains: %v", err)
	}
	stored, err := s.Get(ctx, "hermeslab")
	if err != nil || stored.Enabled || stored.CISlots != 2 || !stored.HostChecksAllowed("") {
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
	local.Roles = append(local.Roles, RoleCI)
	local.CISlots = 1
	local.HostChecksGrants = []HostChecksGrant{{By: GrantorOwner}}
	local.Rooms, local.Suites = []string{"room_dyna"}, []string{"go"}
	if _, err := s.Save(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRole(ctx, local.Name, RoleCI); err != nil {
		t.Fatal(err)
	}
	local, err = s.GetLocal(ctx)
	if err != nil || local.HasHostChecksGrants() {
		t.Fatalf("removing the CI role kept host permission: %#v, %v", local, err)
	}
	// The limits only the ci role uses go with it.
	if len(local.Rooms) != 0 || len(local.Suites) != 0 {
		t.Fatalf("removing the CI role kept rooms %v and suites %v", local.Rooms, local.Suites)
	}
	local.Roles = []Role{RoleAgents}
	if _, err := s.Save(ctx, local); !errors.Is(err, ErrLocal) {
		t.Fatalf("dropped the engine role from this machine: %v", err)
	}
}

// A grant change reads the stored list inside the writer transaction, so a
// change that lands after another caller's refusal cannot restore what that
// caller removed.
func TestHostChecksGrantReadsTheStoredList(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	pve, err := s.Save(ctx, Machine{Name: "pve", Roles: []Role{RoleCI}, SSH: "pip@pve", CISlots: 2, Enabled: true,
		HostChecksGrants: []HostChecksGrant{{By: GrantorOwner}}})
	if err != nil {
		t.Fatal(err)
	}
	// A room root took the list before the owner refused. Its allow then runs.
	if _, err := s.UpdateHostChecksGrants(ctx, pve.ID, func(Machine) ([]HostChecksGrant, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := s.UpdateHostChecksGrants(ctx, pve.ID, func(m Machine) ([]HostChecksGrant, error) {
		if len(m.HostChecksGrants) != 0 {
			t.Fatalf("change read grants %#v after the owner refused them", m.HostChecksGrants)
		}
		return append(m.HostChecksGrants, HostChecksGrant{By: GrantorRoot, Room: "room_a"}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stored.HostChecksAllowed("room_a") || stored.HostChecksAllowed("room_b") || len(stored.HostChecksGrants) != 1 {
		t.Fatalf("stored grants = %#v, want the root's room alone", stored.HostChecksGrants)
	}
}

// A machine edit keeps who allows host checks, so a save from a reader that took
// the row before another caller's refusal cannot restore the old grant.
func TestRegistryEditKeepsHostChecksGrants(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	pve, err := s.Save(ctx, Machine{Name: "pve", Roles: []Role{RoleCI}, SSH: "pip@pve", CISlots: 2, Enabled: true,
		HostChecksGrants: []HostChecksGrant{{By: GrantorOwner}}})
	if err != nil {
		t.Fatal(err)
	}
	stale := pve
	if _, err := s.UpdateHostChecksGrants(ctx, pve.ID, func(Machine) ([]HostChecksGrant, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	stale.Name, stale.SSH = "pve2", "pip@pve2"
	if _, err := s.Save(ctx, stale); err != nil {
		t.Fatal(err)
	}
	edited, err := s.Get(ctx, "pve2")
	if err != nil {
		t.Fatal(err)
	}
	if edited.HasHostChecksGrants() {
		t.Fatalf("edited machine = %#v, want the refused grants left off", edited.HostChecksGrants)
	}
}

// TestMigrateKeepsAnEarlierHostCheckGrant checks the version 4 data folder that
// kept host checks as one switch: a machine that allows them becomes the
// owner's grant for every room, and a machine that refuses them keeps none.
func TestMigrateKeepsAnEarlierHostCheckGrant(t *testing.T) {
	ctx := context.Background()
	dir := storetest.DataDir(t)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(machinesVersionFourSchema); err != nil {
		t.Fatal(err)
	}

	s := Store{DataDir: dir}
	m, err := s.Get(ctx, "pve")
	if err != nil {
		t.Fatal(err)
	}
	if !m.HostChecksAllowed("") || !m.HostChecksAllowed("room_other") {
		t.Fatalf("migrated machine = %#v, want the owner's grant for every room", m)
	}
	refused, err := s.Get(ctx, "quiet")
	if err != nil {
		t.Fatal(err)
	}
	if refused.HasHostChecksGrants() {
		t.Fatalf("migrated machine = %#v, want the refused permission kept off", refused)
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

// machinesVersionFourSchema is the registry with probes and the host-check
// permission as one switch, before a machine recorded who allows host checks or
// named rooms, so a data folder from that version still migrates.
const machinesVersionFourSchema = `CREATE TABLE machines (
	id             TEXT    PRIMARY KEY,
	name           TEXT    NOT NULL UNIQUE,
	roles          TEXT    NOT NULL CHECK(json_valid(roles)),
	ssh            TEXT    NOT NULL,
	ci_slots       INTEGER NOT NULL CHECK(ci_slots >= 0),
	enabled        INTEGER NOT NULL CHECK(enabled IN (0,1)),
	local          INTEGER NOT NULL CHECK(local IN (0,1)),
	created_ms     INTEGER NOT NULL,
	updated_ms     INTEGER NOT NULL,
	capabilities   TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(capabilities)),
	health         TEXT    NOT NULL DEFAULT 'unknown',
	health_reason  TEXT    NOT NULL DEFAULT '',
	last_probe_ms  INTEGER NOT NULL DEFAULT 0,
	last_seen_ms   INTEGER NOT NULL DEFAULT 0,
	probe_error    TEXT    NOT NULL DEFAULT '',
	last_load      TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(last_load)),
	allow_host_checks INTEGER NOT NULL DEFAULT 0 CHECK(allow_host_checks IN (0,1))
);
CREATE UNIQUE INDEX machines_one_local ON machines(local) WHERE local = 1;
INSERT INTO machines(id,name,roles,ssh,ci_slots,enabled,local,created_ms,updated_ms,allow_host_checks) VALUES
 ('m_old','hermes','["ci"]','pip@hermeslab',2,1,0,1000,1000,1),
 ('m_grant','pve','["ci"]','pip@pve',2,1,0,1000,1000,1),
 ('m_quiet','quiet','["ci"]','pip@quiet',1,1,0,1000,1000,0);
INSERT INTO meta(key,value) VALUES('machines.version','4');`

// A version 4 registry reaches the version 6 step in one start: it gains the
// room list and the owner's grant for the permission that was on, and it keeps
// its route and slots.
func TestMigrateAddsRoomListToVersionFour(t *testing.T) {
	ctx := context.Background()
	dir := storetest.DataDir(t)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(machinesVersionFourSchema); err != nil {
		t.Fatal(err)
	}

	m, err := Store{DataDir: dir}.Get(ctx, "hermes")
	if err != nil {
		t.Fatal(err)
	}
	if !m.HostChecksAllowed("") || m.SSH != "pip@hermeslab" || m.CISlots != 2 {
		t.Fatalf("migrated machine = %#v", m)
	}
	// The machine named no room before it could, so it takes every room.
	if len(m.Rooms) != 0 || !m.AllowsRoom("room_any") {
		t.Fatalf("migrated machine rooms = %v", m.Rooms)
	}
}

// machinesVersionFiveSchema is the registry with a room list but no suite list,
// so a data folder from that version still migrates.
const machinesVersionFiveSchema = `CREATE TABLE machines (
	id             TEXT    PRIMARY KEY,
	name           TEXT    NOT NULL UNIQUE,
	roles          TEXT    NOT NULL CHECK(json_valid(roles)),
	ssh            TEXT    NOT NULL,
	ci_slots       INTEGER NOT NULL CHECK(ci_slots >= 0),
	enabled        INTEGER NOT NULL CHECK(enabled IN (0,1)),
	local          INTEGER NOT NULL CHECK(local IN (0,1)),
	created_ms     INTEGER NOT NULL,
	updated_ms     INTEGER NOT NULL,
	capabilities   TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(capabilities)),
	health         TEXT    NOT NULL DEFAULT 'unknown',
	health_reason  TEXT    NOT NULL DEFAULT '',
	last_probe_ms  INTEGER NOT NULL DEFAULT 0,
	last_seen_ms   INTEGER NOT NULL DEFAULT 0,
	probe_error    TEXT    NOT NULL DEFAULT '',
	last_load      TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(last_load)),
	allow_host_checks INTEGER NOT NULL DEFAULT 0 CHECK(allow_host_checks IN (0,1)),
	rooms          TEXT    NOT NULL DEFAULT '[]' CHECK(json_valid(rooms))
);
CREATE UNIQUE INDEX machines_one_local ON machines(local) WHERE local = 1;
INSERT INTO machines(id,name,roles,ssh,ci_slots,enabled,local,created_ms,updated_ms,allow_host_checks,rooms) VALUES
 ('m_old','hermes','["ci"]','pip@hermeslab',2,1,0,1000,1000,1,'["room_dyna"]');
INSERT INTO meta(key,value) VALUES('machines.version','5');`

// A version 5 registry reaches the version 6 step, which adds only the suite
// list. A machine that named no suite before it could allows every suite.
func TestMigrateAddsSuiteListToVersionFive(t *testing.T) {
	ctx := context.Background()
	dir := storetest.DataDir(t)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(machinesVersionFiveSchema); err != nil {
		t.Fatal(err)
	}

	m, err := Store{DataDir: dir}.Get(ctx, "hermes")
	if err != nil {
		t.Fatal(err)
	}
	if !m.AllowsRoom("room_dyna") || m.AllowsRoom("room_other") {
		t.Fatalf("migrated machine rooms = %v", m.Rooms)
	}
	if len(m.Suites) != 0 || !m.AllowsSuite("go") {
		t.Fatalf("migrated machine suites = %v", m.Suites)
	}
}

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

func TestMigrateInheritsHostCheckPermissionFromComputerSmoke(t *testing.T) {
	dir := storetest.DataDir(t)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	_, err = db.Exec(machinesVersionOneSchema + `
INSERT INTO machines(id,name,roles,ssh,ci_slots,enabled,local,created_ms,updated_ms) VALUES
 ('m_other','other','["ci"]','ari@other',1,1,0,1000,1000),
 ('m_setup','setup','["ci"]','ari@setup',1,1,0,1000,1000);
CREATE TABLE local_ci_results(repo TEXT, sha TEXT, payload BLOB NOT NULL);
INSERT INTO local_ci_results VALUES('repo','abc','{"node":"hermes","sha":"abc","status":"passed","suites":[{"suite":"computer-smoke","node":"hermes","status":"passed"}]}');
INSERT INTO local_ci_results VALUES('repo','def','{"node":"setup","sha":"def","status":"error","suites":[{"suite":"computer-smoke","node":"setup","status":"error","cause":"container-failure"}]}');`)
	if err != nil {
		t.Fatal(err)
	}

	s := Store{DataDir: dir}
	hermes, err := s.Get(context.Background(), "hermes")
	if err != nil {
		t.Fatal(err)
	}
	if !hermes.HostChecksAllowed("") {
		t.Fatal("migration did not preserve an existing computer-smoke machine's host permission")
	}
	other, err := s.Get(context.Background(), "other")
	if err != nil || other.HasHostChecksGrants() {
		t.Fatalf("unrelated machine gained host permission: %#v, %v", other, err)
	}
	setup, err := s.Get(context.Background(), "setup")
	if err != nil || setup.HasHostChecksGrants() {
		t.Fatalf("failed setup granted host permission: %#v, %v", setup, err)
	}
	added, err := s.Save(context.Background(), Machine{Name: "added", Roles: []Role{RoleCI}, SSH: "ari@added", CISlots: 1, Enabled: true})
	if err != nil || added.HasHostChecksGrants() {
		t.Fatalf("new machine gained host permission: %#v, %v", added, err)
	}
	if _, err := s.UpdateHostChecksGrants(context.Background(), hermes.ID, func(Machine) ([]HostChecksGrant, error) {
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	hermes, err = s.Get(context.Background(), "hermes")
	if err != nil || hermes.HasHostChecksGrants() {
		t.Fatalf("migration re-enabled an explicit owner decision: %#v, %v", hermes, err)
	}
}
