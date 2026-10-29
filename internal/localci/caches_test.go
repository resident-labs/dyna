package localci

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/shellquote"
)

// TestCleanBeforeRunFollowsTheConfiguredDiskFloor checks that cache recovery
// uses machines.ciDiskFloorGB. It also checks the two edges: a settings file
// that cannot be read keeps the default floor, and a zero floor turns recovery
// off.
func TestCleanBeforeRunFollowsTheConfiguredDiskFloor(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings string
		freeGiB  int
		cleaned  bool
	}{
		{"configured floor cleans below it", `{"version":1,"machines":{"ciDiskFloorGB":40}}`, 30, true},
		{"configured floor keeps space above it", `{"version":1,"machines":{"ciDiskFloorGB":40}}`, 50, false},
		{"zero floor never recovers", `{"version":1,"machines":{"ciDiskFloorGB":0}}`, 1, false},
		{"broken settings keep the default floor", `{`, 10, true},
		{"no settings file keeps the default floor", "", 30, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", config)
			if tc.settings != "" {
				dir := filepath.Join(config, "dyna")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(tc.settings), 0600); err != nil {
					t.Fatal(err)
				}
			}
			node := Node{Name: "local", Slots: 1, Enabled: true}
			cleaned := false
			runner := Runner{DataDir: t.TempDir(), Node: &node}
			legacy := func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, _ string, args ...string) error {
				script := strings.Join(args, " ")
				switch {
				case strings.Contains(script, "df -Pk"):
					fmt.Fprintf(out, "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 1048576 1 %d 1%% /var/tmp\n", tc.freeGiB<<20)
				case strings.Contains(script, "DYNA_CI_CACHE_CLEAN"):
					cleaned = true
					fmt.Fprintln(out, "DYNA_CI_CACHE_CLEAN_FREED_KB=1024")
				}
				return nil
			}
			newFakeMachine(t, legacy).attach(&runner)
			if err := runner.cleanBeforeRun(t.Context(), node); err != nil {
				t.Fatal(err)
			}
			if cleaned != tc.cleaned {
				t.Fatalf("cleaned = %v, want %v for %d GiB free", cleaned, tc.cleaned, tc.freeGiB)
			}
		})
	}
}

// TestCleanCachesReturnsOneBoundedFailure checks the public cleanup boundary:
// the caller gets the managed path and one reason, not rm's output flood.
func TestCleanCachesReturnsOneBoundedFailure(t *testing.T) {
	node := Node{Name: "local", Slots: 1, Enabled: true}
	runner := Runner{Node: &node}
	path := "/var/tmp/dyna-local-ci/runs/old/web/node_modules"
	legacy := func(_ context.Context, _ string, _ io.Reader, _, stderr io.Writer, _ string, _ ...string) error {
		fmt.Fprintf(stderr, "DYNA_CI_CACHE_CLEAN_ERROR\t%s\tpermission denied\n", path)
		fmt.Fprint(stderr, "rm: cannot remove every child: Permission denied\n")
		return errors.New("exit status 1")
	}
	newFakeMachine(t, legacy).attach(&runner)
	_, err := runner.CleanCaches(t.Context(), node, false)
	var failure *CacheCleanError
	if !errors.As(err, &failure) {
		t.Fatalf("cleanup error = %v, want CacheCleanError", err)
	}
	if failure.Path != path || failure.Reason != "permission denied" {
		t.Fatalf("cleanup failure = %+v", failure)
	}
	if strings.Contains(err.Error(), "every child") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("cleanup error returned raw output: %q", err)
	}
}

