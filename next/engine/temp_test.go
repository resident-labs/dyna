package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Keep test stores off a busy disk when Linux shared memory is available. Pool tests run scripts
// from their temp folders, and a container's /dev/shm is noexec, so there they stay in the default.
func TestMain(m *testing.M) {
	if dir, err := os.MkdirTemp("/dev/shm", "dyna-test-"); err == nil {
		if canRun(dir) {
			os.Setenv("TMPDIR", dir)
		}
		code := m.Run()
		os.RemoveAll(dir)
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func canRun(dir string) bool {
	script := filepath.Join(dir, "probe")
	return os.WriteFile(script, []byte("#!/bin/sh\n"), 0700) == nil && exec.Command(script).Run() == nil
}
