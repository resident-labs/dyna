// Package testsafe keeps a Go test binary away from the user's live Dyna: the
// data folder and engine socket its environment names when it starts. It also
// disables automatic maintenance in temporary Git repositories. A test that
// sets its own folders later keeps them. Outside a test binary it changes nothing.
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
	// A Dyna session exports a disk TMPDIR whose path is too long for the unix
	// sockets that tests bind under t.TempDir(). Tests use the system folder.
	// Go's t.TempDir reads GOTMPDIR before TMPDIR, so every variable goes.
	if testing.Testing() {
		for _, name := range []string{"TMPDIR", "TMP", "TEMP", "GOTMPDIR"} {
			if strings.Contains(os.Getenv(name), "/.cache/dyna/tmp") {
				os.Unsetenv(name)
			}
		}
	}
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
	if rel, err := filepath.Rel(shortRoot, dir); err == nil && filepath.IsLocal(rel) && strings.HasPrefix(rel, shortPrefix) {
		return nil
	}
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
	DisableGitAutoMaintenance()
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
	// DYNA_DATA_DIR names the data folder directly and wins over XDG_DATA_HOME,
	// so one outside the temporary folder must go.
	if dir := os.Getenv("DYNA_DATA_DIR"); dir != "" && Check(dir) != nil {
		os.Unsetenv("DYNA_DATA_DIR")
	}
	// Session scratch uses a disk base under the owner's cache, not a
	// temporary-folder variable, so a test must redirect it explicitly. Keep
	// this redirect quiet: hook tests compare a hook's captured log exactly,
	// and a diagnostic line there would look like hook output.
	scratchRoot := os.Getenv("DYNA_SCRATCH_ROOT")
	if scratchRoot == "" || Check(scratchRoot) != nil {
		os.Setenv("DYNA_SCRATCH_ROOT", temporaryDir())
	}
}

// DisableGitAutoMaintenance prevents background Git processes from pruning or
// changing temporary repositories while a test uses them. Existing Git
// configuration from the environment stays in place.
func DisableGitAutoMaintenance() {
	SetGitConfig("gc.auto", "0")
	SetGitConfig("maintenance.auto", "false")
}

// SetGitConfig appends one Git command configuration without discarding
// entries inherited from the caller.
func SetGitConfig(key, value string) {
	count := gitConfigCount(os.Environ())
	_ = os.Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(count), key)
	_ = os.Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(count), value)
	_ = os.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(count+1))
}

// GitConfigWithoutAutoMaintenance adds the Git command configuration that
// DisableGitAutoMaintenance installs to an arbitrary process environment.
func GitConfigWithoutAutoMaintenance(env []string) []string {
	out := appendGitConfig(env, "gc.auto", "0")
	return appendGitConfig(out, "maintenance.auto", "false")
}

func appendGitConfig(env []string, key, value string) []string {
	count := gitConfigCount(env)
	return append(env,
		"GIT_CONFIG_KEY_"+strconv.Itoa(count)+"="+key,
		"GIT_CONFIG_VALUE_"+strconv.Itoa(count)+"="+value,
		"GIT_CONFIG_COUNT="+strconv.Itoa(count+1),
	)
}

func gitConfigCount(env []string) int {
	count, err := strconv.Atoi(environmentValue(env, "GIT_CONFIG_COUNT"))
	if err != nil || count < 0 {
		return 0
	}
	return count
}

func environmentValue(env []string, key string) string {
	value := ""
	for _, entry := range env {
		entryKey, entryValue, _ := strings.Cut(entry, "=")
		if entryKey == key {
			value = entryValue
		}
	}
	return value
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

// shortRoot holds the folders ShortTemp makes. A unix socket path is limited to
// 108 bytes, and a test root can be deeper than that leaves room for, so these
// folders stand beside the root and Check accepts them.
const shortRoot = "/tmp"

// shortPrefix starts the name of every folder ShortTemp makes; Check accepts
// only these under shortRoot, never the rest of /tmp.
const shortPrefix = "dt-"

// ShortTemp makes a folder with a short path and a name that starts with
// "dt-" and then prefix. Check accepts it, so a test binary that a test starts
// (a computer daemon) keeps it: testsafe replaces every XDG folder that Check
// refuses, and a daemon that lost its runtime folder that way listens where its
// client does not look.
func ShortTemp(prefix string) (string, error) {
	return os.MkdirTemp(shortRoot, shortPrefix+prefix)
}

// ShortTempDir is ShortTemp for a test, removed when the test ends. A unix
// socket path is limited to 108 bytes, and t.TempDir adds the test's name to a
// TMPDIR that a CI check already points deep into /tmp, so a test that binds a
// socket (an XDG_RUNTIME_DIR, for one) uses this.
func ShortTempDir(t testing.TB) string {
	t.Helper()
	dir, err := ShortTemp("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
