package gitsvc

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceGitBypassesOwnerWrapper(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("/usr/bin/git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %s %v", out, err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\necho '(using wrong-account)' >&2\nexit 65\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	raw, _, err := runWorkspaceGit(context.Background(), root, 4096, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(string(raw)) != root {
		t.Fatalf("repository lookup used wrapper: %q %v", raw, err)
	}
}
