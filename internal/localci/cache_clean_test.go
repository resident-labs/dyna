package localci

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/machines"
	"dyna-agent/internal/shellquote"
)

// TestCacheCleanRefusesOtherPathsAndFindsItsTargets checks the one command a run
// sends to a CI machine: it must refuse every path outside Dyna's own
// directories, name the controller's directory rather than the account it is
// sent under, and select by age with one xdev find per tree.
func TestCacheCleanRefusesOtherPathsAndFindsItsTargets(t *testing.T) {
	command := cacheCleanCommand("hermeslab", true, ciCacheCleanAge)
	for _, keep := range []string{
		"-xdev", `-maxdepth 0 -mmin +"$age_min" ! -type l`, "rm -rf --one-file-system",
		"DYNA_CI_CLEAN_AGE_MIN=2880", "flock -n \"$1/.active\" true", `exec 8>>"$1/caches.lock"`, "flock -n 8",
	} {
		if !strings.Contains(command, keep) {
			t.Fatalf("cleanup command lacks %q", keep)
		}
	}
	// A cleanup must visit the tree the run writes to, which names the
	// controller's user ID, not the user ID of the SSH account.
	for _, root := range []string{cacheBase("hermeslab"), legacyCacheDir} {
		if !strings.Contains(command, root) {
			t.Fatalf("cleanup command does not visit %s", root)
		}
	}
	// A cleanup refuses to run as root, so its guards need an ordinary user.
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	for _, outside := range []string{"/home/pip/media", "/var/tmp", "/var/tmp/dyna-local-ci/..", "/srv/homelab"} {
		t.Run(outside, func(t *testing.T) {
			out, err := runCacheClean(t, outside, true)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("cleanup accepted %s: %v: %s", outside, err, out)
			}
			if !strings.Contains(string(out), "refusing") {
				t.Fatalf("cleanup refused %s without a reason: %s", outside, out)
			}
		})
	}
	// Dyna's own trees stay allowed, including one entry inside them.
	for _, inside := range []string{cacheBase("hermeslab"), legacyCacheDir + "/go-build", "/var/tmp/dyna-local-ci-1000/hermeslab"} {
		t.Run(inside, func(t *testing.T) {
			out, err := runCacheClean(t, inside, true)
			if err != nil || strings.Contains(string(out), "refusing") {
				t.Fatalf("cleanup refused Dyna's own path %s: %v: %s", inside, err, out)
			}
		})
	}
}

// runCacheClean runs the embedded script itself, with the roots a test chooses.
func runCacheClean(t *testing.T, roots string, dryRun bool) ([]byte, error) {
	t.Helper()
	dry := "0"
	if dryRun {
		dry = "1"
	}
	command := "DYNA_CI_CLEAN_DRY=" + dry + " DYNA_CI_CLEAN_ROOTS=" + shellquote.Quote(roots) +
		" bash -o pipefail -c " + shellquote.Quote(ciCacheCleanBody)
	return exec.Command("bash", "-c", command).CombinedOutput()
}