// TestCacheRepairPreservesLinkedCaches checks the prepare stage of a run on a
// temporary base. A go-build link that resolves outside the base waits for the
// run that holds the shared cache lock, then becomes a real empty directory
// and keeps the link itself as .before-d48, with its target untouched. A run
// that finds real directories takes no lock and prints no repair fact.
func TestCacheRepairPreservesLinkedCaches(t *testing.T) {
	base := filepath.Join(t.TempDir(), "base")
	if err := os.MkdirAll(repositoryCacheRoot(base), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "host-go-build")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "keep")
	if err := os.WriteFile(marker, []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := repositoryCachePath(base, "go-build")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	realMod := repositoryCachePath(base, "go-mod")
	if err := os.MkdirAll(realMod, 0o700); err != nil {
		t.Fatal(err)
	}

	// A live run holds the shared cache lock, as runLocks does, until this
	// test closes its pipe.
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
	releaseHolder := func() {
		if released {
			return
		}
		released = true
		stdin.Close()
		holder.Wait()
	}
	t.Cleanup(releaseHolder)
	if held, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || !strings.Contains(held, "held") {
		t.Fatalf("lock holder did not start: %q %v", held, err)
	}

	// A fake flock signals on inherited fd 3 before it runs the real one, so the
	// test knows the repair stage reached the lock without any sleep or poll.
	realFlock, err := exec.LookPath("flock")
	if err != nil {
		t.Skipf("no real flock: %v", err)
	}
	bin := t.TempDir()
	wrapper := "#!/bin/sh\nprintf 'called\\n' >&3\nexec " + shellquote.Quote(realFlock) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "flock"), []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	signalRead, signalWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer signalRead.Close()
	var out bytes.Buffer
	repair := exec.Command("bash", "-c", cacheRepairScript(base, time.Minute))
	repair.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	repair.ExtraFiles = []*os.File{signalWrite}
	repair.Stdout, repair.Stderr = &out, &out
	if err := repair.Start(); err != nil {
		t.Fatal(err)
	}
	signalWrite.Close()
	done := make(chan error, 1)
	go func() { done <- repair.Wait() }()
	started := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(signalRead).ReadString('\n')
		started <- line
	}()
	select {
	case err := <-done:
		t.Fatalf("repair did not wait for the cache lock: %v: %s", err, out.String())
	case line := <-started:
		if !strings.Contains(line, "called") {
			t.Fatalf("repair stage did not take the cache lock: %q", line)
		}
	}
	// The link is still in place while the live run holds the shared lock.
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("repair changed the link before the live run ended: %v", err)
	}
	if _, err := os.Lstat(link + ".before-d48"); !os.IsNotExist(err) {
		t.Fatalf("repair made a backup before the live run ended: %v", err)
	}

	releaseHolder()
	if err := <-done; err != nil {
		t.Fatalf("repair failed: %v: %s", err, out.String())
	}
	// The link is preserved beside the cache, its target is untouched, and the
	// cache path is a real empty directory. A real go-mod directory stays.
	backup, err := os.Readlink(link + ".before-d48")
	if err != nil || backup != target {
		t.Fatalf("backup = %q, %v; want the original link to %s", backup, err, target)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "shared" {
		t.Fatalf("repair touched the link target: %v: %q", err, data)
	}
	info, err := os.Lstat(link)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("cache path is not a real directory: %+v, %v", info, err)
	}
	if entries, err := os.ReadDir(link); err != nil || len(entries) != 0 {
		t.Fatalf("new cache directory is not empty: %v, %v", entries, err)
	}
	if info, err := os.Lstat(realMod); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("an existing real cache directory changed: %+v, %v", info, err)
	}
	if !strings.Contains(out.String(), "DYNA_CI_CACHE_REPAIR go-build") || strings.Contains(out.String(), "DYNA_CI_CACHE_REPAIR go-mod") {
		t.Fatalf("repair facts = %q", out.String())
	}

	// The next run finds real directories, takes no lock and reports nothing.
	second, err := exec.Command("bash", "-c", cacheRepairScript(base, time.Minute)).CombinedOutput()
	if err != nil {
		t.Fatalf("second repair failed: %v: %s", err, second)
	}
	if len(bytes.TrimSpace(second)) != 0 {
		t.Fatalf("a no-op repair printed %q", second)
	}
	if again, err := os.Readlink(link + ".before-d48"); err != nil || again != target {
		t.Fatalf("a no-op repair changed the backup: %q, %v", again, err)
	}

	// The generated setup command carries the repair before the shared run lock.
	script := nodeSetupScript(base, filepath.Join(base, "runs", "run", "reports"), filepath.Join(base, "runs", "run", ".active"), []string{"go-build", "go-mod", "npm"})
	if parsed, err := exec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("setup command does not parse: %v: %s", err, parsed)
	}
	repairAt, sharedAt := strings.Index(script, "DYNA_CI_CACHE_REPAIR"), strings.Index(script, "flock -s")
	if repairAt < 0 || sharedAt < 0 || repairAt > sharedAt {
		t.Fatalf("setup command does not repair under the exclusive lock before the shared lock: %s", script)
	}
}

