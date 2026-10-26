package computers

import (
	"context"
	"dyna-agent/internal/testsafe"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureCannotSpawnTestBinary(t *testing.T) {
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", testsafe.ShortTempDir(t))
	// Ensure reaches the spawn guard without depending on installed GUI tools.
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	_, err := Ensure(context.Background(), "guard-regression", "")
	if err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(Root()); !os.IsNotExist(err) {
		t.Fatal("Ensure created daemon state before refusing test spawn")
	}
}
func TestComputersDisabledInTestsByDefault(t *testing.T) {
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "")
	if ok, why := Enabled(); ok || !strings.Contains(why, "Go tests") {
		t.Fatalf("enabled=%v reason=%s", ok, why)
	}
}
func TestStartDaemonCannotRunTestBinary(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err = startDaemon(context.Background(), exe); err == nil || !strings.Contains(err.Error(), "Go test binary") {
		t.Fatalf("startDaemon: %v", err)
	}
}
