// Package testsafe keeps a Go test binary away from the user's live Dyna: the
// data folder and engine socket its environment names when it starts. A test
// that sets its own folders later keeps them. Outside a test binary it
// changes nothing.
package testsafe

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
)

// rootEnv carries the first test binary's temporary folder to the test
// binaries it starts, which often get a TMPDIR of their own: they must keep
// the folders the first one chose, or parent and child no longer meet.
const rootEnv = "DYNA_TESTSAFE_ROOT"

// tempRoot is the temporary folder when the first test binary starts, which
// holds every t.TempDir. It is read once: tests set TMPDIR to their own
// folders.
var tempRoot = startRoot()

// rootMarker is the file a test binary leaves in its root. A child accepts
// an inherited root only when it holds the marker, so a stray or hand-set
// DYNA_TESTSAFE_ROOT, such as "/" or a real home, never widens the guard.
const rootMarker = ".dyna-testsafe-root"

func startRoot() string {
	if root := os.Getenv(rootEnv); filepath.IsAbs(root) {
		if _, err := os.Stat(filepath.Join(root, rootMarker)); err == nil {
			return root
		}
	}
	return os.TempDir()
}

// Check refuses a test's folder outside the temporary folder: a test never
// uses the user's own folders, such as the owner's live dyna.db or the CI
// user's.
func Check(dir string) error {
	if rel, err := filepath.Rel(tempRoot, dir); err != nil || !filepath.IsLocal(rel) {
		return fmt.Errorf("a test's dyna folder %s is outside the temporary folder %s; give the test a temporary one (storetest.DataDir)", dir, tempRoot)
	}
	return nil
}

// A test binary whose environment names a real data, config, cache or
// runtime folder, such as the owner's ~/.local/share, ~/.config (with
// settings.json) or /run/user/<uid>, starts with temporary ones instead.
// Processes the tests start, such as a real dyna command or turn runner,
// inherit them, so nothing reaches the live engine, dyna.db or settings.
// The temporary folders stay under TMPDIR after the run; nothing removes
// them.
func init() {
	if !testing.Testing() {
		return
	}
	if err := os.WriteFile(filepath.Join(tempRoot, rootMarker), nil, 0o600); err == nil {
		os.Setenv(rootEnv, tempRoot)
	}
	home := os.Getenv("HOME")
	for _, f := range []struct{ key, fallback string }{
		{"XDG_DATA_HOME", filepath.Join(home, ".local", "share")},
		{"XDG_CONFIG_HOME", filepath.Join(home, ".config")},
		{"XDG_CACHE_HOME", filepath.Join(home, ".cache")},
		{"XDG_RUNTIME_DIR", ""},
	} {
		dir := os.Getenv(f.key)
		if dir == "" {
			dir = f.fallback
		}
		if filepath.IsAbs(dir) && Check(dir) != nil {
			os.Setenv(f.key, temporary(f.key, dir))
		}
	}
}

func temporary(key, real string) string {
	dir, err := os.MkdirTemp(tempRoot, "dyna-test-")
	if err != nil {
		panic(fmt.Errorf("make a temporary %s: %w", key, err))
	}
	log.Printf("testsafe: %s %s is outside %s; this test binary uses %s", key, real, tempRoot, dir)
	return dir
}
