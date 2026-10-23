package sessionlife

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestMigratePersistsHistoricalSessionNameOnce(t *testing.T) {
	data := t.TempDir()
	t.Cleanup(func() { _ = database.Close(data) })
	sessions := filepath.Join(data, "sessions")
	unnamed := "ses_20261001-120000-12345678"
	if err := Save(sessions, unnamed, map[string]any{"id": unnamed, "status": "idle", "model": "m"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(sessions, "ses_named", map[string]any{"id": "ses_named", "name": "kept"}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	read := func(id string) (name string, revision int, fields map[string]json.RawMessage) {
		t.Helper()
		var raw string
		if err := db.QueryRow("SELECT name, revision, meta FROM session_records WHERE id=?", id).Scan(&name, &revision, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			t.Fatal(err)
		}
		return
	}
	name, rev, fields := read(unnamed)
	if name != "agent-12345678" || string(fields["name"]) != `"agent-12345678"` || string(fields["model"]) != `"m"` {
		t.Fatalf("unnamed record = column %q, meta %v", name, fields)
	}
	kept, keptRev, _ := read("ses_named")
	if kept != "kept" {
		t.Fatalf("named record = %q", kept)
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	if _, again, _ := read(unnamed); again != rev {
		t.Fatalf("replay changed revision %d to %d", rev, again)
	}
	if _, again, _ := read("ses_named"); again != keptRev {
		t.Fatalf("named revision changed %d to %d", keptRev, again)
	}
}
