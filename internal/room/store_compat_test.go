package room

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Current writes do not create legacy files and keep lock files private.
func TestStorageWritesOnlyCanonicalState(t *testing.T) {
	s := &Store{Root: t.TempDir(), Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
	rec, err := s.Create(CreateInput{Name: "alpha", CreatedBy: "system"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, rec.ID, "room.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("created a legacy room file: %v", err)
	}
	if err := s.MarkOwnerReadMany(rec.ID, []string{"z", "a"}); err != nil {
		t.Fatal(err)
	}
	for _, lock := range []string{filepath.Join(s.Root, ".lock"), filepath.Join(s.Root, rec.ID, "room.lock")} {
		info, err := os.Stat(lock)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("lock mode = %o", info.Mode().Perm())
		}
	}
}
