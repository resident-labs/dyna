package home

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// D-155: the harness-home probe asked the toolchain for its defaults in the
// caller's configuration directory, and the toolchain records telemetry counters
// under XDG_CONFIG_HOME/go/telemetry. In a test that directory is temporary, so a
// late counter write raced t.TempDir's removal and failed a green test.
func TestGoDefaultsKeepsTheToolchainOutOfTheCallersConfig(t *testing.T) {
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no Go toolchain in PATH")
	}
	config := t.TempDir()
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + config}
	if got := goDefaults(filepath.Dir(path), env); len(got) == 0 {
		t.Skip("the toolchain did not answer go env")
	}
	if _, err := os.Stat(filepath.Join(config, "go", "telemetry")); !os.IsNotExist(err) {
		t.Fatalf("the probe wrote the toolchain telemetry into the caller's configuration: %v", err)
	}
}

// The probe keeps every other variable the caller passed, so the toolchain still
// answers with the owner's configured paths.
func TestGoProbeEnvKeepsTheCallersEnvironment(t *testing.T) {
	env := goProbeEnv([]string{"HOME=/owner", "PATH=/bin", "XDG_CONFIG_HOME=/tests/tree", "GOTELEMETRY=on"}, "/probe/config")
	for _, want := range []string{"HOME=/owner", "PATH=/bin", "XDG_CONFIG_HOME=/probe/config", "GOTELEMETRY=off"} {
		found := false
		for _, pair := range env {
			if pair == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("probe env lacks %q: %v", want, env)
		}
	}
	for _, pair := range env {
		if pair == "XDG_CONFIG_HOME=/tests/tree" {
			t.Fatal("probe kept the test's configuration directory")
		}
	}
}
