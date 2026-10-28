package localci

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	command := cacheCleanCommand(Node{Name: "hermeslab"}, true, ciCacheCleanAge)
	for _, keep := range []string{
		"-xdev", `-maxdepth 0 -mmin +"$age" ! -type l`, "rm -rf --one-file-system",
		"DYNA_CI_CLEAN_AGE_MIN=2880", "DYNA_CI_CLEAN_RUN_AGE_MIN=120", "flock -n \"$1/.active\" true", `exec 8>>"$base/caches.lock"`, "flock -n 8",
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
	// Dyna's own trees stay allowed, including one entry inside them. The test
	// uses its own tree: a real CI tree may hold a cache lock this test cannot
	// take, and a test must never touch a live CI cache.
	parent := filepath.Dir(cacheBase("x"))
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatal(err)
	}
	base, err := os.MkdirTemp(parent, "selftest-allowed-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	inner := filepath.Join(base, "go-build")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, inside := range []string{base, inner} {
		t.Run(inside, func(t *testing.T) {
			out, err := runCacheClean(t, inside, true)
			if err != nil || strings.Contains(string(out), "refusing") {
				t.Fatalf("cleanup refused Dyna's own path %s: %v: %s", inside, err, out)
			}
		})
	}
}

// runCacheClean runs the embedded script itself, with the roots a test chooses.
func runCacheClean(t *testing.T, roots string, dryRun bool, env ...string) ([]byte, error) {
	t.Helper()
	return runCacheCleanBody(t, roots, dryRun, ciCacheCleanBody, env...)
}

func runCacheCleanBody(t *testing.T, roots string, dryRun bool, body string, env ...string) ([]byte, error) {
	t.Helper()
	dry := "0"
	if dryRun {
		dry = "1"
	}
	prefix := ""
	if len(env) > 0 {
		prefix = strings.Join(env, " ") + " "
	}
	command := prefix + "DYNA_CI_CLEAN_DRY=" + dry + " DYNA_CI_CLEAN_ROOTS=" + shellquote.Quote(roots) +
		" bash -o pipefail -c " + shellquote.Quote(body)
	return exec.Command("bash", "-c", command).CombinedOutput()
}

// TestCacheCleanUsesRuntimeWhenHostRemovalFails reproduces a run folder whose
// contents the node user cannot remove. The cleanup mounts its parent in a
// throwaway container and removes it there, without returning rm's error flood.
func TestCacheCleanUsesRuntimeWhenHostRemovalFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-container", os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(base) })
	runDir := filepath.Join(base, "runs", "old")
	if err := os.MkdirAll(filepath.Join(runDir, "web", "node_modules"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(runDir, old, old); err != nil {
		t.Fatal(err)
	}
	realRM, err := exec.LookPath("rm")
	if err != nil {
		t.Skipf("rm is unavailable: %v", err)
	}
	bin := t.TempDir()
	fakeRM := "#!/bin/sh\n" +
		"if [ \"$1\" = -rf ]; then echo 'rm: cannot remove web/node_modules: Permission denied' >&2; exit 1; fi\n" +
		"exec \"$REAL_RM\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "rm"), []byte(fakeRM), 0o700); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(t.TempDir(), "docker-args")
	fakeDocker := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" >> \"$DOCKER_ARGS\"\n" +
		"[ \"$1\" = run ] || exit 0\n" +
		"mount=; last=\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in -v) mount=$2; shift 2 ;; *) last=$1; shift ;; esac; done\n" +
		"parent=${mount%:/dyna-ci-clean}\n" +
		"exec \"$REAL_RM\" -rf --one-file-system -- \"$parent/${last#/dyna-ci-clean/}\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runCacheClean(t, base, false,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"REAL_RM="+realRM,
		"DOCKER_ARGS="+args)
	if err != nil {
		t.Fatalf("container fallback failed: %v: %s", err, out)
	}
	if strings.Contains(string(out), "Permission denied") {
		t.Fatalf("a successful fallback returned the host rm failure: %s", out)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("the runtime left the old run folder behind: %v: %s", err, out)
	}
	data, err := os.ReadFile(args)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"run", "--rm", "--network", "none", "debian:12-slim", "--one-file-system",
		filepath.Join(base, "runs") + ":/dyna-ci-clean", "/dyna-ci-clean/old"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("runtime args %q miss %q", data, want)
		}
	}
}

