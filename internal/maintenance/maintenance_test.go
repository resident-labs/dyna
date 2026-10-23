package maintenance

import (
	"context"
	"dyna-agent/internal/notices"
	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
	"dyna-agent/internal/store/storetest"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The built-in maintenance hook owns the routine hourly pass, so this loop runs
// only while disk pressure holds.
func TestSchedulerRunsOnlyUnderPressure(t *testing.T) {
	var s scheduler
	now := time.Unix(1000, 0)
	runs, alerts := 0, 0
	run := func(context.Context) (Result, error) {
		runs++
		return Result{BytesFreed: 42, Kept: map[string]int{"dirty": 2}}, nil
	}
	alert := func(n notices.Notice) error {
		alerts++
		// The notice carries the low path with its free space and what the
		// pass kept, as typed facts the card reads.
		if n.Kind != notices.KindDisk || n.Disk == nil || len(n.Disk.Paths) != 1 || n.Disk.Paths[0].Path != "/tmp" {
			t.Fatalf("disk notice = %+v", n)
		}
		if n.Disk.FreedBytes != 42 || len(n.Disk.Kept) != 1 || n.Disk.Kept[0] != "dirty: 2" {
			t.Fatalf("disk facts = %+v", n.Disk)
		}
		return nil
	}
	for _, tick := range []struct {
		after        time.Duration
		low          []lowDisk
		runs, alerts int
	}{
		{0, nil, 0, 0},
		{time.Minute, nil, 0, 0},
		{2 * time.Minute, []lowDisk{{Path: "/tmp", FreeBytes: 1024}}, 1, 1},
		{3 * time.Minute, []lowDisk{{Path: "/tmp", FreeBytes: 1024}}, 1, 1},
		{62 * time.Minute, []lowDisk{{Path: "/tmp", FreeBytes: 1024}}, 2, 1},
		{63 * time.Minute, nil, 2, 1},
		{64 * time.Minute, []lowDisk{{Path: "/tmp", FreeBytes: 1024}}, 3, 2},
	} {
		if err := s.tick(context.Background(), now.Add(tick.after), tick.low, run, alert); err != nil {
			t.Fatal(err)
		}
		if runs != tick.runs || alerts != tick.alerts {
			t.Fatalf("at %s: runs=%d alerts=%d", tick.after, runs, alerts)
		}
	}
}

func TestSchedulerReportsPartialFailureOnce(t *testing.T) {
	var s scheduler
	calls := 0
	failure := errors.New("partial cleanup")
	run := func(context.Context) (Result, error) { return Result{BytesFreed: 7}, failure }
	alert := func(n notices.Notice) error {
		calls++
		if n.Disk == nil || !n.Disk.MaintenanceFailed {
			t.Fatalf("failed pass is not a failed disk fact: %+v", n)
		}
		return nil
	}
	now := time.Now()
	if err := s.tick(context.Background(), now, []lowDisk{{Path: "/tmp"}}, run, alert); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := s.tick(context.Background(), now.Add(time.Minute), []lowDisk{{Path: "/tmp"}}, run, alert); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("alerts=%d", calls)
	}
}

func TestFreeSpaceForMissingManagedRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := freeSpace(root); err != nil {
		t.Fatal(err)
	}
	// A missing root must resolve to its nearest existing parent without error.
	// The value is not asserted: a full disk legitimately reports zero free bytes.
	if _, err := freeSpace(root + "/not-created/yet"); err != nil {
		t.Fatal(err)
	}
}

// A small in-memory file system is judged against its own size: the owner's
// absolute minimum free space would keep /tmp under pressure forever.
func TestLowSpaceJudgesInMemoryFileSystemsByShare(t *testing.T) {
	const minFree = 10 << 30
	tmpfs := space{Free: 8 << 30, Total: 16 << 30, InMemory: true}
	if lowSpace(tmpfs, minFree) {
		t.Fatalf("%d free of %d is not under pressure", tmpfs.Free, tmpfs.Total)
	}
	for _, low := range []space{
		{Free: 2 << 30, Total: 16 << 30, InMemory: true},
		{Free: 8 << 30, Total: 900 << 30, InMemory: false},
	} {
		if !lowSpace(low, minFree) {
			t.Fatalf("%+v must be under pressure", low)
		}
	}
}

func TestLoopCanceledDoesNotRunMaintenance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Loop(ctx, func(string, ...any) { t.Fatal("canceled loop logged or ran") }, nil)
}

