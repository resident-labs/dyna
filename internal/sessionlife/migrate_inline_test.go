package sessionlife

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func v5Hash(t *testing.T, body string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

// v5Store builds by hand the store that schema version 5 left behind: hot
// rows with null instruction placeholders, hash columns, and one blob per
// distinct instruction value.
func v5Store(t *testing.T) string {
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
  meta TEXT NOT NULL, dir TEXT NOT NULL DEFAULT '', service INTEGER NOT NULL DEFAULT 0,
  meta_id TEXT NOT NULL DEFAULT '', meta_digest TEXT NOT NULL DEFAULT '',
  instructions_hash TEXT NOT NULL DEFAULT '', instructions_known_hash TEXT NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session_instruction_blobs (
  hash TEXT PRIMARY KEY, body TEXT NOT NULL CHECK(json_valid(body))) WITHOUT ROWID`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key, value) VALUES ('sessions.records', '5')"); err != nil {
		t.Fatal(err)
	}
	teach := `"teach these steps"`
	known := "[1,2]"
	fullA := `{"id":"a","dir":"/trees/a","instructions":"teach these steps","instructionsKnown":[1,2],"future":{"keep":[1,"two"]}}`
	hotA := `{"id":"a","dir":"/trees/a","instructions":null,"instructionsKnown":null,"future":{"keep":[1,"two"]}}`
	plainB := `{"id":"b","dir":"/trees/b"}`
	rows := []struct {
		id, revision, hot, metaID, digest, hash, knownHash string
	}{
		{"a", "41", hotA, "a", ownerDigest([]byte(fullA)), v5Hash(t, teach), v5Hash(t, known)},
		{"b", "7", plainB, "b", ownerDigest([]byte(plainB)), "", ""},
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO session_records(id, revision, room_id, room, name, status, closed, parent, created_ns, activity_ms, history_only, meta, dir, service, meta_id, meta_digest, instructions_hash, instructions_known_hash)
VALUES (?, ?, 'dyna', '', '', '', 0, '', 0, 0, 0, ?, '/trees', 0, ?, ?, ?, ?)`,
			r.id, r.revision, r.hot, r.metaID, r.digest, r.hash, r.knownHash); err != nil {
			t.Fatal(err)
		}
	}
	for _, blob := range [][2]string{{v5Hash(t, teach), teach}, {v5Hash(t, known), known}} {
		if _, err := db.Exec("INSERT INTO session_instruction_blobs(hash, body) VALUES (?, ?)", blob[0], blob[1]); err != nil {
			t.Fatal(err)
		}
	}
	return sessions
}

// TestMigrateRestoresInstructionBodiesInline checks the revert of the
// version 5 blob layout: migration re-joins every hot row with its blobs
// byte-for-byte, keeps revisions and unknown fields, and removes the blob
// table and hash columns. A store without the layout migrates untouched.
func TestMigrateRestoresInstructionBodiesInline(t *testing.T) {
	sessions := v5Store(t)
	if _, err := records(sessions); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"a": `{"id":"a","dir":"/trees/a","instructions":"teach these steps","instructionsKnown":[1,2],"future":{"keep":[1,"two"]}}`,
		"b": `{"id":"b","dir":"/trees/b"}`,
	}
	for id, full := range want {
		var meta string
		var kind string
		if err := db.QueryRow("SELECT meta, typeof(meta) FROM session_records WHERE id=?", id).Scan(&meta, &kind); err != nil {
			t.Fatal(err)
		}
		if meta != full {
			t.Fatalf("session %s meta = %s; want %s", id, meta, full)
		}
		if kind != "text" {
			t.Fatalf("session %s meta storage class = %s; want text", id, kind)
		}
	}
	for id, revision := range map[string]int{"a": 41, "b": 7} {
		var got int
		if err := db.QueryRow("SELECT revision FROM session_records WHERE id=?", id).Scan(&got); err != nil || got != revision {
			t.Fatalf("session %s revision = %d, %v; want %d", id, got, err, revision)
		}
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='session_instruction_blobs'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("blob table still present: %d, %v", tables, err)
	}
	var columns int
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('session_records') WHERE name IN ('instructions_hash','instructions_known_hash')").Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("hash columns still present: %d, %v", columns, err)
	}
	var fields map[string]json.RawMessage
	if err := Load(sessions, "a", &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["instructions"]) != `"teach these steps"` || string(fields["instructionsKnown"]) != "[1,2]" || string(fields["future"]) != `{"keep":[1,"two"]}` {
		t.Fatalf("loaded fields = %v", fields)
	}
	if err := Owners(sessions, func(OwnerRow) error { return nil }); err != nil {
		t.Fatalf("digests after restore: %v", err)
	}
	fields["usage"] = json.RawMessage(`{"input":1}`)
	if err := Save(sessions, "a", fields); err != nil {
		t.Fatal(err)
	}
	var meta string
	if err := db.QueryRow("SELECT meta FROM session_records WHERE id='a'").Scan(&meta); err != nil {
		t.Fatal(err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal([]byte(meta), &saved); err != nil {
		t.Fatal(err)
	}
	if string(saved["instructions"]) != `"teach these steps"` {
		t.Fatalf("saved meta lost its inline instructions: %s", meta)
	}
	if _, err := records(sessions); err != nil {
		t.Fatalf("second open: %v", err)
	}
}

// TestMigrateMissingInstructionBlobAborts checks that a store whose blob is
// gone fails the migration instead of silently dropping instructions: the
// transaction rolls back and the store keeps its version 5 layout.
func TestMigrateMissingInstructionBlobAborts(t *testing.T) {
	sessions := v5Store(t)
	db, err := database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM session_instruction_blobs WHERE hash=(SELECT instructions_hash FROM session_records WHERE id='a')"); err != nil {
		t.Fatal(err)
	}
	if _, err := records(sessions); err == nil {
		t.Fatal("expected the missing blob to fail the migration")
	}
	db, err = database.Open(filepath.Dir(sessions))
	if err != nil {
		t.Fatal(err)
	}
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key='sessions.records'").Scan(&version); err != nil || version != "5" {
		t.Fatalf("aborted migration left version %q, %v; want 5", version, err)
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='session_instruction_blobs'").Scan(&tables); err != nil || tables != 1 {
		t.Fatalf("aborted migration dropped the blob table: %d, %v", tables, err)
	}
}
