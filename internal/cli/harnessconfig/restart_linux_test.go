//go:build linux

package harnessconfig

import (
	"os"
	"path/filepath"
	"reflect"
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

// The restarted server keeps the environment this process launched with. Main
// has already restored the owner's HOME, XDG roots and PATH by then, and the
// session's private roots and tool bridges survive only in the launch
// environment; a restart on os.Environ would run every later shell job with
// the owner's roots (D-171).
func TestRestartPassesTheLaunchEnvironment(t *testing.T) {
	replaced := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(replaced, []byte("#!/bin/sh\ntrue\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launch := []string{"HOME=/private/home", "PATH=/private/bin", "DYNA_OWNER_ENV={}"}
	var gotPath string
	var gotArgv, gotEnv []string
	restartOnOutdated([]string{replaced, "mcp", "shell"}, launch, func(path string, argv, environ []string) error {
		gotPath, gotArgv, gotEnv = path, argv, environ
		return nil
	})()
	if gotPath != replaced || !reflect.DeepEqual(gotArgv, []string{replaced, "mcp", "shell"}) || !reflect.DeepEqual(gotEnv, launch) {
		t.Fatalf("exec(%q, %q, %q), want the replaced binary, its arguments and the launch environment", gotPath, gotArgv, gotEnv)
	}
}
