package watch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	"dyna-agent/internal/subagent"
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
		git("add", ".")
		git("commit", "--quiet", "-m", name)
		return git("rev-parse", "HEAD")
	}
	git("init", "--quiet")
	git("config", "user.email", "ci@example.com")
	git("config", "user.name", "CI")
	writeWatchDefinition(t, repo)
	base := commit("README", "before\n")
	head := commit("main.go", "package main\n")

	// The repository is in the local lane, so a local head's trial hold passes.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	lane, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	lane.LocalLaneRepos = []string{repo}
	if err := lane.Save(); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "prs")
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "ci-queue"})
	if err != nil {
		t.Fatal(err)
	}

	record := &Record{Key: "pr", Kind: "github", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Number: 17, RoomID: rm.ID, Title: "Show running local CI per machine", URL: "https://github.com/owner/repo/pull/17", Head: head, Base: base, Branch: "feature", RepoPath: repo, Watching: true, State: "open", Created: time.Unix(2, 0)}
	saved := State{Version: 1, Records: map[string]*Record{record.Key: record}}
	if err := state.Write(root, saved); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1000, 0)
	s := &Service{Store: &Store{Root: root}, LocalCIRegistry: &runningCIRegistry{}, Notify: notify.New(rooms, subagent.NewManager()), Now: func() time.Time { return started }}
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
	started, cancelled, teardown, finished chan struct{}
	start, cancel, finish                  sync.Once
	ctx                                    context.Context
}

func newFakeNodeWork() *fakeNodeWork {
	return &fakeNodeWork{started: make(chan struct{}), cancelled: make(chan struct{}), teardown: make(chan struct{}), finished: make(chan struct{})}
}

func (f *fakeNodeWork) work(ctx context.Context) (localci.Result, error) {
	f.start.Do(func() {
		f.ctx = ctx
		close(f.started)
	})
	<-ctx.Done()
	f.cancel.Do(func() { close(f.cancelled) })
	// The node work is not over: the containers and the keeper are still there.
	<-f.teardown
	f.finish.Do(func() { close(f.finished) })
	return localci.Result{}, ctx.Err()
}

// A local CI run must end with its engine. A run that outlives the engine leaves
// a keeper on the machine, and that keeper holds the cache lock for hours, so an
// engine that stops cancels every run it admitted, a pull request head and a
// local head alike, and waits for their work on the machine to end.
func TestStopLocalCICancelsRunsAndWaitsForTheirNodeWork(t *testing.T) {
	s, saved, _ := scheduledCIRunFixture(t)
	record := saved.Records["pr"]
	head := record.Head
	localHead := addCommit(t, record.RepoPath, "local.txt")
	local := *record
	local.Key, local.Kind, local.Head = "local", "local", localHead
	scheduled, lane := newFakeNodeWork(), newFakeNodeWork()
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, req localci.Request) (localci.Result, error) {
		if req.SHA == head {
			return scheduled.work(ctx)
		}
		return lane.work(ctx)
	}

	if err := s.scheduleRegisteredCI(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	<-scheduled.started
	if _, err := s.localTrialCI(context.Background(), local, localHead); err != nil {
		t.Fatal(err)
	}
	<-lane.started

	// Cancellation starts with the engine and does not wait for the teardown.
	s.CancelLocalCI()
	if scheduled.ctx.Err() == nil || lane.ctx.Err() == nil {
		t.Fatalf("a run was not cancelled with the engine: scheduled=%v local=%v", scheduled.ctx.Err(), lane.ctx.Err())
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
	want := []string{head[:7] + " on hermes", localHead[:7] + " on hermes"}
	slices.Sort(want)
	left := s.StopLocalCI(expired)
	slices.Sort(left)
	if !slices.Equal(left, want) {
		t.Fatalf("leftover runs = %v, want %v", left, want)
	}

	// Both teardowns end, and then the stop returns.
	close(scheduled.teardown)
	close(lane.teardown)
	if left := <-stopped; len(left) != 0 {
		t.Fatalf("stop reported %v", left)
	}
}

// oneSlotRegistry offers one machine with a single slot, so the queue order
// decides which head runs.
type oneSlotRegistry struct{ runningCIRegistry }

func (r *oneSlotRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "hermes", Slots: 1, Enabled: true}}, nil
}

// localTrialQueueFixture offers a one-slot machine, a running local trial batch
// and a pending pull request head, so the queue order decides what runs.
func localTrialQueueFixture(t *testing.T) (*Service, State, *Record, string) {
	t.Helper()
	s, saved, _ := scheduledCIRunFixture(t)
	s.LocalCIRegistry = &oneSlotRegistry{}
	record := saved.Records["pr"]
	localHead := addCommit(t, record.RepoPath, "local.txt")
	local := *record
	local.Key, local.Kind, local.Head, local.Number = "local", "local", localHead, 0
	batch := &state.MergeTrial{Phase: state.MergeTrialPhaseRunning, Branch: "dyna/merge-trial/7", Base: record.Base, Head: localHead,
		Changes: []state.MergeTrialChange{{Key: local.Key, Head: localHead}}, Created: record.Created.Add(time.Hour)}
	st := State{Version: 1, Records: map[string]*Record{record.Key: record, local.Key: &local}, MergeTrials: map[string]*state.MergeTrial{defaultRepo(local): batch}}
	if err := state.Write(s.Store.Root, st); err != nil {
		t.Fatal(err)
	}
	return s, st, &local, localHead
}

