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
	"strconv"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/fsutil"
	"dyna-agent/internal/procident"
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

// tempPrefix names the temporary folders this package makes. A name carries
// the ID of the test binary that made it, so a later binary can remove the
// folders of a binary that has ended.
const tempPrefix = "dyna-test-"

// staleAfter is how long a folder of an ended test binary must stand before a
// later binary removes it: a binary that still runs keeps writing it, and a
// process ID can come back.
const staleAfter = 5 * time.Minute

func startRoot() string {
	if inheritedRoot() {
		return os.Getenv(rootEnv)
	}
	return os.TempDir()
}

// inheritedRoot reports whether this binary continues a test run that started
// earlier.
func inheritedRoot() bool {
	root := os.Getenv(rootEnv)
	if !filepath.IsAbs(root) {
		return false
	}
	_, err := os.Stat(filepath.Join(root, rootMarker))
	return err == nil
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
// The folders stay under TMPDIR after the run. The next test binary removes
// them, and maintenance removes the folders no later run reaches.
func init() {
	if !testing.Testing() {
		return
	}
	// Only the binary that owns the root removes what earlier runs left: a
	// child binary reuses its parent's folders and must not touch them.
	if !inheritedRoot() {
		pruneStale(tempRoot, time.Now(), procident.Alive)
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
	// Session scratch is a disk folder under the owner's cache, not a
	// temporary-folder variable, so a test must redirect it explicitly. Keep
	// this redirect quiet: hook tests compare a hook's captured log exactly,
	// and a diagnostic line there would look like hook output.
	scratchRoot := os.Getenv("DYNA_SCRATCH_ROOT")
	if scratchRoot == "" || Check(scratchRoot) != nil {
		os.Setenv("DYNA_SCRATCH_ROOT", temporaryDir())
	}
}

// temporaryDir makes a folder under the test root without logging.
func temporaryDir() string {
	dir, err := os.MkdirTemp(tempRoot, fmt.Sprintf("%s%d-", tempPrefix, os.Getpid()))
	if err != nil {
		panic(fmt.Errorf("make a temporary folder: %w", err))
	}
	return dir
}

func temporary(key, real string) string {
	dir, err := os.MkdirTemp(tempRoot, fmt.Sprintf("%s%d-", tempPrefix, os.Getpid()))
	if err != nil {
		panic(fmt.Errorf("make a temporary %s: %w", key, err))
	}
	log.Printf("testsafe: %s %s is outside %s; this test binary uses %s", key, real, tempRoot, dir)
	return dir
}

// pruneStale removes the folders that test binaries which have ended left in
// root, so a run of the suite does not fill the temporary folder. A folder of
// a binary that older Dyna builds made has no ID, and maintenance removes it.
func pruneStale(root string, now time.Time, alive func(int) bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		pid, ok := folderPID(entry.Name())
		if !ok || alive(pid) {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < staleAfter {
			continue
		}
		if err := fsutil.RemoveTree(filepath.Join(root, entry.Name())); err != nil {
			log.Printf("testsafe: remove %s: %v", entry.Name(), err)
		}
	}
}

// folderPID reads the ID of the test binary that made a temporary folder.
func folderPID(name string) (int, bool) {
	rest, ok := strings.CutPrefix(name, tempPrefix)
	if !ok {
		return 0, false
	}
	digits, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	pid, err := strconv.Atoi(digits)
	return pid, err == nil && pid > 0
}
