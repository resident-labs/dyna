package shells

import (
	"context"
	"os"
	"testing"

	"dyna-agent/internal/runstore"
)

// A system without /proc reports no start time. The command must still start,
// and the record keeps the plain PID probe instead of failing the launch.
func TestSuperviseStartsWithoutAProcessStartTime(t *testing.T) {
	t.Setenv(runstore.SessionEnv, "")
	s := &Store{Root: t.TempDir(), startTime: func(int) (uint64, error) { return 0, nil }}
	meta, err := s.Create(StartOptions{Cmd: []string{"true"}, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Supervise(context.Background(), meta.ID); err != nil {
		t.Fatalf("Supervise without a start time = %v; want the command to run", err)
	}
	got, err := s.ReadMeta(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Supervisor != os.Getpid() || got.SupervisorStartTime != 0 {
		t.Fatalf("supervisor = %d/%d, want %d with no start time", got.Supervisor, got.SupervisorStartTime, os.Getpid())
	}
}