// TestRunKeeperExitRemovesItsFolder covers a teardown interrupted just after
// it stops the keeper. The keeper's own exit cleanup must remove the run,
// rather than leaving an unlocked folder for the scheduled sweep.
func TestRunKeeperExitRemovesItsFolder(t *testing.T) {
	active := filepath.Join(t.TempDir(), "runs", "run", ".active")
	if err := os.MkdirAll(filepath.Dir(active), 0700); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command("bash", "-c", runKeeper(active)).CombinedOutput(); err != nil {
		t.Fatalf("run keeper: %v: %s", err, output)
	}
	if _, err := os.Stat(filepath.Dir(active)); !os.IsNotExist(err) {
		t.Fatalf("run folder survived keeper exit: %v", err)
	}
}

// TestStoppingRunKeeperWaitsOnTheSameLock makes a controlled keeper unlink
// .active before teardown's first probe. Teardown must hold that inode open
// before signalling; reopening the pathname can acknowledge a different file.
// Pipe handshakes control both processes, so the test has no sleeps or polling.
func TestStoppingRunKeeperWaitsOnTheSameLock(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base")
	runDir := filepath.Join(base, "runs", "run")
	active := filepath.Join(runDir, ".active")
	if err := os.MkdirAll(runDir, 0700); err != nil {
		t.Fatal(err)
	}
	control, ack := filepath.Join(base, "control"), filepath.Join(base, "ack")
	for _, fifo := range []string{control, ack} {
		if output, err := exec.Command("mkfifo", fifo).CombinedOutput(); err != nil {
			t.Fatalf("make test pipe: %v: %s", err, output)
		}
	}
	// The controlled keeper takes the same run and cache locks as runLocks. Its
	// exit trap unlinks the run and closes both locks before acknowledging exit.
	cleanup := "rm -rf --one-file-system " + shellquote.Quote(runDir) +
		"; exec 6>&-; exec 7>&-; printf 'done\\n' > " + shellquote.Quote(ack)
	keeperScript := "set -e; exec 6>>" + shellquote.Quote(active) + "; flock -x 6; " +
		"exec 7>>" + shellquote.Quote(filepath.Join(base, "caches.lock")) + "; flock -s 7; " +
		"exec 5<>" + shellquote.Quote(control) + "; trap " + shellquote.Quote(cleanup) + " EXIT; " +
		"printf 'ready\\n'; read -r _ <&5"
	keeper := exec.Command("bash", "-c", keeperScript)
	stdout, err := keeper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := keeper.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = keeper.Process.Kill()
			_ = keeper.Wait()
		}
	})
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("keeper did not take its locks: %q %v", line, err)
	}
	if err := os.WriteFile(keeperPID(active), []byte(strconv.Itoa(keeper.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	flock, err := exec.LookPath("flock")
	if err != nil {
		t.Skipf("flock is unavailable: %v", err)
	}
	// Before the descriptor probe, wait until the keeper has unlinked the run
	// and released its locks. The old pathname probe misses this handshake.
	wrapper := "#!/bin/sh\nprintf 'call\\n' >> \"$FLOCK_COUNT\"\n" +
		"if [ \"$1\" = -n ] && [ \"$2\" = 9 ]; then IFS= read -r _ < \"$KEEPER_ACK\"; fi\n" +
		"exec " + shellquote.Quote(flock) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "flock"), []byte(wrapper), 0700); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(t.TempDir(), "calls")
	cmd := exec.Command("bash", "-c", stopKeeper(base, active))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "FLOCK_COUNT="+count, "KEEPER_ACK="+ack)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stop run keeper: %v: %s", err, output)
	}
	stopped = true
	_ = keeper.Wait()
	data, err := os.ReadFile(count)
	if err != nil {
		t.Fatal(err)
	}
	if calls := strings.Count(string(data), "call\n"); calls != 1 {
		t.Fatalf("teardown reopened .active after its deletion: %d flock calls, want one", calls)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("run folder survived keeper teardown: %v", err)
	}
}

