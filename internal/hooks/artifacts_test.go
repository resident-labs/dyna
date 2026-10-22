package hooks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

func TestHookArtifactsSurviveChecksDirectoryRemoval(t *testing.T) {
	s, repo := setup(t)
	hookLaunchTestEngine(t, s.DataDir)
	h := hook("self-update", MainUpdated)
	h.Command = "printf hook-output"
	add(t, s, repo, h)
	t.Cleanup(authority.InEngine())
	run, err := s.Run(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), Event{Repo: repo}, h.Name)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(filepath.Join(s.DataDir, "hooks"), run.LogPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("hook log is outside the hooks store: %s", run.LogPath)
	}
	if _, err = os.Stat(filepath.Join(s.DataDir, "checks")); !os.IsNotExist(err) {
		t.Fatalf("hook created checks data: %v", err)
	}
	restarted := Service{DataDir: s.DataDir}
	runs, err := restarted.Runs(context.Background(), repo, h.Name)
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("hook result did not survive restart: %+v %v", runs, err)
	}
	out, err := os.ReadFile(runs[0].LogPath)
	if err != nil || string(out) != "hook-output" {
		t.Fatalf("hook log: %q %v", out, err)
	}
}
