package home

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	env := goProbeEnv([]string{"HOME=/owner", "PATH=/bin", "XDG_CONFIG_HOME=/tests/tree", "GOTELEMETRY=on"})
	for _, want := range []string{"HOME=/owner", "PATH=/bin", "XDG_CONFIG_HOME=" + goProbeConfigHome, "GOTELEMETRY=off"} {
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

// D-155 review: the isolated probe must still read the settings the owner saved
// with `go env -w`. Without GOENV, Go reads $XDG_CONFIG_HOME/go/env; replacing
// the configuration directory would hide a saved GOPATH, GOCACHE or GOFLAGS.
func TestGoDefaultsKeepsTheOwnersSavedGoSettings(t *testing.T) {
	path, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no Go toolchain in PATH")
	}
	config := t.TempDir()
	saved := filepath.Join(config, "go", "env")
	if err := os.MkdirAll(filepath.Dir(saved), 0o755); err != nil {
		t.Fatal(err)
	}
	gopath, gocache, gomodcache := filepath.Join(t.TempDir(), "gp"), filepath.Join(t.TempDir(), "gc"), filepath.Join(t.TempDir(), "gm")
	settings := "GOPATH=" + gopath + "\nGOCACHE=" + gocache + "\nGOMODCACHE=" + gomodcache + "\n"
	if err := os.WriteFile(saved, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	// No GOENV is exported, so only the saved file in the caller's configuration
	// directory can carry these paths.
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "XDG_CONFIG_HOME=" + config}
	got := goDefaults(filepath.Dir(path), env)
	if len(got) == 0 {
		t.Skip("the toolchain did not answer go env")
	}
	if got["GOPATH"] != gopath || got["GOCACHE"] != gocache || got["GOMODCACHE"] != gomodcache {
		t.Fatalf("probe ignored the owner's saved settings: %v", got)
	}
}

// An exported GOENV keeps its own file.
func TestGoProbeEnvKeepsAnExportedGoEnv(t *testing.T) {
	env := goProbeEnv([]string{"HOME=/owner", "GOENV=/custom/env"})
	found := false
	for _, pair := range env {
		if pair == "GOENV=/custom/env" {
			found = true
		}
	}
	if !found {
		t.Fatalf("probe dropped the exported GOENV: %v", env)
	}
}

// The probe's configuration directory must not be one the toolchain can write
// telemetry counters into, or a late write leaves a folder behind (D-155).
func TestGoProbeEnvConfigHomeCannotHoldFolders(t *testing.T) {
	for _, pair := range goProbeEnv([]string{"HOME=/owner"}) {
		if dir, ok := strings.CutPrefix(pair, "XDG_CONFIG_HOME="); ok {
			if err := os.MkdirAll(filepath.Join(dir, "go", "telemetry"), 0o755); err == nil {
				t.Fatalf("the toolchain can create folders under the probe's configuration directory %s", dir)
			}
			return
		}
	}
	t.Fatal("the probe has no XDG_CONFIG_HOME")
}
