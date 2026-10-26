//go:build linux

package harnessconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// The restart execs a start path whose file differs from the running image,
// and skips a path that still names it: the running build is already the
// newest that path offers, and exec-ing it would only lose the failing
// call's answer again (D-171).
func TestRestartTargetsFollowTheStartPathButNotTheRunningImage(t *testing.T) {
	replaced := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(replaced, []byte("#!/bin/sh\ntrue\n"), 0700); err != nil {
		t.Fatal(err)
	}
	targets := restartTargets([]string{replaced, "mcp", "shell"})
	if len(targets) != 1 || targets[0] != replaced {
		t.Fatalf("restart targets = %v, want [%s]", targets, replaced)
	}
	// A start path that is the running image itself never restarts.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if targets := restartTargets([]string{self, "mcp", "shell"}); len(targets) != 0 {
		t.Fatalf("restart targets include the running image: %v", targets)
	}
}
