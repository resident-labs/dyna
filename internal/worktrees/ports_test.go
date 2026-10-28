package worktrees

import (
	"context"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func alwaysFree(int) bool { return true }

// pathsWithBlock returns n distinct Dyna-style paths whose first block choice is first.
func pathsWithBlock(t *testing.T, first, n int) []string {
	t.Helper()
	var out []string
	for i := 0; i < 1000000 && len(out) < n; i++ {
		path := filepath.Join(os.TempDir(), fmt.Sprintf("dyna-wt-port-%d", i))
		if blockFor(path) == first {
			out = append(out, path)
		}
	}
	if len(out) < n {
		t.Fatalf("found %d paths for block %d, want %d", len(out), first, n)
	}
	return out
}

func TestAllocatePortSkipsTakenBlock(t *testing.T) {
	path := pathsWithBlock(t, 20000, 1)[0]
	records := []Record{{Path: "/other", Port: 20000}}
	port, err := allocatePort(path, records, alwaysFree)
	if err != nil || port != 20010 {
		t.Fatalf("allocatePort() = %d, %v; want 20010", port, err)
	}
}

func TestAllocatePortSkipsBlockWithBusyPort(t *testing.T) {
	path := pathsWithBlock(t, 20000, 1)[0]
	busy := func(port int) bool { return port != 20003 }
	port, err := allocatePort(path, nil, busy)
	if err != nil || port != 20010 {
		t.Fatalf("allocatePort() = %d, %v; want 20010", port, err)
	}
}

func TestCanBindSeesListenerOnPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	if canBind(port) {
		t.Fatalf("canBind(%d) = true while a listener holds it", port)
	}
}

func TestAllocatePortFailsWhenNoBlockIsFree(t *testing.T) {
	path := pathsWithBlock(t, 20000, 1)[0]
	port, err := allocatePort(path, nil, func(int) bool { return false })
	if err == nil {
		t.Fatalf("allocatePort() = %d, nil; want an error when no block is free", port)
	}
	if !strings.Contains(err.Error(), "run dyna git worktrees gc") {
		t.Fatalf("error = %q; want the gc hint", err)
	}
}

func TestAllocatePortStaysInRange(t *testing.T) {
	for i := 0; i < 50; i++ {
		path := filepath.Join(os.TempDir(), fmt.Sprintf("dyna-wt-range-%d", i))
		port, err := allocatePort(path, nil, alwaysFree)
		if err != nil {
			t.Fatal(err)
		}
		if port < 20000 || port > 29990 || port%10 != 0 {
			t.Fatalf("allocatePort(%q) = %d; want a block start in 20000-29990", path, port)
		}
	}
}

func TestAllocatePortReusesBlockAfterRecordRemoval(t *testing.T) {
	path := pathsWithBlock(t, 20000, 1)[0]
	port, err := allocatePort(path, []Record{{Path: "/other", Port: 20000}}, alwaysFree)
	if err != nil || port != 20010 {
		t.Fatalf("with other record: allocatePort() = %d, %v; want 20010", port, err)
	}
	port, err = allocatePort(path, nil, alwaysFree)
	if err != nil || port != 20000 {
		t.Fatalf("after removal: allocatePort() = %d, %v; want 20000", port, err)
	}
}

func TestTrackGivesDistinctPortsToTreesWithSameFirstBlock(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	paths := pathsWithBlock(t, 20000, 2)
	var ports []int
	for _, path := range paths {
		if err := Track(Record{Path: path}); err != nil {
			t.Fatal(err)
		}
		rec, err := Get(path)
		if err != nil || rec.Port == 0 {
			t.Fatalf("Get(%q) = %#v, %v; want a port", path, rec, err)
		}
		ports = append(ports, rec.Port)
	}
	if ports[0] == ports[1] {
		t.Fatalf("two trees share port %d", ports[0])
	}
}

