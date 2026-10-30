package sessionlife

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func ownersStore(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Cleanup(func() { _ = database.Close(store.DataDir()) })
	return filepath.Join(store.DataDir(), "sessions")
}

// TestOwnersReadsTypedRows checks the projection against saved records: the
// columns must carry exactly what the meta blob says, services included.
func TestOwnersReadsTypedRows(t *testing.T) {
	sessions := ownersStore(t)
	closed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, record := range []map[string]any{
		{"id": "session-a", "name": "worker", "room": "dyna", "dir": "/trees/a"},
		{"id": "session-b", "dir": "/trees/b", "closed": closed},
		{"id": "svc", "dir": "/trees/a", "service": true},
		{"id": "session-c"},
	} {
		if err := Save(sessions, record["id"].(string), record); err != nil {
			t.Fatal(err)
		}
	}
	var got []OwnerRow
	if err := Owners(sessions, func(row OwnerRow) error {
		got = append(got, row)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []OwnerRow{
		{ID: "session-a", Room: "dyna", Name: "worker", Dir: "/trees/a"},
		{ID: "session-b", Dir: "/trees/b", Closed: true},
		{ID: "session-c"},
		{ID: "svc", Dir: "/trees/a", Service: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %#v; want %#v", got, want)
	}
}

// TestOwnersFailsWhenMetaDrifts keeps ownership fail-closed: a meta blob that
// changed after its columns were written, in any way, must be an error.
func TestOwnersFailsWhenMetaDrifts(t *testing.T) {
	for name, meta := range map[string]string{
		"syntax":        "{",
		"null":          "null",
		"other id":      `{"id":"other"}`,
		"wrong type":    `{"closed":true}`,
		"rewritten dir": `{"id":"owner","dir":"/trees/other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			sessions := ownersStore(t)
			if err := Save(sessions, "owner", map[string]any{"id": "owner", "dir": "/trees/a"}); err != nil {
				t.Fatal(err)
			}
			db, err := database.Open(filepath.Dir(sessions))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE session_records SET meta = ? WHERE id = ?", meta, "owner"); err != nil {
				t.Fatal(err)
			}
			if err := Owners(sessions, func(OwnerRow) error { return nil }); err == nil {
				t.Fatal("expected an error so ownership stays fail-closed")
			}
		})
	}
}

// TestRemapSpawnGoalsKeepsOwnersConsistent is a regression test: a goal
// fold once rewrote meta directly, so the record drifted from its digest and
// every later ownership scan failed closed, blocking worktree cleanup.
func TestRemapSpawnGoalsKeepsOwnersConsistent(t *testing.T) {
	sessions := ownersStore(t)
	if err := Save(sessions, "spawn", map[string]any{"id": "spawn", "dir": "/trees/a", "goal": "g1", "item": "i1"}); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Immediate(context.Background(), db, func(conn *sql.Conn) error {
		return RemapSpawnGoalsTx(conn, func(_, goal string) (string, string, bool) {
			return "g2", "i2", goal == "g1"
		})
	}); err != nil {
		t.Fatal(err)
	}
	var got []OwnerRow
	if err := Owners(sessions, func(row OwnerRow) error {
		got = append(got, row)
		return nil
	}); err != nil {
		t.Fatalf("ownership scan after goal fold: %v", err)
	}
	if len(got) != 1 || got[0].ID != "spawn" || got[0].Dir != "/trees/a" {
		t.Fatalf("rows after fold = %#v", got)
	}
	var meta map[string]json.RawMessage
	if err := Load(sessions, "spawn", &meta); err != nil {
		t.Fatal(err)
	}
	if string(meta["goal"]) != "\"g2\"" || string(meta["item"]) != "\"i2\"" {
		t.Fatalf("remapped fields = %s %s", meta["goal"], meta["item"])
	}
}

// v3Store builds a store whose session_records table predates the ownership
// columns, like every dyna.db created before schema version 4.
func v3Store(t *testing.T, rows ...[2]string) string {
	t.Helper()
	sessions := ownersStore(t)
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session_records (
  id TEXT PRIMARY KEY, revision INTEGER NOT NULL, room_id TEXT NOT NULL,
  room TEXT NOT NULL, name TEXT NOT NULL, status TEXT NOT NULL,
  closed INTEGER NOT NULL, parent TEXT NOT NULL, created_ns INTEGER NOT NULL,
  activity_ms INTEGER NOT NULL, history_only INTEGER NOT NULL DEFAULT 0,
  meta TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key, value) VALUES ('sessions.records', '3')"); err != nil {
		t.Fatal(err)
	}
	for i, row := range rows {
		if _, err := db.Exec("INSERT INTO session_records(id, revision, room_id, room, name, status, closed, parent, created_ns, activity_ms, history_only, meta) VALUES (?, ?, '', '', '', '', 0, '', 0, 0, 0, ?)", row[0], i+1, row[1]); err != nil {
			t.Fatal(err)
		}
	}
	return sessions
}

// TestOwnerColumnsImport checks the one-time import at version 4: existing
// rows gain ownership columns read from their meta, a damaged row keeps an
// empty digest and stays fail-closed, and a row that names another ID stays
// rejected.
func TestOwnerColumnsImport(t *testing.T) {
	closed := `{"id":"a","dir":"/trees/a","closed":"2026-01-01T00:00:00Z","name":"worker","room":"dyna"}`
	sessions := v3Store(t,
		[2]string{"a", closed},
		[2]string{"damaged", "{"},
		[2]string{"other", `{"id":"not-other","dir":"/trees/x"}`},
	)
	// The version-4 import runs inside records, before the record readers.
	if _, err := records(sessions); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	var dir string
	var service int
	var metaID, digest string
	if err := db.QueryRow("SELECT dir, service, meta_id, meta_digest FROM session_records WHERE id = 'a'").Scan(&dir, &service, &metaID, &digest); err != nil {
		t.Fatal(err)
	}
	if dir != "/trees/a" || service != 0 || metaID != "a" || digest == "" {
		t.Fatalf("imported columns = %q %d %q %q", dir, service, metaID, digest)
	}
	if err := db.QueryRow("SELECT meta_digest FROM session_records WHERE id = 'damaged'").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if digest != "" {
		t.Fatalf("damaged row imported a digest: %q", digest)
	}
	if err := Owners(sessions, func(OwnerRow) error { return nil }); err == nil {
		t.Fatal("expected the damaged row to fail Owners")
	}
	// Without the damaged row, the ID-mismatched row must still fail Owners.
	if _, err := db.Exec("DELETE FROM session_records WHERE id = 'damaged'"); err != nil {
		t.Fatal(err)
	}
	if err := Owners(sessions, func(OwnerRow) error { return nil }); err == nil {
		t.Fatal("expected the ID-mismatched row to fail Owners")
	}
}

// Raw edits must invalidate warm ownership evidence, and a reused revision
// after a raw delete must not reuse the old owner's directory.
func TestOwnersDetectsRawEditsAfterCacheWarm(t *testing.T) {
	sessions := ownersStore(t)
	if err := Save(sessions, "owner", map[string]any{"id": "owner", "dir": "/tree"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(sessions, "other", map[string]any{"id": "other"}); err != nil {
		t.Fatal(err)
	}
	visit := func(OwnerRow) error { return nil }
	if err := Owners(sessions, visit); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE session_records SET meta = ? WHERE id = 'owner'", `{"id":"owner","dir":"/other"}`); err != nil {
		t.Fatal(err)
	}
	if err := Owners(sessions, visit); err == nil {
		t.Fatal("raw edit of warm metadata must fail closed")
	}
	if _, err := db.Exec("DELETE FROM session_records"); err != nil {
		t.Fatal(err)
	}
	if err := Save(sessions, "owner", map[string]any{"id": "owner", "dir": "/old"}); err != nil {
		t.Fatal(err)
	}
	if err := Owners(sessions, visit); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM session_records WHERE id = 'owner'"); err != nil {
		t.Fatal(err)
	}
	if err := Save(sessions, "owner", map[string]any{"id": "owner", "dir": "/new"}); err != nil {
		t.Fatal(err)
	}
	if err := Owners(sessions, func(row OwnerRow) error {
		if row.Dir != "/new" {
			t.Fatalf("recreated owner has stale directory %q", row.Dir)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
