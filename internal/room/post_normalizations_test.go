package room

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// historicalWakeFixture creates a retained post without explicit wake intent.
func historicalWakeFixture(t *testing.T) (*Store, string, string, []byte) {
	t.Helper()
	s := &Store{Root: filepath.Join(t.TempDir(), "rooms")}
	roomID := "room_20261021-060000-01234567"
	path := filepath.Join(s.Root, roomID, "messages.jsonl")
	raw := []byte("{\"id\":\"legacy\",\"kind\":\"message\"}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return s, roomID, path, raw
}

func TestRoomWakeMigrationPreservesSourceOffsets(t *testing.T) {
	s, roomID, path, raw := historicalWakeFixture(t)
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	db, err := s.recordsDB()
	if err != nil {
		t.Fatal(err)
	}
	var start, end int64
	var body, digest []byte
	if err := db.QueryRow("SELECT source_start, source_end, body, digest FROM room_post_normalizations WHERE room_id = ?", roomID).Scan(&start, &end, &body, &digest); err != nil {
		t.Fatal(err)
	}
	wantDigest := sha256.Sum256(raw)
	if start != 0 || end != int64(len(raw)) || !bytes.Equal(digest, wantDigest[:]) {
		t.Fatalf("projection range = %d..%d, digest %x", start, end, digest)
	}
	var current map[string]json.RawMessage
	if err := json.Unmarshal(body, &current); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current["wake"], []byte("true")) {
		t.Fatalf("canonical wake = %s", current["wake"])
	}
	messages, next, err := s.ReadFrom(roomID, 0)
	if err != nil || len(messages) != 1 || !messages[0].Wake || next != int64(len(raw)) {
		t.Fatalf("normalized read = %+v, %d, %v", messages, next, err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("migration changed source bytes: %v", err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM room_post_normalizations WHERE room_id = ?", roomID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("replayed projections = %d, %v", count, err)
	}
	// Existing byte cursors still reach the same end after normalization.
	messages, next, err = s.ReadFrom(roomID, int64(len(raw)))
	if err != nil || len(messages) != 0 || next != int64(len(raw)) {
		t.Fatalf("end cursor = %+v, %d, %v", messages, next, err)
	}
	messages, next, err = s.ReadFrom(roomID, 1)
	if err != nil || len(messages) != 0 || next != int64(len(raw)) {
		t.Fatalf("arbitrary cursor = %+v, %d, %v", messages, next, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{\"id\":\"later\",\"kind\":\"message\"}\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	messages, _, err = s.ReadFrom(roomID, int64(len(raw)))
	if err != nil || len(messages) != 1 || messages[0].Wake {
		t.Fatalf("later post = %+v, %v", messages, err)
	}

	// A replacement must not apply a projection to different source bytes.
	changed := bytes.Replace(raw, []byte("legacy"), []byte("edited"), 1)
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadFrom(roomID, 0); err == nil {
		t.Fatal("changed source accepted")
	}

}