func TestCacheCleanTrimsRepositoryCacheNamespace(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-pip", os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(base) })
	oldEntry := filepath.Join(base, "caches", "pip", "old")
	newEntry := filepath.Join(base, "caches", "pip", "new")
	for _, entry := range []string{oldEntry, newEntry} {
		if err := os.MkdirAll(entry, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-73 * time.Hour)
	if err := os.Chtimes(oldEntry, old, old); err != nil {
		t.Fatal(err)
	}
	out, err := runCacheClean(t, base, false)
	if err != nil {
		t.Fatalf("cleanup failed: %v: %s", err, out)
	}
	if _, err := os.Stat(oldEntry); !os.IsNotExist(err) {
		t.Fatalf("old repository cache entry remains: %v", err)
	}
	if _, err := os.Stat(newEntry); err != nil {
		t.Fatalf("recent repository cache entry was removed: %v", err)
	}
}

// TestCacheCleanCapsRecentCacheSize bounds a cache by its own size, not only by
// the age of its hot parent directory. A recent cache that is over its cap
// loses its oldest entry and keeps the newest, a live run's shared cache lock
// keeps every entry, and the cleanup reports what it removed.
func TestCacheCleanCapsRecentCacheSize(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-cap", os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(base) })
	oldEntry := filepath.Join(base, "go-build", "aa")
	newEntry := filepath.Join(base, "go-build", "bb")
	now := time.Now()
	write := func(path string, at time.Time) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 64<<10), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	// A cache entry can be a directory, as Go keeps its shard folders, or a
	// plain file, as Go keeps some cache objects. Both are trimmed by their own
	// mtime, not by the age of the hot cache directory around them.
	if err := os.MkdirAll(oldEntry, 0700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(oldEntry, "object"), now.Add(-time.Hour))
	if err := os.Chtimes(oldEntry, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	write(newEntry, now)
	// The cap holds one 64 KiB entry and its directory block, not two.
	const capEnv = "DYNA_CI_CACHE_CAP_KB=100"

	// A run that holds the base's shared cache lock keeps every entry.
	holder := exec.Command("flock", "-s", filepath.Join(base, "caches.lock"), "bash", "-c", "echo held; read -r line")
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
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		stdin.Close()
		holder.Wait()
	}
	t.Cleanup(release)
	if held, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || !strings.Contains(held, "held") {
		t.Fatalf("lock holder did not start: %q %v", held, err)
	}
	out, err := runCacheClean(t, base, false, capEnv)
	if err != nil {
		t.Fatalf("cleanup failed: %v: %s", err, out)
	}
	for _, kept := range []string{oldEntry, newEntry} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("cleanup trimmed a cache a live run holds: %v: %s", err, out)
		}
	}

	release()
	// A dry run reports the entry it would remove and deletes nothing.
	out, err = runCacheClean(t, base, true, capEnv)
	if err != nil {
		t.Fatalf("dry run failed: %v: %s", err, out)
	}
	if paths, _ := parseCacheClean(string(out)); len(paths) != 1 || paths[0] != oldEntry {
		t.Fatalf("dry run reported %v, want only %s: %s", paths, oldEntry, out)
	}
	if _, err := os.Stat(oldEntry); err != nil {
		t.Fatalf("dry run removed an entry: %v: %s", err, out)
	}

	out, err = runCacheClean(t, base, false, capEnv)
	if err != nil {
		t.Fatalf("cleanup failed: %v: %s", err, out)
	}
	if _, err := os.Stat(oldEntry); !os.IsNotExist(err) {
		t.Fatalf("the oldest entry stayed over the cap: %v: %s", err, out)
	}
	if _, err := os.Stat(newEntry); err != nil {
		t.Fatalf("cleanup removed the newest entry, not the oldest: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "DYNA_CI_CACHE_CLEAN_ITEM") || !strings.Contains(string(out), oldEntry) {
		t.Fatalf("cleanup did not report the entry it removed: %s", out)
	}
	paths, freed := parseCacheClean(string(out))
	if len(paths) != 1 || paths[0] != oldEntry || freed <= 0 {
		t.Fatalf("cleanup reported paths=%v freed=%d, want only %s: %s", paths, freed, oldEntry, out)
	}
}

