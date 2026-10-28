package worktreesvc

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/worktrees"
)

func TestPrunePreservesSessionWorktrees(t *testing.T) {
	for _, tc := range []struct {
		name   string
		closed bool
		force  bool
	}{
		{"open session", false, false},
		{"open goal with force", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			tree, err := os.MkdirTemp("", "dyna-wt-prune-test-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.RemoveAll(tree) })
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", tree}, args...)...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q")
			git("-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "initial")
			head := git("rev-parse", "HEAD")
			meta := map[string]any{"id": "worker", "room": "project", "dir": tree}
			if tc.closed {
				meta["closed"] = time.Now()
				if _, err := goals.DefaultStore().Add("project", "setter", goals.Input{Text: "Finish work", Holders: []string{"worker"}}); err != nil {
					t.Fatal(err)
				}
			}
			recordtest.Write(t, runstore.SessionsDir(), "worker", meta)
			rec := worktrees.Record{Path: tree, Repo: filepath.Join(t.TempDir(), "missing"), Head: head, Port: 49000, RetainedAt: time.Now().Add(-72 * time.Hour)}
			if err := worktrees.Track(rec); err != nil {
				t.Fatal(err)
			}
			ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
			result, err := Prune(ctx, 48*time.Hour, tc.force, false)
			if err != nil || result.Failure != "" {
				t.Fatalf("prune: %+v, %v", result, err)
			}
			if len(result.Decisions) != 1 || result.Decisions[0].Action != worktrees.ActionSkipped {
				t.Fatalf("decisions: %+v", result.Decisions)
			}
			if _, err := os.Stat(tree); err != nil {
				t.Fatalf("tree removed: %v", err)
			}
		})
	}
}
