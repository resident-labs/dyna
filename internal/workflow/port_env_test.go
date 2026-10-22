package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/worktrees"
	"dyna-agent/internal/worktrees/worktreetest"
)

// A workflow worker in a tracked worktree gets that worktree's port. Other directories get none,
// even when the parent process has port variables from another worktree.
func TestWorkflowWorkerGetsWorktreePortEnv(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv(runstore.AgentJournalRootEnv, "")
	t.Setenv("DYNA_PORT", "39999")
	t.Setenv("DYNA_PORTS", "39999-40008")
	t.Setenv("PORT", "39999")
	tracked := worktreetest.Tracked(t)
	rec, err := worktrees.Get(tracked)
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(rec.Port)
	for _, tc := range []struct {
		name, dir, want string
	}{
		{"tracked worktree", tracked, port + "|" + port},
		{"plain directory", t.TempDir(), "|"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := filepath.Join(t.TempDir(), "port")
			store := &profile.Store{Profiles: []profile.Profile{{
				Name: "leafy", Harness: profile.HarnessCustom, Default: true, Taste: 5, Intelligence: 5, Cost: 5,
				Command: []string{"/bin/sh", "-c", `printf '%s|%s' "$DYNA_PORT" "$PORT" > "$CAPTURE"; printf done`},
				Env:     map[string]string{"CAPTURE": capture},
			}}}
			run, err := runstore.Create("port env", `return await agent("task")`, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer run.Finish("ok", "", nil)
			if _, err := Execute(context.Background(), Options{
				ScriptSrc: `return await agent("task")`, Store: store, Run: run, WorkDir: tc.dir,
			}); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(capture)
			if err != nil || string(got) != tc.want {
				t.Fatalf("DYNA_PORT|PORT = %q, %v; want %q", got, err, tc.want)
			}
			if store.Profiles[0].Env["DYNA_PORT"] != "" {
				t.Fatal("worker port leaked into the shared profile store")
			}
		})
	}
}