// TestCacheCleanTrimsCachesByTheirOwnShards checks that a size cap removes one
// cache unit of the kind that cache keeps, not a whole cache. A Go module
// cache keeps extracted modules and their download directories; an npm cache
// keeps hash shards four levels down; a build cache keeps its shards at the
// top. The older unit goes whole and its newer sibling stays.
func TestCacheCleanTrimsCachesByTheirOwnShards(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	t.Run("npm content", func(t *testing.T) {
		base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-npm", os.Getpid()))
		t.Cleanup(func() { removeTree(t, base) })
		now := time.Now()
		write := func(entry string, at time.Time) string {
			t.Helper()
			dir := filepath.Join(base, "npm", entry)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "object"), make([]byte, 64<<10), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(dir, at, at); err != nil {
				t.Fatal(err)
			}
			return dir
		}
		old := write("_cacache/content-v2/sha512/aa", now.Add(-time.Hour))
		newer := write("_cacache/content-v2/sha512/bb", now)
		out, err := runCacheClean(t, base, false, "DYNA_CI_CACHE_CAP_KB=100")
		if err != nil {
			t.Fatalf("cleanup failed: %v: %s", err, out)
		}
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Fatalf("the oldest shard stayed over the cap: %v: %s", err, out)
		}
		if _, err := os.Stat(newer); err != nil {
			t.Fatalf("cleanup removed a newer shard with its older sibling: %v: %s", err, out)
		}
	})

	t.Run("go modules", func(t *testing.T) {
		base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-go-modules", os.Getpid()))
		t.Cleanup(func() { removeTree(t, base) })
		root := filepath.Join(base, "go-mod")
		now := time.Now()
		// writeUnit makes one unit of 64 KiB, dated by its own mtime.
		writeUnit := func(rel string, at time.Time) string {
			t.Helper()
			dir := filepath.Join(root, rel)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "object"), make([]byte, 1<<20), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(dir, at, at); err != nil {
				t.Fatal(err)
			}
			return dir
		}
		// A download unit, a direct VCS unit, a short module root, a deep /v2
		// module root and a read-only module with a nested directory all go; the
		// newest module stays whole. No module may survive as a partially deleted
		// directory. Units are 1 MiB so directory overhead does not change the cap
		// choice.
		download := writeUnit(filepath.Join("cache", "download", "gopkg.in", "yaml.v2", "@v"), now.Add(-4*time.Hour))
		short := writeUnit("gopkg.in/yaml.v2@v2.4.0", now.Add(-3*time.Hour))
		deep := writeUnit("github.com/foo/bar/v2@v2.1.0", now.Add(-2*time.Hour))
		vcs := writeUnit(filepath.Join("cache", "vcs", "0123456789abcdef"), now.Add(-90*time.Minute))
		if err := os.MkdirAll(filepath.Join(vcs, "objects", "pack"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(vcs, "objects", "pack", "pack-1.pack"), make([]byte, 4096), 0o644); err != nil {
			t.Fatal(err)
		}
		// Adding the checkout contents dates the unit again; keep its own age.
		if err := os.Chtimes(vcs, now.Add(-90*time.Minute), now.Add(-90*time.Minute)); err != nil {
			t.Fatal(err)
		}
		readonly := writeUnit("go.uber.org/zap@v1.24.0", now.Add(-time.Hour))
		if err := os.MkdirAll(filepath.Join(readonly, "internal", "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(readonly, "internal", "sub", "nested.go"), make([]byte, 4096), 0o644); err != nil {
			t.Fatal(err)
		}
		// Adding the nested directory dates the module again; keep its own age.
		if err := os.Chtimes(readonly, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		makeReadOnly(t, readonly)
		kept := writeUnit("gopkg.in/yaml.v3@v3.0.1", now)

		out, err := runCacheClean(t, base, false, "DYNA_CI_CACHE_CAP_KB=1500")
		if err != nil {
			t.Fatalf("cleanup failed on a read-only module: %v: %s", err, out)
		}
		paths, freed := parseCacheClean(string(out))
		want := []string{download, short, deep, vcs, readonly}
		if len(paths) != len(want) {
			t.Fatalf("removed %v, want the module units %v: %s", paths, want, out)
		}
		for i, path := range want {
			if paths[i] != path {
				t.Fatalf("removed %v, want the module units %v: %s", paths, want, out)
			}
		}
		if freed <= 0 {
			t.Fatalf("cleanup reported no freed space: %s", out)
		}
		for _, gone := range want {
			if _, err := os.Stat(gone); !os.IsNotExist(err) {
				t.Fatalf("a trimmed unit remains, so its module is partial: %s: %v", gone, err)
			}
		}
		info, err := os.Stat(filepath.Join(kept, "object"))
		if err != nil || info.Size() != 1<<20 {
			t.Fatalf("the newest module was not kept whole: %v", err)
		}
	})
}

// TestCacheCleanFailsWhenATrimCannotRemove checks that a failed trim ends the
// cleanup with an error and no success total, so a caller never records a
// cleanup that left a module half removed. A read-only cache root makes the
// unit's parent refuse the unlink.
func TestCacheCleanFailsWhenATrimCannotRemove(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-trim-failure", os.Getpid()))
	t.Cleanup(func() { removeTree(t, base) })
	root := filepath.Join(base, "go-mod")
	unit := filepath.Join(root, "example.com/foo@v1.0.0")
	if err := os.MkdirAll(unit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unit, "object"), make([]byte, 64<<10), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(unit), 0o555); err != nil {
		t.Fatal(err)
	}
	// The host cannot unlink the unit. Make the container fallback fail too,
	// so the cleanup still has to return its one structured failure.
	bin := t.TempDir()
	fakeDocker := "#!/bin/sh\n[ \"$1\" = info ] && exit 0\necho 'permission denied' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(fakeDocker), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runCacheClean(t, base, false, "DYNA_CI_CACHE_CAP_KB=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("cleanup reported success after a failed trim: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "DYNA_CI_CACHE_CLEAN_ERROR") || !strings.Contains(string(out), unit) || !strings.Contains(string(out), "permission denied") {
		t.Fatalf("cleanup did not name the unit and reason it could not remove: %s", out)
	}
	if strings.Contains(string(out), "FREED_KB=") {
		t.Fatalf("cleanup printed a success total after a failure: %s", out)
	}
}

