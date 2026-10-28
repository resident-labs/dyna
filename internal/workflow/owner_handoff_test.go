package workflow

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/shells"
	"dyna-agent/internal/store"
	"dyna-agent/internal/worktrees"
)

func TestIsolatedAgentUsesTransferredRunOwner(t *testing.T) {
	repo := initWorktreeTestRepo(t)
	from, to := "ses_20261026-000001-aaaaaaaa", "ses_20261026-000002-aaaaaaaa"
	sessions := filepath.Join(store.DataDir(), "sessions")
	for _, id := range []string{from, to} {
		if err := os.MkdirAll(filepath.Join(sessions, id), 0700); err != nil {
			t.Fatal(err)
		}
		if err := sessionlife.Save(sessions, id, struct {
			ID string `json:"id"`
		}{id}); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("DYNA_SESSION", from)
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_RUN_DETACHED", "1")
	run, err := runstore.Create("handoff", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	oldTree, _, _, err := worktrees.CreateForRun(context.Background(), repo, settings.WorktreesRetentionAll, run.Meta.ID, from)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeWorktreeTestTree(t, repo, oldTree) })
	moved, err := shells.DefaultStore().HandoffRunOwner(context.Background(), run.Meta.ID, from, to, worktrees.HandoffRunTx)
	if err != nil || !moved {
		t.Fatalf("handoff: %v, %v", moved, err)
	}
	script := `return await agent("inspect", {profile:"mock-worktree", isolation:{type:"worktree", retention:"all"}});`
	profiles := &profile.Store{Profiles: []profile.Profile{{Name: "mock-worktree", Harness: profile.HarnessMock, Default: true, Taste: 5, Intelligence: 5, Cost: 10}}}
	if _, err := executeTest(worktreeTestContext(t), Options{ScriptSrc: script, Store: profiles, Run: run, WorkDir: repo, WorktreeRetention: settings.WorktreesRetentionAll}); err != nil {
		t.Fatal(err)
	}
	records, err := worktrees.ForRun(run.Meta.ID)
	if err != nil || len(records) != 2 {
		t.Fatalf("worktrees = %+v, %v", records, err)
	}
	for _, rec := range records {
		if rec.Path != oldTree {
			path := rec.Path
			t.Cleanup(func() { removeWorktreeTestTree(t, repo, path) })
		}
		if rec.Session != to {
			t.Fatalf("worktree %s owner = %q, want %q", rec.Path, rec.Session, to)
		}
	}
}