func TestScratchOwnerReadsSessionAndRunLifecycle(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	closed := time.Now().Add(-48 * time.Hour).UTC().Truncate(time.Second)
	for _, tc := range []struct {
		root, id, name, body string
		known, open          bool
		ended                time.Time
	}{
		{runstore.SessionsDir(), "ses_open", "", `{}`, true, true, time.Time{}},
		{runstore.SessionsDir(), "ses_closed", "", `{"closed":"` + closed.Format(time.RFC3339) + `"}`, true, false, closed},
		{runstore.RunsDir(), "wf_done", "meta.json", `{"id":"wf_done","status":"ok","endedAt":"` + closed.Format(time.RFC3339) + `"}`, true, false, closed},
		{runstore.RunsDir(), "wf_active", "meta.json", `{"id":"wf_active","status":"running","endedAt":"` + closed.Format(time.RFC3339) + `"}`, true, true, time.Time{}},
		{runstore.SessionsDir(), "ses_bad", "", `not json`, false, false, time.Time{}},
	} {
		if tc.name == "" {
			// An empty name stands for the session record.
			recordtest.WriteRaw(t, tc.root, tc.id, []byte(tc.body))
		} else {
			var m runstore.Meta
			if err := json.Unmarshal([]byte(tc.body), &m); err != nil {
				t.Fatal(err)
			}
			runstoretest.Write(t, store.DataDir(), m)
		}
		got := scratchOwner(tc.id)
		if got.Known != tc.known || got.Open != tc.open || !got.ClosedAt.Equal(tc.ended) {
			t.Fatalf("%s: %+v", tc.id, got)
		}
	}
	if got := scratchOwner("../escape"); got.Known {
		t.Fatal(got)
	}
}

func TestDiscoverDeduplicatesLinkedTreesAndScopesSession(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	base := t.TempDir()
	a, b, c := filepath.Join(base, "a"), filepath.Join(base, "b"), filepath.Join(base, "c")
	for _, args := range [][]string{
		{"init", "-q", a}, {"init", "-q", c},
		{"-C", a, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c", "commit.gpgSign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "initial"},
		{"-C", a, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", b, "HEAD"},
	} {
		if out, err := exec.Command("/usr/bin/git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s %v", out, err)
		}
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	for id, dir := range map[string]string{"ses_a": a, "ses_b": b, "ses_c": c} {
		recordtest.WriteRaw(t, runstore.SessionsDir(), id, []byte(`{"dir":"`+dir+`"}`))
	}
	repos, err := discover(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0] != a || repos[1] != c {
		t.Fatal(repos)
	}
	repos, err = discover(context.Background(), "ses_b")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0] != b {
		t.Fatal(repos)
	}
}

func TestOwnerAlertPostsDiskNotice(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	for _, free := range []int64{2048, 1024} {
		n := notices.Notice{
			Key: "disk", Kind: notices.KindDisk, Severity: notices.SeverityError,
			Disk: &notices.DiskFacts{Paths: []notices.DiskPath{{Path: "/tmp", FreeBytes: free}}},
		}
		if err := ownerAlert(n); err != nil {
			t.Fatal(err)
		}
	}
	list, err := notices.DefaultStore().List()
	if err != nil || len(list) != 1 || list[0].Kind != notices.KindDisk || list[0].Severity != notices.SeverityError {
		t.Fatalf("notices = %+v, %v", list, err)
	}
	// One notice per key, with the newest facts, and its line built from them.
	if list[0].Disk == nil || list[0].Disk.Paths[0].FreeBytes != 1024 || list[0].Line() == "" {
		t.Fatalf("notice = %+v", list[0])
	}
}

// A merge cleanup removes the merged branch's worktrees only. It must not
// walk scratch, runs or session logs on every merge; the scheduled pass does.
func TestMergeCleanupSkipsFolderSweeps(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	storetest.DataDir(t)
	merge, err := CleanBranch(context.Background(), []string{}, "feature")
	if err != nil {
		t.Fatal(err)
	}
	if len(merge.Pruned) != 0 {
		t.Fatalf("a merge cleanup swept %v; want no folder sweeps", merge.Pruned)
	}
	scheduled, _ := Run(context.Background(), Options{Repos: []string{}})
	if _, ok := scheduled.Pruned["scratch"]; !ok {
		t.Fatalf("the scheduled pass did not sweep scratch: %v", scheduled.Pruned)
	}
}

// Two maintenance passes never overlap: a pass waits for the running one, and
// a pass whose context ends while it waits does not start.
func TestAPassWaitsForTheRunningOne(t *testing.T) {
	storetest.Isolate(t)
	pass <- struct{}{}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Run(ctx, Options{Repos: []string{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("a waiting pass ignored its ended context: %v", err)
	}
	<-pass
	if _, err := Run(t.Context(), Options{Repos: []string{}}); err != nil {
		t.Fatal(err)
	}
}
