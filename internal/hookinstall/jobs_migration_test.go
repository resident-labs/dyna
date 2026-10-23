package hookinstall

import (
	"context"
	"dyna-agent/internal/processguard"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/store"
)

func TestInstallReceiptImportIsAuthoritative(t *testing.T) {
	t.Run("unfinished legacy worker", testLegacyInstallerHandoff)
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

func testLegacyInstallerHandoff(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(func() { database.Close(dir) })
	jobs := Store{Root: filepath.Join(dir, "hooks", "install")}
	if err := os.MkdirAll(jobs.Root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(jobs.Root, testID+".json")
	started := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	initial := Job{ID: testID, Unit: "legacy-worker", Status: "running", StartedAt: started}
	if err := store.WriteJSON(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	final := initial
	final.Status, final.Result, final.Cause = "finished", "error", "desktop restart failed after install"
	final.Installed, final.FinishedAt = testSHA, started.Add(time.Minute)
	workerFinished := false
	service := Service{Jobs: jobs, Now: func() time.Time { return started.Add(3 * time.Minute) }}
	service.Exec = func(_ context.Context, p processguard.Process) error {
		// The old worker finishes after the new engine imported its running job,
		// just before systemd reports its exit. It still writes only JSON.
		unlock, err := store.Lock(path + ".lock")
		if err != nil {
			return err
		}
		defer unlock()
		if err := store.WriteJSON(path, final, 0600); err != nil {
			return err
		}
		workerFinished = true
		_, err = fmt.Fprint(p.Output, "ActiveState=failed\nSubState=failed\nResult=exit-code\nExecMainStatus=1\nLoadState=loaded\n")
		return err
	}
	got, err := service.InspectUnit(t.Context(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if !workerFinished || got.Installed != final.Installed || got.Cause != final.Cause || !got.FinishedAt.Equal(final.FinishedAt) {
		t.Fatalf("final legacy receipt lost: %+v", got)
	}
	if err := jobs.Update(testID, func(j *Job) error { j.Recorded = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := jobs.Archive(testID); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteJSON(path, initial, 0600); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(dir); err != nil {
		t.Fatal(err)
	}
	got, err = jobs.Read(testID)
	if err != nil || got.Installed != final.Installed || got.Cause != final.Cause || !got.FinishedAt.Equal(final.FinishedAt) || !got.Recorded {
		t.Fatalf("retired handoff accepted stale JSON: %+v, %v", got, err)
	}
}
