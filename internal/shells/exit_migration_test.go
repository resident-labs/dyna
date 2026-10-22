package shells

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestShellExitUpgradePreservesCanonicalExit(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Root: filepath.Join(dir, "shells")}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	const ddl = `CREATE TABLE shell_jobs (
 id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', cmd TEXT NOT NULL, cwd TEXT NOT NULL,
 owner TEXT NOT NULL DEFAULT '', keep INTEGER NOT NULL DEFAULT 0, room_id TEXT NOT NULL DEFAULT '', room TEXT NOT NULL DEFAULT '',
 pid INTEGER NOT NULL DEFAULT 0, supervisor INTEGER NOT NULL DEFAULT 0, supervisor_start INTEGER NOT NULL DEFAULT 0, started TEXT NOT NULL,
 ended TEXT, exit_code INTEGER NOT NULL DEFAULT 0, exit_signal TEXT NOT NULL DEFAULT '', exit_error TEXT NOT NULL DEFAULT '',
 read_offset INTEGER NOT NULL DEFAULT 0, waiter_until INTEGER NOT NULL DEFAULT 0, delivered INTEGER NOT NULL DEFAULT 0,
 background_at TEXT, stop_by TEXT NOT NULL DEFAULT '', output_lines INTEGER NOT NULL DEFAULT 0);
 CREATE INDEX shell_jobs_open ON shell_jobs(started) WHERE ended IS NULL;
 INSERT INTO meta(key,value) VALUES('shells.version','3');`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	const openID = "sh_20261022-120000-1234abcd"
	const doneID = "sh_20261022-120001-1234abcd"
	ended := time.Date(2026, 10, 22, 12, 2, 0, 0, time.UTC)
	canonical := Exit{Code: 9, Ended: ended, Signal: "canonical", Error: "keep"}
	imported := Exit{Code: 7, Ended: ended, Signal: "retained", Error: "import"}
	writeExit := func(id string, ex Exit) []byte {
		t.Helper()
		path := filepath.Join(s.Root, id, "exit.json")
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(ex)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, id := range []string{openID, doneID} {
		var end any
		code, signal, message := 0, "", ""
		if id == doneID {
			end = canonical.Ended.Format(time.RFC3339Nano)
			code = canonical.Code
			signal = canonical.Signal
			message = canonical.Error
		}
		_, err := db.Exec(`INSERT INTO shell_jobs(id,name,cmd,cwd,owner,keep,room_id,room,pid,supervisor,supervisor_start,started,ended,exit_code,exit_signal,exit_error,read_offset,waiter_until,delivered,background_at,stop_by,output_lines)
 VALUES(?,'canonical name','["true"]','/work','agent',1,'room_id','room',31,0,33,'2026-10-22T12:00:00Z',?,?,?,?,42,43,1,'2026-10-22T12:01:00Z','owner',44)`, id, end, code, signal, message)
		if err != nil {
			t.Fatal(err)
		}
		writeExit(id, imported)
	}
	unrelated := func(id string) []string {
		t.Helper()
		var values [18]string
		ptr := make([]any, len(values))
		for i := range values {
			ptr[i] = &values[i]
		}
		err := db.QueryRow("SELECT id,name,cmd,cwd,owner,keep,room_id,room,pid,supervisor,supervisor_start,started,read_offset,waiter_until,delivered,background_at,stop_by,output_lines FROM shell_jobs WHERE id=?", id).Scan(ptr...)
		if err != nil {
			t.Fatal(err)
		}
		return values[:]
	}
	before := map[string][]string{openID: unrelated(openID), doneID: unrelated(doneID)}
	check := func() {
		t.Helper()
		for id, want := range map[string]Exit{openID: imported, doneID: canonical} {
			ex, err := s.ReadExit(id)
			if err != nil || ex == nil || *ex != want {
				t.Fatalf("exit %s = %+v, %v; want %+v", id, ex, err, want)
			}
			info, err := s.Info(id)
			if err != nil || info.Status != "exited" || info.Exit == nil || *info.Exit != want {
				t.Fatalf("info %s = %+v, %v", id, info, err)
			}
			if !reflect.DeepEqual(unrelated(id), before[id]) {
				t.Fatalf("unrelated fields changed for %s", id)
			}
		}
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	check()
	for _, id := range []string{openID, doneID} {
		raw, err := os.ReadFile(filepath.Join(s.Root, id, "exit.json"))
		want, _ := json.Marshal(imported)
		if err != nil || !bytes.Equal(raw, want) {
			t.Fatalf("source changed: %q, %v", raw, err)
		}
	}
	changed := map[string][]byte{}
	for _, id := range []string{openID, doneID} {
		changed[id] = writeExit(id, Exit{Code: 88, Ended: ended})
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	check()
	for id, want := range changed {
		got, err := os.ReadFile(filepath.Join(s.Root, id, "exit.json"))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("source changed: %q, %v", got, err)
		}
	}
}
