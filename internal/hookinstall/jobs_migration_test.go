package hookinstall

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func TestInstallReceiptImportIsAuthoritative(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() { database.Close(dataDir) })
	s := Store{Root: filepath.Join(dataDir, "hooks", "install")}
	if err := os.MkdirAll(filepath.Join(s.Root, "done"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, testID+".json")
	initial := Job{ID: testID, Recorded: true, Result: "legacy"}
	if err := store.WriteJSON(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	archivedID := "00000000000000000000000000000000"
	if err := store.WriteJSON(filepath.Join(s.Root, "done", archivedID+".json"), Job{ID: archivedID, Recorded: true}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee.json"), []byte("not JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(testID); err != nil || got.Result != "legacy" {
		t.Fatalf("imported job = %+v, %v", got, err)
	}
	if err := s.Update(testID, func(j *Job) error { j.Result = "canonical"; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Archive(testID); err != nil {
		t.Fatal(err)
	}
	preserved, err := os.ReadFile(path)
	if err != nil || string(preserved) != string(source) {
		t.Fatalf("import source changed: %s, %v", preserved, err)
	}
	if err := store.WriteJSON(path, Job{ID: testID, Result: "stale"}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(dataDir); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(testID); err != nil || got.Result != "canonical" || !got.Recorded {
		t.Fatalf("canonical job = %+v, %v", got, err)
	}
	if jobs, err := s.List(); err != nil || len(jobs) != 0 {
		t.Fatalf("archived jobs listed: %+v, %v", jobs, err)
	}
	if got, err := s.Read(archivedID); err != nil || !got.Recorded {
		t.Fatalf("imported archived job = %+v, %v", got, err)
	}
	if err := s.Update(testID, func(j *Job) error { j.Cause = "updated"; return nil }); err != nil {
		t.Fatal(err)
	}
	if jobs, err := s.List(); err != nil || len(jobs) != 0 {
		t.Fatalf("update unarchived job: %+v, %v", jobs, err)
	}
}
