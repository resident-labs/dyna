package hooks

import (
	"context"
	"os"
	"strings"
	"testing"

	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/processguard"
)

// An install unit for the same commit that is already active belongs to an
// earlier install. The scheduled run records that and starts nothing new.
func TestStartUpdateRecordsAlreadyActiveInstall(t *testing.T) {
	s, repo := setup(t)
	sha := strings.Repeat("a", 40)
	var systemdRuns int
	installer := hookinstall.Service{Jobs: hookinstall.Store{Root: t.TempDir()}, Exec: func(_ context.Context, p processguard.Process) error {
		switch p.Args[0] {
		case "systemctl":
			if got := strings.Join(p.Args, " "); got != "systemctl --user is-active dyna-install-"+sha {
				t.Fatalf("unit check = %q", got)
			}
		case "systemd-run":
			systemdRuns++
		default:
			t.Fatalf("unexpected command %v", p.Args)
		}
		return nil
	}}
	s.Install = &installer
	run, err := s.StartUpdate(t.Context(), Event{Event: Schedule, Repo: repo, SHA: sha}, repo, "")
	if err != nil {
		t.Fatalf("start update: %v", err)
	}
	if systemdRuns != 0 {
		t.Fatalf("systemd-run calls = %d, want 0", systemdRuns)
	}
	if run.Result != ResultInstalling || run.FinishedAt.IsZero() {
		t.Fatalf("run = %+v, want a finished %s run", run, ResultInstalling)
	}
	log, err := os.ReadFile(run.LogPath)
	if err != nil || !strings.Contains(string(log), "already active") {
		t.Fatalf("log = %q, %v", log, err)
	}
}