func TestTrackKeepsPortAcrossRetentionUpdate(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := pathsWithBlock(t, 20000, 1)[0]
	if err := Track(Record{Path: path, RunID: "wf_first"}); err != nil {
		t.Fatal(err)
	}
	first, err := Get(path)
	if err != nil || first.Port == 0 {
		t.Fatalf("Get() = %#v, %v; want a port", first, err)
	}
	if err := Track(Record{Path: path, RunID: "wf_second", Port: 0}); err != nil {
		t.Fatal(err)
	}
	again, err := Get(path)
	if err != nil || again.Port != first.Port {
		t.Fatalf("port after update = %d, %v; want %d", again.Port, err, first.Port)
	}
}

func TestTrackParallelCallsNeverShareAPort(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	paths := pathsWithBlock(t, 20000, 8)
	var wg sync.WaitGroup
	errs := make(chan error, len(paths))
	for _, path := range paths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			errs <- Track(Record{Path: path})
		}(path)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int]string{}
	for _, path := range paths {
		rec, err := Get(path)
		if err != nil || rec.Port == 0 {
			t.Fatalf("Get(%q) = %#v, %v; want a port", path, rec, err)
		}
		if other, dup := seen[rec.Port]; dup {
			t.Fatalf("%s and %s share port %d", other, path, rec.Port)
		}
		seen[rec.Port] = path
	}
}

func TestPortEnvNamesBlockVariables(t *testing.T) {
	got := PortEnv(Record{Port: 20010})
	want := map[string]string{"DYNA_PORT": "20010", "DYNA_PORTS": "20010-20019", "PORT": "20010"}
	if len(got) != len(want) {
		t.Fatalf("PortEnv() = %#v; want %#v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("PortEnv()[%s] = %q; want %q", key, got[key], value)
		}
	}
	if env := PortEnv(Record{}); env != nil {
		t.Fatalf("PortEnv() without a port = %#v; want nil", env)
	}
}

func TestTrackFailsWhenNoPortBlockIsFree(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	saved := portFree
	portFree = func(int) bool { return false }
	t.Cleanup(func() { portFree = saved })
	path := pathsWithBlock(t, 20000, 1)[0]
	if err := Track(Record{Path: path}); err == nil {
		t.Fatal("Track() succeeded with no free port block")
	}
	if _, err := Get(path); err == nil {
		t.Fatal("record was written although its port allocation failed")
	}
}

// onlyBlockFree makes every port outside 20000-20009 busy, so one record
// holding block 20000 leaves the pool full.
func onlyBlockFree(t *testing.T) {
	t.Helper()
	saved := portFree
	portFree = func(port int) bool { return port >= 20000 && port < 20010 }
	t.Cleanup(func() { portFree = saved })
}

func TestTrackReclaimsBlockOfMissingPath(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	onlyBlockFree(t)
	paths := pathsWithBlock(t, 20000, 2)
	// The first path does not exist on disk, so its record is proven stale.
	if err := Track(Record{Path: paths[0]}); err != nil {
		t.Fatal(err)
	}
	if err := Track(Record{Path: paths[1]}); err != nil {
		t.Fatalf("Track() = %v; want the missing path's block reclaimed", err)
	}
	if _, err := Get(paths[0]); err == nil {
		t.Fatal("stale record still registered")
	}
	if rec, err := Get(paths[1]); err != nil || rec.Port != 20000 {
		t.Fatalf("Get() = %#v, %v; want port 20000", rec, err)
	}
}

func TestTrackClearsPortOfReclaimableTreeAndKeepsRecord(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	onlyBlockFree(t)
	paths := pathsWithBlock(t, 20000, 2)
	if err := os.Mkdir(paths[0], 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(paths[0]) })
	if err := Track(Record{Path: paths[0], Repo: "/repo", RunID: "wf_keep"}); err != nil {
		t.Fatal(err)
	}
	saved := reclaimablePaths
	t.Cleanup(func() { reclaimablePaths = saved })

	// An active, dirty, unmerged or uncertain tree keeps its block.
	reclaimablePaths = func(context.Context, string) map[string]bool { return nil }
	if err := Track(Record{Path: paths[1]}); err == nil {
		t.Fatal("Track() reassigned the block of a tree that must be kept")
	}
	if rec, err := Get(paths[0]); err != nil || rec.Port != 20000 {
		t.Fatalf("kept tree = %#v, %v; want port 20000 unchanged", rec, err)
	}

	reclaimablePaths = func(_ context.Context, _ string) map[string]bool { return map[string]bool{paths[0]: true} }
	if err := Track(Record{Path: paths[1]}); err != nil {
		t.Fatalf("Track() = %v; want the reclaimable tree's block reused", err)
	}
	if rec, err := Get(paths[1]); err != nil || rec.Port != 20000 {
		t.Fatalf("Get() = %#v, %v; want port 20000", rec, err)
	}
	old, err := Get(paths[0])
	if err != nil || old.Port != 0 || old.RunID != "wf_keep" {
		t.Fatalf("released record = %#v, %v; want it kept without a port", old, err)
	}
}

