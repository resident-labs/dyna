package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

// runningCIRegistry offers one 16-slot machine and no saved result.
type runningCIRegistry struct{ hostedCIRegistry }

func (r *runningCIRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "hermes", Slots: 16, Enabled: true}}, nil
}
func (r *runningCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}
func (r *runningCIRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	return localci.Result{}, os.ErrNotExist
}

// scheduledCIRunFixture offers one PR whose head needs local CI, and a service
// that can schedule it.
func scheduledCIRunFixture(t *testing.T) (*Service, State, time.Time) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(name, body string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		git("add", name)
		git("commit", "--quiet", "-m", name)
		return git("rev-parse", "HEAD")
	}
	git("init", "--quiet")
	git("config", "user.email", "ci@example.com")
	git("config", "user.name", "CI")
	base := commit("README", "before\n")
	head := commit("main.go", "package main\n")

	root := filepath.Join(t.TempDir(), "prs")
	record := &Record{Key: "pr", Kind: "github", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Number: 17, Title: "Show running local CI per machine", URL: "https://github.com/owner/repo/pull/17", Head: head, Base: base, Branch: "feature", RepoPath: repo, Watching: true, State: "open", Created: time.Unix(2, 0)}
	saved := State{Version: 1, Records: map[string]*Record{record.Key: record}}
	if err := state.Write(root, saved); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1000, 0)
	s := &Service{Store: &Store{Root: root}, LocalCIRegistry: &runningCIRegistry{}, Now: func() time.Time { return started }}
	s.GH.run = func(context.Context, *exec.Cmd) error { return nil }
	return s, saved, started
}

// A run in flight names its machine, its slots and its PR, and it leaves the
// snapshot when it ends.
func TestRunningCIReportsAMachineRunUntilItEnds(t *testing.T) {
	s, saved, started := scheduledCIRunFixture(t)
	record := saved.Records["pr"]
	head := record.Head
	running := make(chan string, 1)
	s.RunNodeCI = func(ctx context.Context, node localci.Node, _ localci.Request) (localci.Result, error) {
		running <- node.Name
		<-ctx.Done()
		return localci.Result{}, ctx.Err()
	}
	changed := make(chan struct{}, 8)
	s.RunningCIChanged = func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}

	if err := s.scheduleRegisteredCI(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	select {
	case name := <-running:
		if name != "hermes" {
			t.Fatalf("run started on %q", name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no local CI run started")
	}
	list := s.RunningCI()
	if len(list) != 1 {
		t.Fatalf("running CI = %+v", list)
	}
	got := list[0]
	if got.Machine != "hermes" || got.Slots != registeredCIRunSlots || got.Repo != "owner/repo" || got.Number != 17 || got.Head != head[:7] || got.Trial || !got.Started.Equal(started) {
		t.Fatalf("running CI = %+v", got)
	}
	if got.Title != record.Title || got.URL != record.URL {
		t.Fatalf("running CI names %q at %q, want the PR", got.Title, got.URL)
	}
	if suites := strings.Join(got.Suites, ","); suites != "go,cross-build" {
		t.Fatalf("running CI suites = %q", suites)
	}

	// The PR is no longer tracked, so the run is cancelled and must leave the
	// snapshot for clients.
	if err := state.Write(s.Store.Root, State{Version: 1, Records: map[string]*Record{}}); err != nil {
		t.Fatal(err)
	}
	if err := s.scheduleRegisteredCI(context.Background(), State{Version: 1}); err != nil {
		t.Fatal(err)
	}
	for waiting := true; waiting; {
		select {
		case <-changed:
			waiting = len(s.RunningCI()) != 0
		case <-time.After(5 * time.Second):
			t.Fatalf("finished run stayed in the snapshot: %+v", s.RunningCI())
		}
	}
}

// fakeNodeWork holds one kind of local CI run in the part of its work that keeps
// a keeper on the machine. Each step is a channel, so the test controls when the
// run starts and finishes its teardown.
type fakeNodeWork struct {
	started, cancelled, teardown chan struct{}
	ctx                          context.Context
}

func newFakeNodeWork() *fakeNodeWork {
	return &fakeNodeWork{started: make(chan struct{}), cancelled: make(chan struct{}), teardown: make(chan struct{})}
}

func (f *fakeNodeWork) work(ctx context.Context) (localci.Result, error) {
	f.ctx = ctx
	close(f.started)
	<-ctx.Done()
	close(f.cancelled)
	// The node work is not over: the containers and the keeper are still there.
	<-f.teardown
	return localci.Result{}, ctx.Err()
}

// localLaneNodes starts the local lane's CI run and holds it in the same way.
type localLaneNodes struct{ work *fakeNodeWork }

func (l localLaneNodes) Show(string, string) (localci.Result, error) {
	return localci.Result{}, os.ErrNotExist
}
func (l localLaneNodes) Run(ctx context.Context, _ localci.Request) (localci.Result, error) {
	return l.work.work(ctx)
}

// A local CI run must end with its engine. A run that outlives the engine leaves
// a keeper on the machine, and that keeper holds the cache lock for hours, so an
// engine that stops cancels the runs it started, in both lanes, and waits for
// their work on the machine to end.
func TestStopLocalCICancelsRunsAndWaitsForTheirNodeWork(t *testing.T) {
	s, saved, _ := scheduledCIRunFixture(t)
	record := saved.Records["pr"]
	head := record.Head
	scheduled := newFakeNodeWork()
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, _ localci.Request) (localci.Result, error) {
		return scheduled.work(ctx)
	}
	lane := newFakeNodeWork()
	s.LocalCI = localLaneNodes{work: lane}

	if err := s.scheduleRegisteredCI(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	<-scheduled.started
	if _, err := s.localTrialCI(context.Background(), *record, head); err != nil {
		t.Fatal(err)
	}
	<-lane.started

	// Cancellation starts with the engine and does not wait for the teardown.
	s.CancelLocalCI()
	if scheduled.ctx.Err() == nil || lane.ctx.Err() == nil {
		t.Fatalf("a run was not cancelled with the engine: scheduled=%v lane=%v", scheduled.ctx.Err(), lane.ctx.Err())
	}

	// A stop with a live budget waits for both teardowns to end.
	stopped := make(chan []string, 1)
	go func() { stopped <- s.StopLocalCI(context.Background()) }()
	// The stop has cancelled every run, so a run that waits for its
	// teardown is blocked here; one that returns at once has no value yet.
	select {
	case left := <-stopped:
		t.Fatalf("stop returned before the runs ended their node work: %v", left)
	default:
	}

	// A stop whose budget is already gone names the runs still working.
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	want := []string{head[:7] + " on hermes", head[:7] + " on the local lane"}
	if left := s.StopLocalCI(expired); !slices.Equal(left, want) {
		t.Fatalf("leftover runs = %v, want %v", left, want)
	}

	// Both teardowns end, and then the stop returns.
	close(scheduled.teardown)
	close(lane.teardown)
	if left := <-stopped; len(left) != 0 {
		t.Fatalf("stop reported %v", left)
	}
}
