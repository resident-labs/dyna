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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSchedulerHourlyAndPressureEpisodes(t *testing.T) {
	var s scheduler
	now := time.Unix(1000, 0)
	runs, alerts := 0, 0
	run := func(context.Context) (Result, error) {
		runs++
		return Result{BytesFreed: 42, Kept: map[string]int{"dirty": 2}}, nil
	}
	alert := func(text string) error {
		alerts++
		if !strings.Contains(text, "42 bytes") || !strings.Contains(text, "dirty: 2") {
			t.Fatal(text)
		}
		if !strings.Contains(text, "Disk space is low in /tmp") {
			t.Fatalf("the alert must name the low path: %s", text)
		}
		return nil
	}
	for _, tick := range []struct {
		after        time.Duration
		low          []string
		runs, alerts int
	}{
		{0, nil, 1, 0},
		{time.Minute, nil, 1, 0},
		{2 * time.Minute, []string{"/tmp"}, 2, 1},
		{3 * time.Minute, []string{"/tmp"}, 2, 1},
		{62 * time.Minute, []string{"/tmp"}, 3, 1},
		{63 * time.Minute, nil, 3, 1},
		{64 * time.Minute, []string{"/tmp"}, 4, 2},
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
	alert := func(text string) error {
		calls++
		if !strings.Contains(text, "Some maintenance failed") {
			t.Fatal(text)
		}
		return nil
	}
	now := time.Now()
	if err := s.tick(context.Background(), now, []string{"/tmp"}, run, alert); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if err := s.tick(context.Background(), now.Add(time.Minute), []string{"/tmp"}, run, alert); err != nil {
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
	for _, text := range []string{"Disk space is low.", "Disk space is low again."} {
		if err := ownerAlert(text); err != nil {
			t.Fatal(err)
		}
	}
	list, err := notices.DefaultStore().List()
	if err != nil || len(list) != 1 || list[0].Kind != "disk" || list[0].Severity != "error" || list[0].Text != "Disk space is low again." {
		t.Fatalf("notices = %+v, %v", list, err)
	}
}

type shutdownHooks struct {
	cancel context.CancelFunc
	phase  string
	fail   error
}

func (h shutdownHooks) ReconcileInstall(context.Context) error {
	if h.phase == "install" {
		h.cancel()
		return context.Canceled
	}
	if h.phase == "real-error" {
		return h.fail
	}
	return nil
}
func (h shutdownHooks) Repositories(context.Context) ([]string, error) { return []string{"repo"}, nil }
func (h shutdownHooks) Tick(context.Context, string, time.Time) error {
	if h.phase == "real-hook-error" {
		return h.fail
	}
	h.cancel()
	return context.Canceled
}

func TestLoopReportsShutdownOnce(t *testing.T) {
	for _, phase := range []string{"install", "hooks", "real-error", "real-hook-error"} {
		t.Run(phase, func(t *testing.T) {
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var lines []string
			hooks := shutdownHooks{cancel: cancel, phase: phase, fail: errors.New("install disk failure")}
			loop(ctx, func(format string, args ...any) {
				line := fmt.Sprintf(format, args...)
				lines = append(lines, line)
				if phase == "real-hook-error" && strings.HasPrefix(line, "hooks:") {
					cancel()
				}
			}, hooks, nil)
			want := []string{"maintenance: stopped for engine shutdown"}
			if phase == "real-error" {
				want = append([]string{"install hook results: install disk failure"}, want...)
			}
			if phase == "real-hook-error" {
				want = append([]string{"hooks: install disk failure"}, want...)
			}
			if strings.Join(lines, "\n") != strings.Join(want, "\n") {
				t.Fatalf("logs=%q want=%q", lines, want)
			}
		})
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
