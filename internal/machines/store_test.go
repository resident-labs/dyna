package machines

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"reflect"
	"strings"
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

	// Every SSH node becomes its own machine; a local node ran on this
	// machine, whose row already takes that work.
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
		if !local.Local || local.SSH != "" || !local.Enabled {
			t.Fatalf("local machine = %#v", local)
		}
		// The enabled local node kept its CI work on this machine, so the
		// engine host row carries the ci label.
		if !local.HasLabel(LabelCI) {
			t.Fatalf("engine host lost the local node's CI work: %#v", local)
		}
		// An old CI node keeps taking local CI, so its label says so.
		want := Machine{Name: "hermes", SSH: "pip@hermeslab", Enabled: true, Labels: []string{LabelCI}, Health: HealthUnknown}
		if got := list[1]; !sameMachine(got, want) {
			t.Fatalf("imported machine = %#v, want %#v", got, want)
		}
	})

	// A disabled local node changes nothing on the engine host row, whose own
	// switch says whether its work runs.
	t.Run("disabled local node", func(t *testing.T) {
		s := seed(t, `
INSERT INTO local_ci_nodes(name,ssh,slots,enabled) VALUES ('local','',3,0);`)
		list, err := s.List(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("machines = %#v, %v", list, err)
		}
		if !list[0].Local {
			t.Fatalf("local machine = %#v", list[0])
		}
		// A disabled local node opted out of CI, so the row stays unlabeled.
		if list[0].HasLabel(LabelCI) {
			t.Fatalf("engine host took CI from a disabled local node: %#v", list[0])
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
		{"remote", Machine{Name: "hermes", SSH: "pip@hermeslab", Enabled: true}, true},
		{"local", Machine{Name: "here", Local: true}, true},
		{"empty name", Machine{Name: "", SSH: "host"}, false},
		{"option name", Machine{Name: "-option", SSH: "host"}, false},
		{"local with ssh", Machine{Name: "here", SSH: "host", Local: true}, false},
		{"remote without ssh", Machine{Name: "hermes"}, false},
		{"ssh shell syntax", Machine{Name: "hermes", SSH: "user@host; id"}, false},
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
	saved, err := s.Save(ctx, Machine{Name: "hermes", SSH: "pip@hermeslab", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if saved.HasHostChecksGrants() {
		t.Fatal("host checks were allowed by default")
	}
	saved.Name, saved.Enabled = "buildhost", false
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
	stored, err := s.Get(ctx, "buildhost")
	if err != nil || stored.Enabled || !stored.HostChecksAllowed("") {
		t.Fatalf("stored machine = %#v, %v", stored, err)
	}
	if _, err := s.Save(ctx, Machine{Name: "buildhost", SSH: "host"}); !errors.Is(err, os.ErrExist) {
		t.Fatalf("a new machine took a stored name: %v", err)
	}
	if err := s.Remove(ctx, "buildhost"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "buildhost"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed machine remains: %v", err)
	}
	local, err := s.GetLocal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, local.Name); !errors.Is(err, ErrLocal) {
		t.Fatalf("removed this machine: %v", err)
	}
}

// A grant change reads the stored list inside the writer transaction, so a
// change that lands after another caller's refusal cannot restore what that
// caller removed.
func TestHostChecksGrantReadsTheStoredList(t *testing.T) {
	ctx := context.Background()
	s := fixture(t)
	pve, err := s.Save(ctx, Machine{Name: "pve", SSH: "pip@pve", Enabled: true,
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
	pve, err := s.Save(ctx, Machine{Name: "pve", SSH: "pip@pve", Enabled: true,
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
	if !m.HostChecksAllowed("") || m.SSH != "pip@hermeslab" {
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

// A version 5 registry had no suite filter, so migration leaves it unrestricted.
func TestMigrateKeepsVersionFiveMachineUnrestricted(t *testing.T) {
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
	for _, label := range m.Labels {
		if strings.HasPrefix(label, "suite=") {
			t.Fatalf("an unrestricted machine gained a suite filter: %v", m.Labels)
		}
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
	if m.Health != HealthUnknown || m.SSH != "pip@hermeslab" || !m.Enabled {
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

// machinesVersionThirteenSchema is the registry before a node lost its roles
// and CI slots, so a data folder from that version still migrates.
const machinesVersionThirteenSchema = `CREATE TABLE machines (
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
	last_probe_ms  INTEGER NOT NULL DEFAULT 0,
	last_seen_ms   INTEGER NOT NULL DEFAULT 0,
	probe_error    TEXT    NOT NULL DEFAULT '',
	last_load      TEXT    NOT NULL DEFAULT '{}' CHECK(json_valid(last_load)),
	host_checks_grants   TEXT NOT NULL DEFAULT '[]' CHECK(json_valid(host_checks_grants)),
	rooms          TEXT    NOT NULL DEFAULT '[]' CHECK(json_valid(rooms)),
	suites         TEXT    NOT NULL DEFAULT '[]' CHECK(json_valid(suites)),
	container_socket TEXT  NOT NULL DEFAULT '',
	ssh_connect_timeout_ms INTEGER NOT NULL DEFAULT 0 CHECK(ssh_connect_timeout_ms >= 0),
	check_tmp TEXT NOT NULL DEFAULT '',
	max_io_heavy INTEGER NOT NULL DEFAULT 0 CHECK(max_io_heavy >= 0),
	max_disk_percent INTEGER NOT NULL DEFAULT 0 CHECK(max_disk_percent BETWEEN 0 AND 100),
	resources TEXT NOT NULL DEFAULT '{}' CHECK(json_valid(resources))
);
CREATE UNIQUE INDEX machines_one_local ON machines(local) WHERE local = 1;
INSERT INTO machines(id,name,roles,ssh,ci_slots,enabled,local,created_ms,updated_ms,rooms) VALUES
 ('m_old','hermes','["ci"]','pip@hermeslab',2,1,0,1000,1000,'["room_dyna"]'),
 ('m_desktop','desktop','["engine","agents"]','',0,1,1,1000,1000,'[]');
INSERT INTO meta(key,value) VALUES('machines.version','13');`

// A version 13 registry drops its roles and CI slots and keeps every machine:
// the row's switch and room list alone decide what it takes.
func TestMigrateDropsRolesAndSlots(t *testing.T) {
	dir := storetest.DataDir(t)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	if _, err := db.Exec(machinesVersionThirteenSchema); err != nil {
		t.Fatal(err)
	}

	m, err := Store{DataDir: dir}.Get(context.Background(), "hermes")
	if err != nil {
		t.Fatal(err)
	}
	if m.SSH != "pip@hermeslab" || !m.Enabled || !m.AllowsRoom("room_dyna") || m.AllowsRoom("room_other") {
		t.Fatalf("migrated machine = %#v", m)
	}
	for _, column := range []string{"roles", "ci_slots"} {
		var name string
		if err := db.QueryRow("SELECT name FROM pragma_table_info('machines') WHERE name=?", column).Scan(&name); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("machines.%s remains: %v", column, err)
		}
	}
	// The ci role becomes the ci label, so the engine host that never had the
	// role stays out of local CI after the migration, and the machine that had
	// it keeps taking local CI.
	if !m.HasLabel(LabelCI) {
		t.Fatalf("former ci machine lost its ci label: %#v", m)
	}
	desktop, err := Store{DataDir: dir}.Get(context.Background(), "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if desktop.HasLabel(LabelCI) {
		t.Fatalf("engine host without the old ci role carries the ci label: %#v", desktop)
	}
}

func TestMigrateImportsSuiteFiltersAsOwnerLabels(t *testing.T) {
	dir := storetest.DataDir(t)
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	version14 := strings.Replace(schema, "	labels         TEXT", `	suites         TEXT    NOT NULL DEFAULT '[]' CHECK(json_valid(suites)),
	labels         TEXT`, 1)
	if version14 == schema {
		t.Fatal("the version 14 fixture could not add its suites column")
	}
	_, err = db.Exec(version14 + `
INSERT INTO machines(id,name,ssh,enabled,local,created_ms,updated_ms,labels,suites) VALUES
 ('m_limited','limited','pip@limited',1,0,1000,1000,'["ci","suite=go"]','["go","frontend"]'),
 ('m_every','every','pip@every',1,0,1000,1000,'[]','[]');
INSERT INTO meta(key,value) VALUES('machines.version','14');`)
	if err != nil {
		t.Fatal(err)
	}

	store := Store{DataDir: dir}
	limited, err := store.Get(context.Background(), "limited")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ci", "suite=go", "suite=frontend"}
	if !reflect.DeepEqual(limited.Labels, want) {
		t.Fatalf("imported labels = %v, want %v", limited.Labels, want)
	}
	every, err := store.Get(context.Background(), "every")
	if err != nil || len(every.Labels) != 0 {
		t.Fatalf("an unrestricted machine gained labels: %#v, %v", every, err)
	}
	var column string
	if err := db.QueryRow("SELECT name FROM pragma_table_info('machines') WHERE name='suites'").Scan(&column); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("machines.suites remains: %v", err)
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
	added, err := s.Save(context.Background(), Machine{Name: "added", SSH: "ari@added", Enabled: true})
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

// A machine keeps the container runtime socket a workflow job uses, and one
// that names none uses Docker's, so a rootless Podman machine can be pointed at
// its own socket without changing any other machine.
func TestMachineContainerSocketRoundTripsAndDefaultsToDocker(t *testing.T) {
	ctx := context.Background()
	store := fixture(t)
	saved, err := store.Save(ctx, Machine{Name: "pve", Enabled: true, SSH: "build@pve", ContainerSocket: "unix:///run/user/1000/podman/podman.sock"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var read Machine
	for _, machine := range list {
		if machine.ID == saved.ID {
			read = machine
		}
	}
	if read.ContainerSocket != "unix:///run/user/1000/podman/podman.sock" || read.DockerHost() != read.ContainerSocket {
		t.Fatalf("socket = %q, DOCKER_HOST = %q", read.ContainerSocket, read.DockerHost())
	}
	plain, err := store.Save(ctx, Machine{Name: "plain", Enabled: true, SSH: "build@plain"})
	if err != nil {
		t.Fatal(err)
	}
	if plain.DockerHost() != DefaultContainerSocket {
		t.Fatalf("a machine without a socket uses %q, want %q", plain.DockerHost(), DefaultContainerSocket)
	}
}
