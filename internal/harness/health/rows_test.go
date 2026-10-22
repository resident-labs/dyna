package health

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"

	"dyna-agent/internal/store"
)

// oldBinaryWrites writes a profile's record the way the previous release
// does: a new file renamed onto its path.
func oldBinaryWrites(t *testing.T, s *Service, name string, r record) {
	t.Helper()
	path, err := s.path(name)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, json.RawMessage(b), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A record that the previous release wrote is imported on read, and then
// lives in its row: the app's view needs no file.
func TestOldBinaryRecordIsImportedOnRead(t *testing.T) {
	s, p := New(t.TempDir()), testProfile()
	at := time.Unix(5, 0).UTC()
	oldBinaryWrites(t, s, p.Name, record{Fingerprint: Fingerprint(p), Version: "v1", LastHealthyVersion: "v1", LastHealthyAt: at,
		Observed: Observed{Observation: Observation{At: at, Successful: true, StdoutSeen: true, RecognizedRecords: 1}, LastTurnAt: at, RecognizedOutput: true}})
	if v := s.View(p, "v1"); v.Status != Healthy || v.StorageError != "" {
		t.Fatalf("view of the old binary's record = %+v", v)
	}
	path, _ := s.path(p.Name)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if v := s.View(p, "v1"); v.Status != Healthy || v.LastHealthyVersion != "v1" {
		t.Fatalf("without the file, the row was lost: %+v", v)
	}
}

// A record imports the previous release's write first, then applies its own
// observation on top: the old binary's healthy version is kept.
func TestRecordImportsAnOldBinaryWriteFirst(t *testing.T) {
	s, p := New(t.TempDir()), testProfile()
	recordTurn(t, s, p, "v1", observation(1))
	at := time.Unix(10, 0).UTC()
	oldBinaryWrites(t, s, p.Name, record{Fingerprint: Fingerprint(p), Version: "old", LastHealthyVersion: "old", LastHealthyAt: at,
		Observed: Observed{Observation: Observation{At: at, Successful: true, StdoutSeen: true, RecognizedRecords: 1}, LastTurnAt: at, RecognizedOutput: true}})
	running := observation(11)
	if v := recordProgress(t, s, p, "old", running); v.StorageError != "" || v.LastHealthyVersion != "old" {
		t.Fatalf("progress after the old binary's write = %+v; want its healthy version kept", v)
	}
	if v := s.View(p, "old"); v.LastHealthyVersion != "old" || v.LastCheckedAt != running.At {
		t.Fatalf("row after the update = %+v", v)
	}
}

// The copy is rewritten only when a field that decides the status changes:
// a later check with the same outcome moves the row's times, not the file.
func TestCopyIsRewrittenOnlyWhenTheStatusInputsChange(t *testing.T) {
	s, p := New(t.TempDir()), testProfile()
	path, _ := s.path(p.Name)
	recordTurn(t, s, p, "v1", observation(1))
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v := recordTurn(t, s, p, "v1", observation(2)); v.LastCheckedAt != observation(2).At {
		t.Fatalf("row after a same-outcome check = %+v", v)
	}
	if same, _ := os.ReadFile(path); !bytes.Equal(same, first) {
		t.Fatal("a check that changed only times rewrote the copy")
	}
	failed := observation(3)
	failed.Successful = false
	if v := recordTurn(t, s, p, "v1", failed); v.Status != Unhealthy {
		t.Fatalf("view after a failed turn = %+v", v)
	}
	var copied record
	if err := store.ReadJSON(path, &copied); err != nil || copied.Observed.Successful {
		t.Fatalf("copy after a failed turn = %+v, %v; want the failure", copied, err)
	}
	// The row is current even where the copy is not.
	if v := s.View(p, "v1"); v.Status != Unhealthy || v.LastCheckedAt != failed.At {
		t.Fatalf("view = %+v", v)
	}
}
