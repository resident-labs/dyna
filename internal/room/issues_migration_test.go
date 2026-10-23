package room

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/database"
)

func TestIssueUpgradePreservesCurrentRowsAndSourceOrder(t *testing.T) {
	dir := t.TempDir()
	s := &Store{Root: filepath.Join(dir, "rooms")}
	const roomID = "room_20261022-120000-1234abcd"
	path := filepath.Join(s.Root, roomID, IssuesFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close(dir) })
	const ddl = `CREATE TABLE room_issues(seq INTEGER PRIMARY KEY AUTOINCREMENT, room_id TEXT NOT NULL, id TEXT NOT NULL, state TEXT NOT NULL, body TEXT NOT NULL CHECK(json_valid(body)), UNIQUE(room_id,id)); CREATE INDEX room_issues_state ON room_issues(room_id,state,seq); CREATE TABLE room_issue_revisions(room_id TEXT PRIMARY KEY, revision INTEGER NOT NULL); INSERT INTO meta(key,value) VALUES('room.issues.schema','1');`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	current := Issue{ID: "current", Title: "canonical", State: "closed", Reason: "done"}
	body, _ := json.Marshal(current)
	if _, err := db.Exec("INSERT INTO room_issues(seq,room_id,id,state,body) VALUES(7,?,?,?,?)", roomID, current.ID, current.State, string(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO room_issue_revisions VALUES(?,9)", roomID); err != nil {
		t.Fatal(err)
	}
	source := []byte(`[{"id":"current","title":"stale","state":"open"},{"id":"newer","state":"open"},{"id":"older","state":"open"},{"id":"current","state":"open"}]`)
	write := func(raw []byte) {
		t.Helper()
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	checkSource := func(want []byte) {
		t.Helper()
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("source changed: %q, %v", got, err)
		}
	}
	checkMeta := func(wantVersion string, wantRevision int64) {
		t.Helper()
		var version string
		var revision int64
		if err := db.QueryRow("SELECT value FROM meta WHERE key='room.issues.schema'").Scan(&version); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRow("SELECT revision FROM room_issue_revisions WHERE room_id=?", roomID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if version != wantVersion || revision != wantRevision {
			t.Fatalf("version/revision = %s/%d, want %s/%d", version, revision, wantVersion, wantRevision)
		}
	}
	write(source)
	if _, err := s.issuesDB(); err == nil {
		t.Fatal("duplicate source ID accepted")
	}
	checkMeta("1", 9)
	var count int
	var gotBody string
	if err := db.QueryRow("SELECT COUNT(*) FROM room_issues").Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed upgrade added rows: %d, %v", count, err)
	}
	if err := db.QueryRow("SELECT body FROM room_issues WHERE id='current'").Scan(&gotBody); err != nil || gotBody != string(body) {
		t.Fatalf("canonical body changed: %q, %v", gotBody, err)
	}
	checkSource(source)
	source = []byte(`[{"id":"current","title":"stale","state":"open"},{"id":"newer","state":"open"},{"id":"older","state":"open"}]`)
	write(source)
	if _, err := s.issuesDB(); err != nil {
		t.Fatal(err)
	}
	checkMeta("3", 11)
	got, err := s.Issues(roomID, true)
	current.Number = 3
	want := []Issue{current, {ID: "newer", Number: 2, State: "open"}, {ID: "older", Number: 1, State: "open"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("issues = %+v, %v", got, err)
	}
	checkSource(source)
	source = []byte(`[{"id":"late","state":"open"}]`)
	write(source)
	if _, err := s.issuesDB(); err != nil {
		t.Fatal(err)
	}
	checkMeta("3", 11)
	got, err = s.Issues(roomID, true)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("source imported again: %+v, %v", got, err)
	}
	checkSource(source)
}
