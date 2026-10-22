package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/processguard"
)

func TestHookRejectsForgedBranchMetadata(t *testing.T) {
	s, repo := setup(t)
	h := hook("gate", BeforeLand)
	h.OnFail = "block"
	add(t, s, repo, h)
	called := false
	s.Exec = func(context.Context, processguard.Process) error {
		called = true
		return nil
	}
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: "session-trigger"})
	err := s.Emit(ctx, Event{Repo: repo, Event: BeforeLand, SHA: "captured", Branch: "feature;dyna library wake", Session: "session-trigger"})
	var block *BlockError
	if !errors.As(err, &block) || called {
		t.Fatalf("unsafe branch reached command: called=%v, error=%v", called, err)
	}
	runs, err := s.Runs(t.Context(), repo, h.Name)
	if err != nil || len(runs) != 1 || runs[0].Result != "error" || runs[0].RunnerSession != "session-trigger" {
		t.Fatalf("refusal audit = %+v, %v", runs, err)
	}
	raw, err := os.ReadFile(runs[0].LogPath)
	if err != nil || !strings.Contains(string(raw), "DYNA_HOOK_BRANCH") {
		t.Fatalf("refusal log = %q, %v", raw, err)
	}
}

func TestHookRejectsUnsafeEventMetadata(t *testing.T) {
	for _, field := range []string{"event", "worktree", "branch", "sha", "pr", "session"} {
		t.Run(field, func(t *testing.T) {
			s, repo := setup(t)
			h := hook("metadata", AfterLand)
			add(t, s, repo, h)
			e := Event{Repo: repo, Event: AfterLand, Worktree: repo, Branch: "feature", SHA: "captured", PR: "1", Session: "session"}
			switch field {
			case "event":
				e.Event = "after-land\nforged"
			case "worktree":
				e.Worktree = filepath.Join(repo, "tree;forged")
			case "branch":
				e.Branch = "branch\nforged"
			case "sha":
				e.SHA = "$(forged)"
			case "pr":
				e.PR = "1|forged"
			case "session":
				e.Session = "session\x00forged"
			}
			called := false
			s.Exec = func(context.Context, processguard.Process) error { called = true; return nil }
			run, err := s.Run(t.Context(), e, h.Name)
			if err == nil || called || run.Result != "error" {
				t.Fatalf("unsafe %s reached command: called=%v, run=%+v, error=%v", field, called, run, err)
			}
		})
	}
}

func TestHookMetadataValidatorCoversEveryValue(t *testing.T) {
	for _, key := range []string{
		"DYNA_HOOK_EVENT", "DYNA_HOOK_REPO", "DYNA_HOOK_WORKTREE",
		"DYNA_HOOK_BRANCH", "DYNA_HOOK_SHA", "DYNA_HOOK_PR",
		"DYNA_HOOK_SESSION", "DYNA_HOOK_RUN_ID", "DYNA_HOOK_LOG",
	} {
		for _, unsafe := range []string{";", "\n", "\r", "\x00", "\u2028", "\u2029", "\t", "$", "|", "&", "(", ")", "<", ">", "\\", "\"", "'", "*", "?", "[", "]", "{", "}", "!", "~", string(rune(96))} {
			if err := validateHookMetadata([]string{key + "=safe" + unsafe + "suffix"}); err == nil {
				t.Errorf("%s accepted %q", key, unsafe)
			}
		}
	}
	if err := validateHookMetadata([]string{
		"DYNA_HOOK_REPO=github.com:owner/repo with space",
		"DYNA_HOOK_WORKTREE=/tmp/main checkout",
		"DYNA_HOOK_BRANCH=feature/update-v1.2+test@remote",
		"DYNA_HOOK_SESSION=room:ses_20261021-000000-a1b2c3d4",
		"DYNA_HOOK_PR=42", "DYNA_HOOK_SHA=0123456789abcdef", "DYNA_HOOK_LOG=/tmp/hook output.log",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestHookRunsInWorktreeWithSpace(t *testing.T) {
	s, repo := setup(t)
	hookLaunchTestEngine(t, s.DataDir)
	tree := filepath.Join(t.TempDir(), "tree with space")
	git(t, repo, "worktree", "add", "-b", "feature-space", tree)
	h := hook("space", AfterLand)
	h.Command = "printf '%s' \"$DYNA_HOOK_WORKTREE\""
	add(t, s, repo, h)
	t.Cleanup(authority.InEngine())
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	run, err := s.Run(ctx, Event{Repo: repo, Worktree: tree}, h.Name)
	if err != nil || run.Result != "pass" {
		t.Fatalf("worktree with space did not run: %+v, %v", run, err)
	}
	raw, err := os.ReadFile(run.LogPath)
	if err != nil || string(raw) != tree {
		t.Fatalf("worktree metadata changed: %q, %v; want %q", raw, err, tree)
	}
}
