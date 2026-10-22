package harness

import (
	"context"
	"dyna-agent/internal/computers"
	"dyna-agent/internal/profile"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputerFailureDoesNotFailHarnessTurn(t *testing.T) {
	t.Setenv("DYNA_COMPUTERS_ENABLED", "")
	t.Setenv("DYNA_COMPUTERS_TEST_ENABLE", "1")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "labwc"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	p := profile.Profile{Name: "computer-failure", Harness: profile.HarnessCustom, Command: []string{"/bin/sh", "-c", "printf ran-without-desktop"}, Env: map[string]string{"DYNA_SESSION": "failed-computer"}}
	result, err := Run(context.Background(), p, "continue", t.TempDir())
	if err != nil || !strings.Contains(result.Output, "ran-without-desktop") {
		t.Fatalf("turn: %+v %v", result, err)
	}
	status, err := computers.GetStatus(context.Background(), "failed-computer")
	if err != nil || !strings.Contains(status.Reason, "Go test binary") {
		t.Fatalf("missing setup diagnostic: %+v %v", status, err)
	}
}