// TestCacheCleanRemovesOnlyWhatNoRunUses checks the liveness rule on a real tree
// of Dyna's own: a dead run's directory goes, a held run's stays, and a base
// whose cache lock a run holds keeps all of its caches. It also checks that a
// cleanup asked for one tree never visits the shared legacy tree.
func TestCacheCleanRemovesOnlyWhatNoRunUses(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d", os.Getpid()))
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	old := time.Now().Add(-72 * time.Hour)
	dirs := []string{
		filepath.Join(base, "runs", "finished"),
		filepath.Join(base, "runs", "crashed"),
		filepath.Join(base, "runs", "held"),
		filepath.Join(base, "go-mod", "module"),
		filepath.Join(base, "npm", "package"),
		filepath.Join(base, "diagnostics", "run"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	// A crashed run leaves its lock file behind; a dead holder leaves nothing.
	// The lock files come first: a new entry would date its directory today.
	for _, name := range []string{"crashed", "held"} {
		if err := os.WriteFile(filepath.Join(base, "runs", name, ".active"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range dirs {
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// The live run holds its own lock and the base's shared cache lock, as
	// runLocks does, until this test closes the pipe.
	holder := exec.Command("flock", "-n", filepath.Join(base, "runs", "held", ".active"),
		"flock", "-s", filepath.Join(base, "caches.lock"), "bash", "-c", "echo held; read -r line")
	stdin, err := holder.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Skipf("no flock to hold the test's lock: %v", err)
	}
	stopped := false
	stopHolder := func() {
		if stopped {
			return
		}
		stopped = true
		stdin.Close()
		holder.Wait()
	}
	t.Cleanup(stopHolder)
	if held, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || !strings.Contains(held, "held") {
		t.Fatalf("lock holder did not start: %q %v", held, err)
	}

	out, err := runCacheClean(t, base, false)
	if err != nil {
		t.Fatalf("cleanup failed: %v: %s", err, out)
	}
	for _, gone := range []string{"runs/finished", "runs/crashed", "diagnostics/run"} {
		if _, err := os.Stat(filepath.Join(base, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s stayed: %s", gone, out)
		}
	}
	for _, kept := range []string{"runs/held", "go-mod/module", "npm/package"} {
		if _, err := os.Stat(filepath.Join(base, kept)); err != nil {
			t.Fatalf("%s went while a run held it: %v: %s", kept, err, out)
		}
	}
	if !strings.Contains(string(out), "DYNA_CI_CACHE_CLEAN_FREED_KB=") {
		t.Fatalf("cleanup reported no total: %s", out)
	}
	// A cleanup for one tree never reaches into the shared legacy tree.
	for _, line := range strings.Split(string(out), "\n") {
		if fields := strings.Fields(line); len(fields) == 3 && strings.HasPrefix(fields[2], legacyCacheDir+"/") {
			t.Fatalf("cleanup visited the legacy tree it was not asked for: %s", line)
		}
	}

	// The same tree, now with no live run: its caches are old enough to go.
	stopHolder()
	out, err = runCacheClean(t, base, false)
	if err != nil {
		t.Fatalf("cleanup failed: %v: %s", err, out)
	}
	for _, gone := range []string{"runs/held", "go-mod/module", "npm/package"} {
		if _, err := os.Stat(filepath.Join(base, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s stayed with no live run: %s", gone, out)
		}
	}
}

// TestCleanCachesTicksEveryEnabledMachineOnceADay checks the daily worker: an
// engine with no CI machine gets no deadline, a machine enabled later gets one,
// and the next cleanup waits a day.
func TestCleanCachesTicksEveryEnabledMachineOnceADay(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var cleaned []string
	clean := func(_ context.Context, node Node) (CacheCleanResult, error) {
		cleaned = append(cleaned, node.Name)
		return CacheCleanResult{Node: node.Name, FreedBytes: 1 << 20}, nil
	}
	next, err := cleanCachesTick(t.Context(), dir, func() time.Time { return at }, clean)
	if err != nil || !next.IsZero() || len(cleaned) != 0 {
		t.Fatalf("empty startup: next=%v err=%v cleaned=%v", next, err, cleaned)
	}
	if _, err := (machines.Store{DataDir: dir}).Save(t.Context(), machines.Machine{
		Name: "node-a", SSH: "pip@node-a", Roles: []machines.Role{machines.RoleCI}, CISlots: 2, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	next, err = cleanCachesTick(t.Context(), dir, func() time.Time { return at }, clean)
	if err != nil || len(cleaned) != 1 || cleaned[0] != "node-a" || !next.Equal(at.Add(cacheCleanEvery)) {
		t.Fatalf("enabled machine: next=%v err=%v cleaned=%v", next, err, cleaned)
	}
	cleaned = nil
	next, err = cleanCachesTick(t.Context(), dir, func() time.Time { return at.Add(time.Hour) }, clean)
	if err != nil || len(cleaned) != 0 || !next.Equal(at.Add(cacheCleanEvery)) {
		t.Fatalf("second pass: next=%v err=%v cleaned=%v", next, err, cleaned)
	}
}
