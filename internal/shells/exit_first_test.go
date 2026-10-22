package shells

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

// A failed line-count write (a busy dyna.db or a deadline) must not lose the
// exit: without it the job shows as lost once its supervisor ends.
func TestSuperviseSavesTheExitWhenTheLineCountFails(t *testing.T) {
	s := &Store{Root: filepath.Join(t.TempDir(), "shells")}
	meta, err := s.Create(StartOptions{Cmd: []string{"/bin/sh", "-c", "echo one; exit 3"}, Cwd: t.TempDir(), Keep: true})
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TRIGGER fail_line_count BEFORE UPDATE OF output_lines ON shell_jobs
BEGIN SELECT RAISE(ABORT, 'line count write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.Supervise(context.Background(), meta.ID); err == nil {
		t.Fatal("Supervise hid the failed line count")
	}
	ex, err := s.ReadExit(meta.ID)
	if err != nil || ex == nil || ex.Code != 3 {
		t.Fatalf("exit = %+v, %v; want code 3 saved", ex, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, meta.ID, exitedMarker)); err != nil {
		t.Fatalf("waiters were not woken: %v", err)
	}
}
