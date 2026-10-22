package testfixture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/ghclient"
)

func TestFakeGHReplacesInheritedExecutable(t *testing.T) {
	inherited := filepath.Join(t.TempDir(), "inherited-gh")
	if err := os.WriteFile(inherited, []byte("#!/bin/sh\nexit 23\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ghclient.BinaryEnv, inherited)
	FakeGH(t)
	out, err := ghclient.Command(t.Context(), "", "", "repo", "view").CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"nameWithOwner":"acme/widgets"`) {
		t.Fatalf("fixture did not select its fake gh: %s, %v", out, err)
	}
}
