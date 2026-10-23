package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// These golden bytes pin the previous writeJSONAtomic format through real
// room callers, including struct field order, escaping and the final newline.
func TestStorageGoldenBytes(t *testing.T) {
	s := &Store{Root: t.TempDir(), Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
	rec, err := s.Create(CreateInput{Name: "alpha", CreatedBy: "system"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, rec.ID, "room.json")
	want := "{\n  \"sessionPause\": {\n    \"roomId\": \"\",\n    \"paused\": false,\n    \"pausedAt\": \"0001-01-01T00:00:00Z\",\n    \"resumedAt\": \"0001-01-01T00:00:00Z\",\n    \"revision\": 0\n  },\n  \"id\": \"" + rec.ID + "\",\n  \"name\": \"alpha\",\n  \"createdBy\": \"system\",\n  \"createdAt\": \"2026-01-02T03:04:05Z\",\n  \"lastActivity\": \"2026-01-02T03:04:05Z\"\n}\n"
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("room bytes = %s; error %v", got, err)
	}
	// Compare the old encoder directly as well as the literal golden.
	old, err := json.MarshalIndent(rec, "", "  ")
	if err != nil || string(got) != string(append(old, '\n')) {
		t.Fatal("old room encoder differs")
	}
	if err := s.MarkOwnerReadMany(rec.ID, []string{"z", "a"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("room mode = %o", info.Mode().Perm())
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
