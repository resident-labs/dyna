package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/repoid"
)

func TestClaudeRepositoryInstructionsStayInsideGitRoot(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	parent := t.TempDir()
	repo := filepath.Join(parent, "repo")
	cwd := filepath.Join(repo, "nested")
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := repoid.Git(context.Background(), repo, "init", "-q"); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(parent, "CLAUDE.md"): "PARENT_CANARY",
		filepath.Join(repo, "CLAUDE.md"):   "REPOSITORY_CANARY",
		filepath.Join(repo, "AGENTS.md"):   "SHADOWED_FALLBACK_CANARY",
		filepath.Join(cwd, "AGENTS.md"):    "NESTED_FALLBACK_CANARY",
	} {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	p := profile.Profile{Harness: profile.HarnessClaudeCode, RuntimeInstructions: "DYNA_CANARY", ExtraArgs: []string{"--setting-sources", "user"}}
	inv, err := buildInvocationInDir(p, "task", cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{inv.initial.argv, inv.resume("session", "again").argv} {
		var instructionFile string
		for i, arg := range args {
			if arg == "--append-system-prompt-file" {
				instructionFile = args[i+1]
			}
		}
		raw, err := os.ReadFile(instructionFile)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, want := range []string{"DYNA_CANARY", "REPOSITORY_CANARY", "NESTED_FALLBACK_CANARY"} {
			if !strings.Contains(text, want) {
				t.Errorf("native instruction file lost %s", want)
			}
		}
		for _, denied := range []string{"PARENT_CANARY", "SHADOWED_FALLBACK_CANARY"} {
			if strings.Contains(text, denied) {
				t.Errorf("native instruction file includes %s", denied)
			}
		}
		if strings.Index(text, "REPOSITORY_CANARY") > strings.Index(text, "NESTED_FALLBACK_CANARY") {
			t.Error("nested instructions must follow repository instructions")
		}
		if !hasOptionValue(args, "--setting-sources", "user") {
			t.Error("repository instructions re-enabled native project settings")
		}
	}
}

func TestClaudeRepositoryInstructionsRejectOversizeAndEscapingSymlink(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, kind := range []string{"oversize", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			repo := t.TempDir()
			if _, err := repoid.Git(context.Background(), repo, "init", "-q"); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(repo, "CLAUDE.md")
			if kind == "oversize" {
				if err := os.WriteFile(path, []byte(strings.Repeat("x", 64*1024+1)), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				outside := filepath.Join(t.TempDir(), "outside.md")
				if err := os.WriteFile(outside, []byte("OUTSIDE_CANARY"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			}
			_, err := buildInvocationInDir(profile.Profile{Harness: profile.HarnessClaudeCode}, "task", repo)
			if err == nil {
				t.Fatalf("accepted %s repository instructions", kind)
			}
		})
	}
}
