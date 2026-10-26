package fsutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"
)

// A scope that has not run a turn since an update still publishes the build it
// last named. Pruning must keep that image, or its wrappers, MCP commands and
// $DYNA_BIN point at an executable that no longer exists.
func TestPruneKeepsLinkedImage(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	linked, err := buildImage(t, "published build")
	if err != nil {
		t.Fatal(err)
	}
	link, err := CurrentLink("/owner/accounts/one/.runtime.home")
	if err != nil {
		t.Fatal(err)
	}
	if err := SetLink(link, linked); err != nil {
		t.Fatal(err)
	}
	// The linked copy is the oldest one, so an unbounded prune would drop it.
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(linked, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= stableImageCopies; i++ {
		if _, err := buildImage(t, fmt.Sprintf("later build %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if target, err := os.Readlink(link); err != nil || target != linked {
		t.Fatalf("pruning moved this scope's link: %q, %v", target, err)
	}
	if _, err := os.Stat(link); err != nil {
		t.Fatalf("pruning deleted the image a published link names: %v", err)
	}
}

// Two preparations of one scope can publish at the same time, before any
// generation lock. Neither may remove the other's staged link or fail, which
// would leave that session on one fixed build.
func TestSetLinkSupportsOverlappingUpdates(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	link, err := CurrentLink("/owner/accounts/one/.runtime.home")
	if err != nil {
		t.Fatal(err)
	}
	targets := make([]string, 4)
	for i := range targets {
		if targets[i], err = buildImage(t, fmt.Sprintf("build %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(targets)*8)
	for round := 0; round < 8; round++ {
		for _, target := range targets {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- SetLink(link, target)
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("overlapping update failed: %v", err)
		}
	}
	current, err := os.Readlink(link)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(targets, current) {
		t.Fatalf("link names %q, which is not one of the published builds", current)
	}
}

// buildImage makes a runnable stand-in for one engine build and returns its
// copied image.
func buildImage(t *testing.T, build string) (string, error) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "dyna")
	if err := os.WriteFile(source, []byte("#!/bin/sh\n# "+build+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return StableImage(source)
}

// An engine that restarted after an update must repoint every link a native
// scope published at its own image. A session that keeps running execs that
// link, so it follows the engine onto its current build without a restart;
// leaving the link on the first build keeps the old CLI against new engine
// state (D-161).
func TestRefreshScopeLinksRepublishesEveryScope(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux running image copy")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	first, err := buildImage(t, "first build")
	if err != nil {
		t.Fatal(err)
	}
	scopes := []string{"/owner/accounts/one/.runtime.home", "/owner/accounts/two/.runtime.home"}
	links := make([]string, len(scopes))
	for i, scope := range scopes {
		if links[i], err = CurrentLink(scope); err != nil {
			t.Fatal(err)
		}
		if err = SetLink(links[i], first); err != nil {
			t.Fatal(err)
		}
	}
	if err := RefreshScopeLinks(); err != nil {
		t.Fatal(err)
	}
	current, err := StableExecutable()
	if err != nil {
		t.Fatal(err)
	}
	for _, link := range links {
		if target, err := os.Readlink(link); err != nil || target != current {
			t.Fatalf("scope link still names the first build: %q, %v", target, err)
		}
	}
}

// dyna publishes this path as $DYNA_BIN, in every harness MCP server command
// and in the tool home it hands to workers. A turn that ends must not take the
// binary with it, or the next turn starts an MCP server that cannot run and
// the worker loses every Dyna tool.
func TestStableExecutableOutlivesItsProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux running image copy")
	}
	if out := os.Getenv("DYNA_STABLE_EXECUTABLE_OUT"); out != "" {
		path, err := StableExecutable()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, []byte(path), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	names := make([]string, 0, 2)
	for run := 0; run < 2; run++ {
		out := filepath.Join(dir, "path-"+string(rune('a'+run)))
		cmd := exec.Command(os.Args[0], "-test.run=TestStableExecutableOutlivesItsProcess$")
		cmd.Env = append(os.Environ(), "DYNA_STABLE_EXECUTABLE_OUT="+out)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child failed: %v\n%s", err, output)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, string(raw))
	}
	// The two children ran as different processes. A pinned /proc/<pid>/exe
	// path differs between them and names a process that has already exited.
	if names[0] != names[1] {
		t.Fatalf("stable executable changed between processes: %q then %q", names[0], names[1])
	}
	info, err := os.Stat(names[0])
	if err != nil {
		t.Fatalf("stable executable is gone after its processes exited: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0700 != 0700 || info.Size() == 0 {
		t.Fatalf("stable executable is not a runnable copy: %v", info.Mode())
	}
}

// A long-lived session MCP server or an idle harness child keeps running the
// image its turn published. Pruning must not delete that image while it runs,
// or the next launch of the same path fails.
func TestPruneKeepsRunningImage(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "dyna-old")
	live := filepath.Join(dir, "dyna-live")
	for _, path := range []string{old, live} {
		if err := os.WriteFile(path, []byte("image"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().Add(-time.Hour)
	if err := os.Chtimes(old, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < stableImageCopies; i++ {
		path := filepath.Join(dir, "dyna-new-"+string(rune('a'+i)))
		if err := os.WriteFile(path, []byte("image"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, stamp.Add(time.Duration(i)*time.Minute), stamp.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	pruneImages(dir, filepath.Join(dir, "dyna-keep"), map[string]bool{live: true})
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("pruned the image a live process runs: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("kept an idle superseded image: %v", err)
	}
}
