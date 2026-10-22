package room

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

// The fixture comes from a real pins.json captured on 2026-10-21.
// Only message IDs and pinner addresses changed; order and timestamps remain.
func pinsMigrationFixture(t *testing.T) (*Store, *sql.DB, string, []byte, []Pin) {
	t.Helper()
	s := &Store{Root: filepath.Join(t.TempDir(), "rooms")}
	db, err := s.recordsDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "room.pins", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), pinsSchema)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/pins-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []Pin
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, "room_20261021-060000-01234567", "pins.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return s, db, path, raw, want
}

func TestPinsMigrationImportsRealRecordOnce(t *testing.T) {
	s, db, path, raw, want := pinsMigrationFixture(t)
	roomID := filepath.Base(filepath.Dir(path))
	dataDir := filepath.Dir(s.Root)
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	// Read rows directly: a read-time importer must not satisfy this assertion.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM room_pins WHERE room_id = ?", roomID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(want) {
		t.Fatalf("eager migration imported %d pins; want %d", count, len(want))
	}
	pins, err := s.Pins(roomID)
	if err != nil || !reflect.DeepEqual(pins, want) {
		t.Fatalf("pins = %+v, %v; want %+v", pins, err, want)
	}
	if err := s.Unpin(roomID, want[0].ID); err != nil {
		t.Fatal(err)
	}
	revision, err := s.PinsRevision(roomID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	pins, err = s.Pins(roomID)
	if err != nil || !reflect.DeepEqual(pins, want[1:]) {
		t.Fatalf("replayed pins = %+v, %v", pins, err)
	}
	if after, err := s.PinsRevision(roomID); err != nil || after != revision {
		t.Fatalf("replay revision = %d, %v; want %d", after, err, revision)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("migration or unpin changed the original file: %v", err)
	}
	// A later old-file replacement has no authority after the versioned import.
	if err := os.WriteFile(path, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	pins, err = s.Pins(roomID)
	if err != nil || !reflect.DeepEqual(pins, want[1:]) {
		t.Fatalf("old file changed current pins = %+v, %v", pins, err)
	}
}

func TestPinsMigrationRollsBackInvalidRecord(t *testing.T) {
	s, db, path, raw, want := pinsMigrationFixture(t)
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("malformed pins error = %v; want the full source path %s", err, path)
	}
	var version string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = ?", "room.pins").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "1" {
		t.Fatalf("failed migration stamped version %q", version)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	pins, err := s.Pins(filepath.Base(filepath.Dir(path)))
	if err != nil || !reflect.DeepEqual(pins, want) {
		t.Fatalf("repaired migration = %+v, %v", pins, err)
	}
}

func TestPinsMigrationPreservesRowsOverRollbackCopy(t *testing.T) {
	s, db, path, raw, _ := pinsMigrationFixture(t)
	roomID := filepath.Base(filepath.Dir(path))
	// Create an authentic stamped rollback copy, then commit a newer row.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := state.For(db).Mirror(context.Background(), roomKey(roomID)+"/pins", path, func(out io.Writer) error {
		_, err := out.Write(raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := []Pin{{ID: "msg_current", By: "fixture", At: 3}}
	if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
		return putPins(context.Background(), conn, roomID, want)
	}); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	pins, err := s.Pins(roomID)
	if err != nil || !reflect.DeepEqual(pins, want) {
		t.Fatalf("stale copy replaced rows = %+v, %v", pins, err)
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, raw) {
		t.Fatalf("migration changed the rollback source: %v", err)
	}
}