// A local trial enters the one admission queue before ordinary pull request
// heads, so a backlog of hosted work cannot starve a local merge.
func TestLocalTrialTakesTheQueueBeforePullRequestHeads(t *testing.T) {
	s, st, _, localHead := localTrialQueueFixture(t)
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, _ localci.Request) (localci.Result, error) {
		<-ctx.Done()
		return localci.Result{}, ctx.Err()
	}
	t.Cleanup(s.CancelLocalCI)
	if err := s.scheduleRegisteredCI(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	// The one slot went to the local trial, so the pull request head still waits.
	runs := s.RunningCI()
	if len(runs) != 1 || runs[0].Head != localHead[:7] {
		t.Fatalf("machine runs = %+v, want the local trial head %s", runs, localHead[:7])
	}
}

// A held merge queue admits no local run. The trial that reads the result cannot
// stop a run the scheduler started on its own.
func TestHeldMergeQueueAdmitsNoLocalRun(t *testing.T) {
	s, st, local, localHead := localTrialQueueFixture(t)
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, _ localci.Request) (localci.Result, error) {
		<-ctx.Done()
		return localci.Result{}, ctx.Err()
	}
	t.Cleanup(s.CancelLocalCI)
	if err := s.Store.update(func(read *State) error {
		read.MergePauses = map[string]state.MergePause{local.RoomID: {Reason: "merge queue paused for recovery"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Only the local trial is queued, so any admitted run is its own.
	only := st
	only.Records = map[string]*Record{local.Key: local}
	if err := s.scheduleRegisteredCI(context.Background(), only); err != nil {
		t.Fatal(err)
	}
	if head := runningLocalCIHead(s); head != "" {
		t.Fatalf("a held queue admitted the run for %s", head)
	}
	if _, err := s.localTrialCI(context.Background(), *local, localHead); err != nil {
		t.Fatal(err)
	}
	if head := runningLocalCIHead(s); head != "" {
		t.Fatalf("a held queue admitted the run for %s", head)
	}
}

// A poll tick never cancels a local head's run. No hosted poll decides that
// head, so only the engine's stop ends its run.
func TestPollTickKeepsALocalRunInFlight(t *testing.T) {
	s, saved, _ := scheduledCIRunFixture(t)
	record := saved.Records["pr"]
	localHead := addCommit(t, record.RepoPath, "local.txt")
	local := *record
	local.Key, local.Kind, local.Head = "local", "local", localHead
	lane := newFakeNodeWork()
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, _ localci.Request) (localci.Result, error) {
		return lane.work(ctx)
	}
	if _, err := s.localTrialCI(context.Background(), local, localHead); err != nil {
		t.Fatal(err)
	}
	<-lane.started
	if err := s.scheduleRegisteredCI(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if err := lane.ctx.Err(); err != nil {
		t.Fatalf("a poll tick cancelled the local run: %v", err)
	}
	s.CancelLocalCI()
	close(lane.teardown)
	<-lane.finished
}

// A stop closes admission once. A tick and a local trial that are still
// finishing cannot start a run after the engine has cancelled the runs it knew:
// that run's keeper would outlive the process.
func TestStopLocalCIRefusesRunsScheduledAfterTheStop(t *testing.T) {
	s, saved, _ := scheduledCIRunFixture(t)
	record := saved.Records["pr"]
	head := record.Head
	localHead := addCommit(t, record.RepoPath, "local.txt")
	local := *record
	local.Key, local.Kind, local.Head = "local", "local", localHead
	scheduled, lane := newFakeNodeWork(), newFakeNodeWork()
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, req localci.Request) (localci.Result, error) {
		if req.SHA == head {
			return scheduled.work(ctx)
		}
		return lane.work(ctx)
	}
	if err := s.scheduleRegisteredCI(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	<-scheduled.started
	if _, err := s.localTrialCI(context.Background(), local, localHead); err != nil {
		t.Fatal(err)
	}
	<-lane.started

	s.CancelLocalCI()
	known := len(s.RunningCI())
	if known != 2 {
		t.Fatalf("the engine knows %d runs, want both heads it started", known)
	}

	// The run set refuses both kinds of work, so only the runs the engine already
	// had stay in it.
	lateHead := addCommit(t, record.RepoPath, "late.txt")
	late := *record
	late.Key, late.Number, late.Head = "late", 18, lateHead
	lateState := State{Version: 1, Records: map[string]*Record{late.Key: &late}}
	if err := s.scheduleRegisteredCI(context.Background(), lateState); err != nil {
		t.Fatal(err)
	}
	if runs := s.RunningCI(); len(runs) != known {
		t.Fatalf("a scheduled run was admitted after the stop: %+v", runs)
	}
	lateLocal := late
	lateLocal.Kind = "local"
	if _, err := s.localTrialCI(context.Background(), lateLocal, lateHead); err != nil {
		t.Fatal(err)
	}
	job, err := s.localTrialJob(lateLocal, lateHead)
	if err != nil {
		t.Fatal(err)
	}
	if admittedRun(s, job) {
		t.Fatal("a local head's run was admitted after the stop")
	}

	// Release the two runs the engine already had.
	close(scheduled.teardown)
	close(lane.teardown)
	<-scheduled.finished
	<-lane.finished
}

// addCommit writes one file and commits it, and returns the new head.
func addCommit(t *testing.T, repo, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "-C", repo, "add", name).CombinedOutput()
	if err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err = exec.Command("git", "-C", repo, "commit", "--quiet", "-m", name).CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	out, err = exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}
