package computers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
)

func recordTestID(t *testing.T) string {
	t.Helper()
	storetest.DataDir(t)
	t.Cleanup(func() { database.Close(filepath.Dir(Root())) })
	id := "ses_20261020-120000-abcdef12"
	if err := os.MkdirAll(Dir(id), 0o700); err != nil {
		t.Fatal(err)
	}
	return id
}

// oldBinaryWrites writes a computer's record file the way the previous
// release does.
func oldBinaryWrites(t *testing.T, path string, v any) {
	t.Helper()
	if err := store.WriteJSON(path, v, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A retained registration is imported during startup and then lives in SQLite.
func TestOldBinaryMetaIsImportedAtStartup(t *testing.T) {
	id := recordTestID(t)
	oldBinaryWrites(t, metaRecord.path(id), Meta{SessionID: id, Room: "old-room"})
	if err := Migrate(filepath.Dir(Root())); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta(id); err != nil || m.Room != "old-room" {
		t.Fatalf("ReadMeta = %+v, %v", m, err)
	}
	if err := os.Remove(metaRecord.path(id)); err != nil {
		t.Fatal(err)
	}
	if m, err := ReadMeta(id); err != nil || m.Room != "old-room" {
		t.Fatalf("without the file, ReadMeta = %+v, %v", m, err)
	}
}

// A computer with no record reads as not found, as before.
func TestReadMetaOfAnUnknownComputerIsNotFound(t *testing.T) {
	id := recordTestID(t)
	if _, err := ReadMeta(id); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ReadMeta of an unknown computer = %v, want not found", err)
	}
}

// The status row is written only when the status changes in
// more than its sampled metrics.
func TestStatusIsWrittenOnlyWhenItChanges(t *testing.T) {
	id := recordTestID(t)
	s := Status{SessionID: id, State: StateRunning, Width: 1280, Height: 800, IdleSeconds: 1, RSSKiB: 100}
	if err := saveStatus(s); err != nil {
		t.Fatal(err)
	}
	db, err := recordsDB()
	if err != nil {
		t.Fatal(err)
	}
	before := state.For(db).Health().Writes
	first, err := column(t.Context(), db, id, statusRecord)
	if err != nil {
		t.Fatal(err)
	}
	s.IdleSeconds, s.RSSKiB, s.MemoryCurrentKiB = 5, 200, 300
	if err := saveStatus(s); err != nil {
		t.Fatal(err)
	}
	if got := state.For(db).Health().Writes - before; got != 0 {
		t.Fatalf("a sample-only change made %d state writes, want 0", got)
	}
	if same, _ := column(t.Context(), db, id, statusRecord); !bytes.Equal(same, first) {
		t.Fatal("a sample-only change rewrote the row")
	}
	s.State = StateIdleStopped
	if err := saveStatus(s); err != nil {
		t.Fatal(err)
	}
	if got := savedStatus(id); got.State != StateIdleStopped {
		t.Fatalf("status after a change = %+v", got)
	}
	if _, err := os.Stat(statusRecord.path(id)); !os.IsNotExist(err) {
		t.Fatalf("save wrote retained file: %v", err)
	}

}

// A status write compares only the canonical row and leaves retained files alone.
func TestStatusWriteIgnoresRetainedFile(t *testing.T) {
	id := recordTestID(t)
	current := Status{SessionID: id, State: StateRunning}
	if err := saveStatus(current); err != nil {
		t.Fatal(err)
	}
	oldBinaryWrites(t, statusRecord.path(id), Status{SessionID: id, State: StateIdleStopped, Reason: "stale"})
	if err := saveStatus(current); err != nil {
		t.Fatal(err)
	}
	if got := savedStatus(id); got.State != StateRunning || got.Reason != "" {
		t.Fatalf("status = %+v", got)
	}
}

// A damaged record file from another binary is skipped once
// (state.ErrSkipForeign): the row stays.
func TestDamagedRecordFileKeepsTheRow(t *testing.T) {
	id := recordTestID(t)
	if err := saveMeta(Meta{SessionID: id, Room: "row"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metaRecord.path(id), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if m, err := ReadMeta(id); err != nil || m.Room != "row" {
			t.Fatalf("after a damaged file, ReadMeta = %+v, %v", m, err)
		}
	}
	var copied map[string]any
	if raw, err := os.ReadFile(metaRecord.path(id)); err != nil || json.Unmarshal(raw, &copied) == nil {
		t.Fatalf("the damaged file was replaced: %q, %v", raw, err)
	}
}