// TestCacheCleanRefusesAnUnboundedCache checks that a cache whose remaining
// data has no trim unit ends the cleanup with an error, not with a successful
// bound. The sum database is such data: it is not a module, a download unit or
// a VCS checkout.
func TestCacheCleanRefusesAnUnboundedCache(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-residual", os.Getpid()))
	t.Cleanup(func() { removeTree(t, base) })
	sumdb := filepath.Join(base, "go-mod", "cache", "download", "sumdb", "sum.golang.org")
	if err := os.MkdirAll(sumdb, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sumdb, "latest"), make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runCacheClean(t, base, false, "DYNA_CI_CACHE_CAP_KB=1")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("cleanup claimed a bound it did not reach: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "no trimmable unit remains") || !strings.Contains(string(out), filepath.Join(base, "go-mod")) {
		t.Fatalf("cleanup did not explain the residual data: %s", out)
	}
	if strings.Contains(string(out), "FREED_KB=") {
		t.Fatalf("cleanup printed a success total after a failed bound: %s", out)
	}
}

// makeReadOnly makes every directory below dir read-only, as Go leaves an
// extracted module, and every file read-only too.
func makeReadOnly(t *testing.T, dir string) {
	t.Helper()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return os.Chmod(path, 0o555)
		}
		return os.Chmod(path, 0o444)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// removeTree removes a test tree that may hold read-only module directories.
