package store

import (
	"database/sql"
	_ "embed"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestUpgradeRunsOneStep(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key,value) VALUES ('schema_version', ?)", strconv.Itoa(schemaVersion-1)); err != nil {
		t.Fatal(err)
	}
	stepsForTest := map[int][]string{schemaVersion - 1: {"CREATE TABLE probe (id INTEGER PRIMARY KEY)"}}
	if err := upgrade(db, 1, "", stepsForTest); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = 'schema_version'").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema_version = %s, want %d", value, schemaVersion)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM probe").Scan(&count); err != nil {
		t.Fatal(err)
	}
}

// floorSchema is the schema a store holds at the floor, as sqlite3 .schema printed it on the live store, without FTS's
// own tables, which the virtual tables make. It moves with the floor.
//
//go:embed testdata/floor.sql
var floorSchema string

// floorStore writes a store at the floor into a new folder, with what setup adds to it.
func floorStore(t *testing.T, setup func(*sql.DB)) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(floorSchema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key,value) VALUES ('format','dyna-v2'),('schema_version',?),('store_id','floor'),('created_commit','')", strconv.Itoa(floorVersion)); err != nil {
		t.Fatal(err)
	}
	if setup != nil {
		setup(db)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestUpgradeFromTheFloor opens a store at the floor: every step from there runs, and the result is schema.sql.
func TestUpgradeFromTheFloor(t *testing.T) {
	s, err := Open(floorStore(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var value string
	if err := s.Read().QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&value); err != nil || value != strconv.Itoa(schemaVersion) {
		t.Fatalf("schema_version = %s, want %d: %v", value, schemaVersion, err)
	}
}

// TestStepsStartAtTheFloor holds the steps to the versions a store can still be at.
func TestStepsStartAtTheFloor(t *testing.T) {
	if floorVersion > schemaVersion {
		t.Fatalf("floor %d is above schema %d", floorVersion, schemaVersion)
	}
	if want := schemaVersion - floorVersion; len(steps) != want {
		t.Errorf("%d steps, want one for each of the %d versions from the floor", len(steps), want)
	}
	for version := floorVersion; version < schemaVersion; version++ {
		if _, ok := steps[version]; !ok {
			t.Errorf("no step from version %d", version)
		}
	}
}

// TestUpgradeStripsShellJobExitCode leaves every other byte of a job's body alone, and every other event's exitCode.
func TestUpgradeStripsShellJobExitCode(t *testing.T) {
	tail := `"exitCode\": caf\u00e9 \u003cb\u003e \\ \n\u0000 ☕"`
	type row struct{ kind, before, after string }
	rows := []row{
		{"shell.job.ended", `{"end":{"kind":"exit","code":3},"session":"ses_a","job":"job_1","exitCode":3,"tail":` + tail + `}`, `{"end":{"kind":"exit","code":3},"session":"ses_a","job":"job_1","tail":` + tail + `}`},
		{"shell.job.ended", `{"exitCode":-1,"end":{"kind":"stopped"},"session":"ses_a","job":"job_2","tail":""}`, `{"end":{"kind":"stopped"},"session":"ses_a","job":"job_2","tail":""}`},
		{"shell.job.ended", `{"end":{"kind":"exit"},"session":"ses_a","job":"job_3","tail":""}`, `{"end":{"kind":"exit"},"session":"ses_a","job":"job_3","tail":""}`},
		{"hook.finished", `{"subscription":"sub_a","result":"failed","exitCode":2}`, `{"subscription":"sub_a","result":"failed","exitCode":2}`},
		{"op.ran", `{"id":"op_a","state":"done","result":{"exitCode":0}}`, `{"id":"op_a","state":"done","result":{"exitCode":0}}`},
	}
	s, err := Open(floorStore(t, func(db *sql.DB) {
		for i, r := range rows {
			if _, err := db.Exec("INSERT INTO events(seq,id,at,type,from_kind,from_id,body) VALUES (?,?,1,?,'engine','',?)", i+1, strconv.Itoa(i), r.kind, []byte(r.before)); err != nil {
				t.Fatal(err)
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, r := range rows {
		var body []byte
		var storage string
		if err := s.Read().QueryRow("SELECT body,typeof(body) FROM events WHERE seq=?", i+1).Scan(&body, &storage); err != nil {
			t.Fatal(err)
		}
		if string(body) != r.after || storage != "blob" {
			t.Errorf("%d: got %s as %s, want %s", i, body, storage, r.after)
		}
		if _, err := contract.DecodeEventBody(contract.EventType(r.kind), body); err != nil {
			t.Errorf("%d: %v", i, err)
		}
	}
}

// TestUpgradeCarriesRootApprovesHostexec turns a current store back into a version 86 one, with one room that let its root
// approve host exec and one that did not. The upgrade keeps the first as the hostexec kind and drops the old column.
func TestUpgradeCarriesRootApprovesHostexec(t *testing.T) {
	dir := floorStore(t, nil)
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"ALTER TABLE turns DROP COLUMN active_at",
		"ALTER TABLE rooms DROP COLUMN root_approves",
		"ALTER TABLE rooms ADD COLUMN root_approves_hostexec INTEGER NOT NULL DEFAULT 0",
		"INSERT INTO rooms(id,name,created_at,cpus,mem_bytes,root_approves_hostexec) VALUES ('room_on','on',1,0,0,1),('room_off','off',1,0,0,0)",
		"UPDATE meta SET value='86' WHERE key='schema_version'",
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, want := range map[string]string{"room_on": `["hostexec"]`, "room_off": `[]`} {
		var got string
		if err := s.Read().QueryRow("SELECT root_approves FROM rooms WHERE id=?", id).Scan(&got); err != nil || got != want {
			t.Errorf("%s: root_approves = %q, want %s: %v", id, got, want, err)
		}
	}
	if _, err := s.Read().Exec("SELECT root_approves_hostexec FROM rooms"); err == nil {
		t.Error("root_approves_hostexec is still there")
	}
}
