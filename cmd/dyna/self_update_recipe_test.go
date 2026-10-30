package main

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hookinstall"
	"dyna-agent/internal/processguard"
	"dyna-agent/internal/store/storetest"
)

func TestUpdateWorkersResolveThroughGitHooks(t *testing.T) {
	storetest.DataDir(t)
	repo := t.TempDir()
	const exe = "/fake/dyna with ' quote"
	routes := map[string][]string{}
	installData := t.TempDir()
	t.Cleanup(func() { database.Close(installData) })
	s := hookinstall.Service{Jobs: hookinstall.Store{Root: filepath.Join(installData, "hooks", "install")}}
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
