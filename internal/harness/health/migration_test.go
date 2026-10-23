package health

import (
	"os"
	"testing"
)

func TestHealthImportIsEagerAndRetainedFileCannotOverwriteObservation(t *testing.T) {
	s, p := New(t.TempDir()), testProfile()
	old := record{Fingerprint: Fingerprint(p), Version: "v1"}
	oldBinaryWrites(t, s, p.Name, old)
	path, _ := s.path(p.Name)
	retained, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.db()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := readRow(t.Context(), db, p.Name); err != nil || !ok || got.Version != "v1" {
		t.Fatalf("eager import = %+v, %v, %v", got, ok, err)
	}
	recordTurn(t, s, p, "v2", observation(2))
	if got, err := os.ReadFile(path); err != nil || string(got) != string(retained) {
		t.Errorf("save changed retained evidence: %v", err)
	}
	oldBinaryWrites(t, s, p.Name, old)
	if got := s.View(p, "v2"); got.LastHealthyVersion != "v2" || got.LastCheckedAt != observation(2).At || got.StorageError != "" {
		t.Fatalf("retained evidence replaced observation: %+v", got)
	}
}