func TestTrackReclaimsSeventeenthTreeWithOneScan(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	saved, savedPaths := portFree, reclaimablePaths
	t.Cleanup(func() { portFree, reclaimablePaths = saved, savedPaths })
	// Only the 17 blocks that records hold are free of sockets, so the pool is full.
	portFree = func(port int) bool { return port >= 20000 && port < 20170 }
	var target string
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 17; i++ {
		path, err := os.MkdirTemp("", "dyna-wt-old-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.RemoveAll(path) })
		rec := Record{Path: path, Repo: "/repo", Port: 20000 + 10*i, RetainedAt: base.Add(time.Duration(i) * time.Hour)}
		if err := Track(rec); err != nil {
			t.Fatal(err)
		}
		target = path
	}
	scans := 0
	reclaimablePaths = func(_ context.Context, repo string) map[string]bool {
		scans++
		return map[string]bool{target: true}
	}
	path := pathsWithBlock(t, 20000, 1)[0]
	if err := Track(Record{Path: path}); err != nil {
		t.Fatalf("Track() = %v; want the 17th record's block reclaimed", err)
	}
	if rec, err := Get(path); err != nil || rec.Port != 20160 {
		t.Fatalf("Get() = %#v, %v; want port 20160", rec, err)
	}
	if scans != 1 {
		t.Fatalf("repository scans = %d; want 1", scans)
	}
}

func TestReleaseRecordSkipsReplacedRecord(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	path := pathsWithBlock(t, 20000, 1)[0]
	if err := Track(Record{Path: path, RunID: "wf_old"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Get(path)
	if err != nil {
		t.Fatal(err)
	}
	// Another caller replaces the record after the snapshot was read.
	if err := Track(Record{Path: path, RunID: "wf_new"}); err != nil {
		t.Fatal(err)
	}
	db, err := registryDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, remove := range []bool{true, false} {
		if ok, err := releaseRecord(context.Background(), db, snapshot, remove); err != nil || ok {
			t.Fatalf("releaseRecord(remove=%v) = %v, %v; want no change", remove, ok, err)
		}
	}
	if rec, err := Get(path); err != nil || rec.RunID != "wf_new" || rec.Port != snapshot.Port {
		t.Fatalf("Get() = %#v, %v; want the replaced record intact", rec, err)
	}
}

func TestPortEnvForDirNamesTrackedWorktreeOnly(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	repo := initRepo(t)
	wt := addGitWorktree(t, repo)
	cleared := map[string]string{"DYNA_PORT": "", "DYNA_PORTS": "", "PORT": ""}
	if got := PortEnvForDir(context.Background(), wt); !maps.Equal(got, cleared) {
		t.Fatalf("untracked worktree env = %#v; want %#v", got, cleared)
	}
	if err := Track(Record{Path: wt}); err != nil {
		t.Fatal(err)
	}
	rec, err := Get(wt)
	if err != nil {
		t.Fatal(err)
	}
	if got := PortEnvForDir(context.Background(), wt); !maps.Equal(got, PortEnv(rec)) {
		t.Fatalf("PortEnvForDir() = %#v; want %#v", got, PortEnv(rec))
	}
	if got := PortEnvForDir(context.Background(), repo); !maps.Equal(got, cleared) {
		t.Fatalf("repo checkout env = %#v; want %#v", got, cleared)
	}
}