// TestCacheRepairRefusesUnsafeLinks keeps the guards around the automatic
// repair: a linked npm cache or base, a link that resolves inside the managed
// base, and an existing backup all stop the run. No link is removed or
// rewritten and no target changes.
func TestCacheRepairRefusesUnsafeLinks(t *testing.T) {
	t.Run("npm link", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "base")
		if err := os.MkdirAll(repositoryCacheRoot(base), 0o700); err != nil {
			t.Fatal(err)
		}
		target := t.TempDir()
		if err := os.Symlink(target, repositoryCachePath(base, "npm")); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", "-c", cacheLinkGuard(base, []string{"go-build", "go-mod", "npm"})).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 69 {
			t.Fatalf("guard accepted a linked npm cache: %v: %s", err, out)
		}
		for _, want := range []string{repositoryCachePath(base, "npm"), target} {
			if !strings.Contains(string(out), want) {
				t.Fatalf("guard did not name %s: %s", want, out)
			}
		}
	})

	t.Run("all real directories", func(t *testing.T) {
		base := t.TempDir()
		for _, name := range []string{"go-build", "go-mod", "npm"} {
			if err := os.MkdirAll(repositoryCachePath(base, name), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if out, err := exec.Command("bash", "-c", cacheLinkGuard(base, []string{"go-build", "go-mod", "npm"})).CombinedOutput(); err != nil {
			t.Fatalf("guard refused real managed cache directories: %v: %s", err, out)
		}
	})

	t.Run("linked base parent", func(t *testing.T) {
		realParent := t.TempDir()
		parent := filepath.Join(t.TempDir(), "parent-link")
		if err := os.Symlink(realParent, parent); err != nil {
			t.Fatal(err)
		}
		base := filepath.Join(parent, "node")
		out, err := exec.Command("bash", "-c", cacheLinkGuard(base, []string{"go-build", "go-mod", "npm"})).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 69 || !strings.Contains(string(out), parent) {
			t.Fatalf("guard accepted a linked base parent: %v: %s", err, out)
		}
	})

	t.Run("link inside the base", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "base")
		real := repositoryCachePath(base, "go-mod-real")
		if err := os.MkdirAll(real, 0o700); err != nil {
			t.Fatal(err)
		}
		link := repositoryCachePath(base, "go-mod")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", "-c", cacheRepairScript(base, time.Minute)).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 69 || !strings.Contains(string(out), "resolves inside") {
			t.Fatalf("repair accepted a link inside the base: %v: %s", err, out)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("repair removed or rewrote the unsafe link: %v", err)
		}
		if _, err := os.Lstat(link + ".before-d48"); !os.IsNotExist(err) {
			t.Fatalf("repair made a backup for an unsafe link: %v", err)
		}
	})

	t.Run("link with no target", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "base")
		if err := os.MkdirAll(repositoryCacheRoot(base), 0o700); err != nil {
			t.Fatal(err)
		}
		targetParent := t.TempDir()
		target := filepath.Join(targetParent, "missing")
		link := repositoryCachePath(base, "go-build")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", "-c", cacheRepairScript(base, time.Minute)).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 69 || !strings.Contains(string(out), "dangling") {
			t.Fatalf("repair accepted a link whose target does not exist: %v: %s", err, out)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("repair changed the dangling link: %v", err)
		}
		if _, err := os.Lstat(link + ".before-d48"); !os.IsNotExist(err) {
			t.Fatalf("repair made a backup for a dangling link: %v", err)
		}
		if _, err := os.Stat(target); !os.IsNotExist(err) {
			t.Fatalf("repair created the missing target: %v", err)
		}
	})

	t.Run("existing backup", func(t *testing.T) {
		base := filepath.Join(t.TempDir(), "base")
		target := t.TempDir()
		if err := os.MkdirAll(repositoryCacheRoot(base), 0o700); err != nil {
			t.Fatal(err)
		}
		link := repositoryCachePath(base, "go-build")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		backup := link + ".before-d48"
		if err := os.WriteFile(backup, []byte("keep me"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("bash", "-c", cacheRepairScript(base, time.Minute)).CombinedOutput()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 69 || !strings.Contains(string(out), "refusing to overwrite") {
			t.Fatalf("repair accepted an existing backup: %v: %s", err, out)
		}
		if data, err := os.ReadFile(backup); err != nil || string(data) != "keep me" {
			t.Fatalf("repair overwrote the backup: %q, %v", data, err)
		}
		if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("repair changed the link beside an existing backup: %v", err)
		}
	})
}

// TestParseCacheRepairs reads the repair facts once each, in order, and
// ignores unrelated setup output.
func TestParseCacheRepairs(t *testing.T) {
	got := parseCacheRepairs("noise\nDYNA_CI_CACHE_REPAIR go-build\nDYNA_CI_CACHE_REPAIR go-build\nDYNA_CI_CACHE_REPAIR go-mod\ntrailing")
	want := []CacheKind{CacheGoBuild, CacheGoMod}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("parseCacheRepairs = %v, want %v", got, want)
	}
	if kinds := parseCacheRepairs("DYNA_CI_CACHE_REPAIR npm\n"); len(kinds) != 0 {
		t.Fatalf("parseCacheRepairs accepted an unregistered cache: %v", kinds)
	}
}
