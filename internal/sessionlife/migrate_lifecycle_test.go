package sessionlife

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestMigratePersistsOldDoneAsDormancy(t *testing.T) {
	data := t.TempDir()
	t.Cleanup(func() { _ = database.Close(data) })
	sessions := filepath.Join(data, "sessions")
	const stamp = "2026-10-01T00:00:00Z"
	for id, fields := range map[string]map[string]any{
		"ses_done":   {"name": "done", "done": true, "closed": stamp, "model": "kept"},
		"ses_closed": {"name": "closed", "closed": stamp, "done": false},
	} {
		fields["id"] = id
		if err := Save(sessions, id, fields); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string) (int, string, int, map[string]json.RawMessage) {
		t.Helper()
		var rev, closed int
		var status, raw string
		if err := db.QueryRow("SELECT revision,status,closed,meta FROM session_records WHERE id=?", id).Scan(&rev, &status, &closed, &raw); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			t.Fatal(err)
		}
		return rev, status, closed, fields
	}
	rev, status, closed, fields := read("ses_done")
	if status != "dormant" || closed != 0 || string(fields["finished"]) != `"`+stamp+`"` || fields["done"] != nil || fields["closed"] != nil || string(fields["model"]) != `"kept"` {
		t.Fatalf("stored dormant record: %s %d %s", status, closed, fields)
	}
	closedRev, status, closed, fields := read("ses_closed")
	if status != "closed" || fields["done"] != nil || closed != 1 || string(fields["closed"]) != `"`+stamp+`"` {
		t.Fatalf("closed record changed: %d %s", closed, fields)
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	if again, _, _, _ := read("ses_done"); again != rev {
		t.Fatal("replay changed dormant record")
	}
	if again, _, _, _ := read("ses_closed"); again != closedRev {
		t.Fatal("replay changed closed record")
	}
}
