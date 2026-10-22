package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/shellquote"
	"dyna-agent/internal/store/storetest"
)

func TestSelfUpdateAndWorkersResolveThroughGitHooks(t *testing.T) {
	storetest.DataDir(t)
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", repo}, {"-C", repo, "remote", "add", "origin", "https://github.com/resident-labs/dyna.git"}} {
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git fixture: %s: %v", out, err)
		}
	}
	tools := t.TempDir()
	for _, name := range []string{"go", "npm", "node"} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools)
	if err := os.MkdirAll(filepath.Join(repo, "desktop"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, data := range map[string]string{"go.mod": "module dyna-agent\n", "desktop/build.sh": "fixture"} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	const exe = "/fake/dyna with ' quote"
	recipe, err := hooks.SelfUpdateRecipe(context.Background(), repo, exe, "self-update")
	if err != nil {
		t.Fatal(err)
	}
	// The recipe quotes the executable as one shell word. Only parse its command route.
	tail, ok := strings.CutPrefix(recipe.Command, shellquote.Quote(exe)+" ")
	if !ok {
		t.Fatalf("recipe executable: %s", recipe.Command)
	}
	routes := map[string][]string{"recipe": strings.Fields(strings.Split(tail, " --main ")[0])}
	s := hookinstall.Service{Jobs: hookinstall.Store{Root: t.TempDir()}}
	s.Exec = func(_ context.Context, p processguard.Process) error {
		i := slices.Index(p.Args, exe)
		if i < 0 {
			t.Fatalf("missing executable: %q", p.Args)
		}
		routes[p.Args[0]] = p.Args[i+1:]
		return nil
	}
	job := hookinstall.Job{ID: strings.Repeat("1", 32), SHA: strings.Repeat("a", 40), HookLog: filepath.Join(t.TempDir(), "hook.log"), Config: hookinstall.Config{Executable: exe, Main: repo, Lock: filepath.Join(t.TempDir(), "lock")}}
	if err := s.Start(t.Context(), job, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := s.Worker(t.Context(), job.ID); err != nil {
		t.Fatal(err)
	}
	for name, args := range routes {
		cmd, _, err := newRootCommand().Find(args)
		if err != nil || cmd.Parent().CommandPath() != "dyna git hooks" {
			t.Errorf("%s route %q resolves to %v: %v", name, args, cmd, err)
		}
	}
}
