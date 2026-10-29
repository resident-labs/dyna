package jobs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/jobstest"
	"dyna-agent/internal/machines"
)

// Two checkouts of one kept workspace reset tracked and untracked files, but
// preserve ignored build output between commits.
func TestWorkspaceCheckoutKeepsIgnoredBuildOutput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	cache := filepath.Join(root, "cache.git")
	work := filepath.Join(root, "work")
	gitTest(t, root, "init", "-q", source)
	gitTest(t, source, "config", "user.email", "jobs@example.invalid")
	gitTest(t, source, "config", "user.name", "Jobs Test")
	writeWorkspaceTestFile(t, filepath.Join(source, ".gitignore"), "target/\n")
	writeWorkspaceTestFile(t, filepath.Join(source, "tracked"), "one\n")
	gitTest(t, source, "add", ".")
	gitTest(t, source, "commit", "-qm", "first")
	first := gitTest(t, source, "rev-parse", "HEAD")
	gitTest(t, root, "init", "-q", "--bare", cache)
	gitTest(t, source, "push", "-q", cache, first+":refs/dyna/ci/"+first)

	runCheckout := func(sha string) {
		t.Helper()
		cmd := exec.Command("bash", "-o", "pipefail", "-c", workspaceCheckoutScript(cache, work, sha))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("checkout %s: %v: %s", sha, err, out)
		}
	}
	runCheckout(first)
	writeWorkspaceTestFile(t, filepath.Join(work, "target", "warm"), "keep me\n")
	writeWorkspaceTestFile(t, filepath.Join(work, "untracked"), "remove me\n")
	writeWorkspaceTestFile(t, filepath.Join(work, "tracked"), "dirty\n")

	writeWorkspaceTestFile(t, filepath.Join(source, "tracked"), "two\n")
	gitTest(t, source, "add", "tracked")
	gitTest(t, source, "commit", "-qm", "second")
	second := gitTest(t, source, "rev-parse", "HEAD")
	gitTest(t, source, "push", "-q", cache, second+":refs/dyna/ci/"+second)
	runCheckout(second)

	if got := gitTest(t, work, "rev-parse", "HEAD"); got != second {
		t.Fatalf("HEAD = %s, want %s", got, second)
	}
	if data, err := os.ReadFile(filepath.Join(work, "target", "warm")); err != nil || string(data) != "keep me\n" {
		t.Fatalf("ignored build output = %q, %v; want it kept", data, err)
	}
	if _, err := os.Stat(filepath.Join(work, "untracked")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("untracked file was not cleaned: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(work, "tracked")); err != nil || string(data) != "two\n" {
		t.Fatalf("tracked file = %q, %v; want the second commit", data, err)
	}
}

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeWorkspaceTestFile(t *testing.T, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// One session's jobs for a repository share one node folder, and the second
// job waits for the first through the node admission queue.
func TestWorkspaceJobsShareOneFolderAndSerialize(t *testing.T) {
	sha := strings.Repeat("a", 40)
	svc, _, _, _ := newIdleService(t, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		joined := name + " " + strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "rev-parse --verify"):
			fmt.Fprintln(out, sha)
		case strings.Contains(joined, "--git-common-dir"):
			fmt.Fprintln(out, "/repo/.git")
		}
		return nil
	})
	defer svc.Stop()
	now := time.Unix(2_000_000, 0)
	svc.Store.clock = func() time.Time { now = now.Add(time.Millisecond); return now }
	if _, err := svc.Submit(t.Context(), Input{Command: []string{"true"}, Workspace: true}); err == nil {
		t.Fatal("a workspace without a commit was accepted")
	}
	input := Input{RoomID: "dyna", Session: "ses_one", Command: []string{"server"}, Host: true, Repo: "/repo", Ref: "HEAD", Workspace: true}
	first, err := svc.Submit(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Submit(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Workspace == "" || second.Workspace != first.Workspace {
		t.Fatalf("workspaces = %q and %q, want one kept folder", first.Workspace, second.Workspace)
	}
	wantPath := path.Join(NodeBase(first.Node), "workspaces", RepoKey(first.Session+"\x00"+first.Repo))
	if first.Workspace != wantPath {
		t.Fatalf("workspace = %q, want the stable session path %q", first.Workspace, wantPath)
	}
	admitted, reasons, err := svc.admission().Pass(t.Context(), []Job{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if !admitted[first.ID] || admitted[second.ID] {
		t.Fatalf("admissions = %v, want only the first workspace job", admitted)
	}
	if reason := reasons[second.ID]; !strings.Contains(reason, "workspace") || !strings.Contains(reason, first.ID) {
		t.Fatalf("second job wait = %q, want its workspace held by the first", reason)
	}
}

// Cleanup removes closed and idle workspaces, but never one a queued or
// running job uses. A closed session's live workspace goes on a later pass.
func TestWorkspaceCleanupProtectsLiveJobs(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	node := machines.Machine{Name: "here", Enabled: true}
	svc, store, removed, _ := newWorkspaceCleanupService(t, node)
	store.clock = func() time.Time { return now }
	closedFree := putWorkspaceTestRow(t, store, Job{ID: "job_closed_free", Session: "ses_closed", State: StatePassed})
	closedLive := putWorkspaceTestRow(t, store, Job{ID: "job_closed_live", Session: "ses_closed", State: StateRunning})
	now = now.Add(-25 * time.Hour)
	idleFree := putWorkspaceTestRow(t, store, Job{ID: "job_idle_free", Session: "ses_idle", State: StatePassed})
	idleQueued := putWorkspaceTestRow(t, store, Job{ID: "job_idle_queued", Session: "ses_idle", State: StateQueued})
	now = time.Unix(2_000_000, 0)
	fresh := putWorkspaceTestRow(t, store, Job{ID: "job_fresh", Session: "ses_fresh", State: StatePassed})

	if n, err := svc.CleanupWorkspaces(t.Context(), WorkspaceCleanup{Session: "ses_closed", Now: now}); err != nil || n != 1 {
		t.Fatalf("session cleanup removed %d, %v; want one", n, err)
	}
	assertWorkspaceRemovals(t, removed, closedFree.Workspace+".trash")
	if n, err := svc.CleanupWorkspaces(t.Context(), WorkspaceCleanup{Now: now}); err != nil || n != 1 {
		t.Fatalf("idle cleanup removed %d, %v; want one", n, err)
	}
	assertWorkspaceRemovals(t, removed, idleFree.Workspace+".trash")

	closedLive.State = StatePassed
	if _, won, err := store.SaveOpen(t.Context(), closedLive); err != nil || !won {
		t.Fatalf("finish the closed session's job: won=%v, %v", won, err)
	}
	if n, err := svc.CleanupWorkspaces(t.Context(), WorkspaceCleanup{Now: now}); err != nil || n != 1 {
		t.Fatalf("deferred session cleanup removed %d, %v; want one", n, err)
	}
	assertWorkspaceRemovals(t, removed, closedLive.Workspace+".trash")
	rows, err := store.listWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, row := range rows {
		kept[row.Path] = true
	}
	if len(rows) != 2 || !kept[idleQueued.Workspace] || !kept[fresh.Workspace] {
		t.Fatalf("kept workspaces = %+v, want the queued job's and the fresh one", rows)
	}
}

func TestWorkspaceCleanupRenamesBeforeRemoval(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	node := machines.Machine{Name: "here", Enabled: true}
	svc, store, removed, moved := newWorkspaceCleanupService(t, node)
	store.clock = func() time.Time { return now }
	now = now.Add(-25 * time.Hour)
	kept := putWorkspaceTestRow(t, store, Job{ID: "job_idle", State: StatePassed})
	now = now.Add(25 * time.Hour)
	rows, err := store.listWorkspaces(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("workspaces = %+v, %v; want one", rows, err)
	}
	claimed, err := store.claimWorkspace(t.Context(), rows[0])
	if err != nil || !claimed {
		t.Fatalf("claim = %v, %v; want the idle workspace", claimed, err)
	}
	next := kept
	next.ID, next.State = "job_next", StateQueued
	if _, err := store.saveSubmitted(t.Context(), next); err == nil || !strings.Contains(err.Error(), "being cleaned") {
		t.Fatalf("submission during cleanup = %v, want a retryable cleaning error", err)
	}

	if n, err := svc.CleanupWorkspaces(t.Context(), WorkspaceCleanup{Now: now}); err != nil || n != 1 {
		t.Fatalf("cleanup removed %d, %v; want one", n, err)
	}
	select {
	case got := <-moved:
		want := kept.Workspace + " -> " + kept.Workspace + ".trash"
		if got != want {
			t.Fatalf("workspace move = %q, want %q", got, want)
		}
	default:
		t.Fatal("cleanup removed the workspace without an atomic rename")
	}
	assertWorkspaceRemovals(t, removed, kept.Workspace+".trash")
	reused, err := store.saveSubmitted(t.Context(), next)
	if err != nil || reused.Workspace != kept.Workspace {
		t.Fatalf("workspace after cleanup = %q, %v; want %q", reused.Workspace, err, kept.Workspace)
	}
}

// Disk-pressure cleanup stops as soon as a fresh probe meets the node's floor.
func TestWorkspaceCleanupStopsWhenDiskFloorIsMet(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	node := machines.Machine{Name: "full", Enabled: true, MaxDiskPercent: 90}
	node.Capabilities.Disk = machines.Disk{Total: 100 << 30, Free: 1 << 30}
	svc, store, removed, _ := newWorkspaceCleanupService(t, node)
	store.clock = func() time.Time { return now }
	now = now.Add(-2 * time.Hour)
	oldest := putWorkspaceTestRow(t, store, Job{ID: "job_disk_oldest", Node: "full", State: StatePassed})
	now = now.Add(time.Hour)
	newer := putWorkspaceTestRow(t, store, Job{ID: "job_disk_newer", Node: "full", State: StatePassed})
	now = time.Unix(2_000_000, 0)

	if n, err := svc.CleanupWorkspaces(t.Context(), WorkspaceCleanup{Now: now}); err != nil || n != 1 {
		t.Fatalf("disk cleanup removed %d, %v; want one", n, err)
	}
	assertWorkspaceRemovals(t, removed, oldest.Workspace+".trash")
	rows, err := store.listWorkspaces(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Path != newer.Workspace {
		t.Fatalf("kept workspaces = %+v, want the newer one", rows)
	}
}

func newWorkspaceCleanupService(t *testing.T, nodes ...machines.Machine) (*Service, Store, chan string, chan string) {
	t.Helper()
	dir := t.TempDir()
	byName := map[string]machines.Machine{}
	for _, node := range nodes {
		byName[node.Name] = node
	}
	removed := make(chan string, 16)
	moved := make(chan string, 16)
	fake := jobstest.NewFake(t, jobstest.Protocol{Start: StartScript, Poll: PollScript, Stop: StopScript}, func(_ context.Context, _ string, _ io.Reader, out, _ io.Writer, name string, args ...string) error {
		script, _, ok := jobstest.DecodeNodeScript(name, args)
		words := jobstest.UnquoteWords(script)
		if ok && len(words) == 4 && words[0] == "rm" && words[1] == "-rf" && words[2] == "--" {
			removed <- words[3]
		}
		if ok && len(words) == 9 && words[0] == "test" && words[1] == "!" && words[2] == "-e" && words[4] == "||" && words[5] == "mv" && words[6] == "--" {
			moved <- words[3] + " -> " + words[8]
		}
		if ok && len(words) == 3 && words[0] == "df" && words[1] == "-Pk" {
			fmt.Fprintln(out, "Filesystem 1024-blocks Used Available Capacity Mounted on")
			fmt.Fprintf(out, "/dev/test %d 0 %d 0%% /var/tmp\n", 100<<20, 100<<20)
		}
		return nil
	})
	store := Store{DataDir: dir}
	svc := &Service{Store: store, Machines: fakeMachines{byName: byName}, DataDir: dir, logf: func(string, ...any) {}}
	svc.NewExecutor = func(conn Conn) Executor {
		x := Executor{Node: conn}
		x.Command, x.Wait, x.Clock = Command(fake.CommandFunc()), fake.WaitFunc(), fake.ClockFunc()
		return x
	}
	return svc, store, removed, moved
}

func putWorkspaceTestRow(t *testing.T, store Store, job Job) Job {
	t.Helper()
	if job.Node == "" {
		job.Node = "here"
	}
	if job.Session == "" {
		job.Session = "ses_" + job.ID
	}
	job.Command = []string{"true"}
	job.Repo = "/repo/" + job.ID + "/.git"
	job.SHA = strings.Repeat("a", 40)
	job.Workspace = path.Join(NodeBase(job.Node), "workspaces", "test", job.ID)
	job.Created = store.now()
	job.Timeout = time.Minute
	job.LogPath = filepath.Join(store.DataDir, "jobs", job.ID, "log")
	got, err := store.saveSubmitted(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func assertWorkspaceRemovals(t *testing.T, removed chan string, want ...string) {
	t.Helper()
	for _, expected := range want {
		select {
		case got := <-removed:
			if got != expected {
				t.Fatalf("removed %q, want %q", got, expected)
			}
		case <-t.Context().Done():
			t.Fatalf("workspace %q was not removed", expected)
		}
	}
	select {
	case got := <-removed:
		t.Fatalf("unexpected workspace removal %q", got)
	default:
	}
}