func removeTree(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// TestCacheCleanValidatesALinkedTreeBeforeChangingIt checks that a base reached
// through a linked parent never changes the external tree. The read-only module
// keeps its mode and file, and the cleanup creates no lock there.
func TestCacheCleanValidatesALinkedTreeBeforeChangingIt(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	realParent := filepath.Join(t.TempDir(), "real")
	base := filepath.Join(realParent, "node")
	module := filepath.Join(base, "go-mod", "example.com", "module@v1.0.0")
	if err := os.MkdirAll(module, 0o755); err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(module, "object")
	if err := os.WriteFile(object, make([]byte, 1<<20), 0o644); err != nil {
		t.Fatal(err)
	}
	makeReadOnly(t, module)
	t.Cleanup(func() { removeTree(t, realParent) })
	linkParent := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-linked-parent", os.Getpid()))
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(linkParent) })

	out, err := runCacheClean(t, filepath.Join(linkParent, "node"), false, "DYNA_CI_CACHE_CAP_KB=1")
	if err == nil {
		t.Fatalf("cleanup accepted a base under a linked parent: %s", out)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("cleanup exit = %v: %s", err, out)
	}
	if !strings.Contains(string(out), "linked parent") {
		t.Fatalf("cleanup did not name the linked parent: %s", out)
	}
	info, err := os.Stat(module)
	if err != nil {
		t.Fatalf("cleanup changed the external module: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o555 {
		t.Fatalf("external module permissions changed to %04o", perm)
	}
	if data, err := os.ReadFile(object); err != nil || len(data) != 1<<20 {
		t.Fatalf("external module file changed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "caches.lock")); !os.IsNotExist(err) {
		t.Fatalf("cleanup created a lock in the external tree: %v", err)
	}
}

func TestCacheCleanPrunesRunFoldersAfterTwoHours(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-run-age", os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(base) })
	now := time.Now()
	for name, modified := range map[string]time.Time{
		"runs/old":                         now.Add(-3 * time.Hour),
		"runs/recent":                      now.Add(-1 * time.Hour),
		"go-mod/example.com/module@v1.0.0": now.Add(-25 * time.Hour),
		"repo.git":                         now.Add(-25 * time.Hour),
	} {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	// Relocate only the legacy root so the test exercises its top-level sweep
	// without touching the machine's real legacy tree.
	body := strings.Replace(ciCacheCleanBody, "legacy=/var/tmp/dyna-local-ci", "legacy="+shellquote.Quote(base), 1)
	if body == ciCacheCleanBody {
		t.Fatal("could not relocate the legacy cleanup root")
	}
	out, err := runCacheCleanBody(t, base, false, body)
	if err != nil {
		t.Fatalf("cleanup failed: %v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(base, "runs", "old")); !os.IsNotExist(err) {
		t.Fatalf("run folder older than two hours stayed: %v: %s", err, out)
	}
	for _, kept := range []string{"runs/recent", "go-mod/example.com/module@v1.0.0", "repo.git"} {
		if _, err := os.Stat(filepath.Join(base, kept)); err != nil {
			t.Fatalf("cleanup removed %s before its retention age: %v: %s", kept, err, out)
		}
	}
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
		filepath.Join(base, "go-mod", "example.com", "module@v1.0.0"),
		filepath.Join(base, "npm", "_cacache", "content-v2", "sha512", "aa"),
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
	for _, kept := range []string{"runs/held", "go-mod/example.com/module@v1.0.0", "npm/_cacache/content-v2/sha512/aa"} {
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
	for _, gone := range []string{"runs/held", "go-mod/example.com/module@v1.0.0", "npm/_cacache/content-v2/sha512/aa"} {
		if _, err := os.Stat(filepath.Join(base, gone)); !os.IsNotExist(err) {
			t.Fatalf("%s stayed with no live run: %s", gone, out)
		}
	}
}

// TestCacheCleanFreesCachesRightAfterARunEnds runs the cleanup against the
// locks a run holds. Two runs hold one base's caches between them, one ends,
// and the old caches go only when the last is gone. Readiness and release
// travel over pipes and process exit, so the test never waits for a poll.
func TestCacheCleanFreesCachesRightAfterARunEnds(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("the cleanup refuses to run as root")
	}
	base := filepath.Join(filepath.Dir(cacheBase("x")), fmt.Sprintf("selftest-%d-lifecycle", os.Getpid()))
	t.Cleanup(func() { os.RemoveAll(base) })
	old := time.Now().Add(-72 * time.Hour)
	caches := []string{filepath.Join(base, "go-mod", "example.com", "module@v1.0.0"), filepath.Join(base, "npm", "_cacache", "content-v2", "sha512", "aa")}
	for _, dir := range append([]string{filepath.Join(base, "runs")}, caches...) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	// holdRun keeps one run's locks: the base's cache lock shared and the run's
	// own lock exclusive, which is what a run holds while it works. Its pipe
	// reports readiness and its closing ends the run, as runLocks and the run's
	// teardown do on the node.
	holdRun := func(name string) func() {
		t.Helper()
		active := filepath.Join(base, "runs", name, ".active")
		if err := os.MkdirAll(filepath.Dir(active), 0700); err != nil {
			t.Fatal(err)
		}
		script := `exec 7>>"$0" || exit 1; flock -s 7 || exit 1; exec 6>>"$1" || exit 1; flock -x 6 || exit 1; echo ready; read -r _`
		cmd := exec.Command("bash", "-c", script, filepath.Join(base, "caches.lock"), active)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ended := false
		end := func() {
			if ended {
				return
			}
			ended = true
			stdin.Close()
			cmd.Wait()
		}
		t.Cleanup(end)
		if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || !strings.Contains(line, "ready") {
			t.Fatalf("run %s did not take its locks: %q %v", name, line, err)
		}
		return end
	}
	first, second := holdRun("one"), holdRun("two")

	// A cleanup under the runs frees no cache.
	if out, err := runCacheClean(t, base, false); err != nil || strings.Contains(string(out), "go-mod") {
		t.Fatalf("cleanup trimmed caches under a live run: %v: %s", err, out)
	}
	// The first run ends; the second still holds the caches, so they stay.
	first()
	if out, err := runCacheClean(t, base, false); err != nil || strings.Contains(string(out), "go-mod") {
		t.Fatalf("cleanup trimmed caches that another run still mounts: %v: %s", err, out)
	}
	// The last run ends, and the cleanup called right after frees them.
	second()
	if err := exec.Command("flock", "-n", filepath.Join(base, "caches.lock"), "true").Run(); err != nil {
		t.Fatalf("a keeper still held the base cache lock after the last teardown: %v", err)
	}
	out, err := runCacheClean(t, base, false)
	if err != nil {
		t.Fatalf("cleanup after the last run: %v: %s", err, out)
	}
	for _, dir := range caches {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s stayed after the last run ended: %s", filepath.Base(dir), out)
		}
	}
}

// TestCleanCachesTicksEveryEnabledMachineOnceAnHour checks the hourly worker:
// an engine with no CI machine gets no deadline, a machine enabled later gets
// one, and the next cleanup waits an hour.
func TestCleanCachesTicksEveryEnabledMachineOnceAnHour(t *testing.T) {
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
	next, err = cleanCachesTick(t.Context(), dir, func() time.Time { return at.Add(30 * time.Minute) }, clean)
	if err != nil || len(cleaned) != 0 || !next.Equal(at.Add(cacheCleanEvery)) {
		t.Fatalf("second pass: next=%v err=%v cleaned=%v", next, err, cleaned)
	}
}
