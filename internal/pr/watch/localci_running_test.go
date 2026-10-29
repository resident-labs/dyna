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

	"dyna-agent/internal/jobs"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// runningCIRegistry offers one 16-slot machine and no saved result.
type runningCIRegistry struct{ hostedCIRegistry }

func (r *runningCIRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "hermes", Enabled: true}}, nil
}
func (r *runningCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}
func (r *runningCIRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	return localci.Result{}, os.ErrNotExist
}

// scheduledCIRunFixture offers one pull request in a merge trial whose head
// needs local CI, and a service that can schedule it.
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

	root := filepath.Join(t.TempDir(), "prs")
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "ci-queue"})
	if err != nil {
		t.Fatal(err)
	}

	record := &Record{Key: "pr", Kind: "github", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Number: 17, RoomID: rm.ID, Title: "Show running local CI per machine", URL: "https://github.com/owner/repo/pull/17", Head: head, Base: base, Branch: "dyna/merge-trial/7", RepoPath: repo, Watching: true, State: "open", Created: time.Unix(2, 0)}
	batch := &state.MergeTrial{Phase: state.MergeTrialPhaseRunning, Branch: record.Branch, Base: base, Head: head, Changes: []state.MergeTrialChange{{Key: record.Key, Head: head}}, Created: time.Unix(2, 0)}
	saved := State{Version: 1, Records: map[string]*Record{record.Key: record}, MergeTrials: map[string]*state.MergeTrial{defaultRepo(*record): batch}}
	if err := state.Write(root, saved); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1000, 0)
	// Each fixture owns its run set, so no run from another test can wait on this
	// test's engine stop.
	s := &Service{Store: &Store{Root: root}, LocalCIRegistry: &runningCIRegistry{}, Notify: notify.New(rooms, subagent.NewManager()), Now: func() time.Time { return started }, registeredRuns: &registeredCIRuns{runs: map[string]*registeredCIRun{}}}
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) == "git" {
			return nil
		}
		for _, arg := range cmd.Args {
			switch {
			case strings.Contains(arg, "check-runs?"):
				return writeFakeJSON(cmd.Stdout, checkRunsResponse{})
			case strings.Contains(arg, "actions/runs?"):
				return writeFakeJSON(cmd.Stdout, workflowRunsResponse{})
			case strings.Contains(arg, "/statuses?"):
				return writeFakeJSON(cmd.Stdout, []Status{})
			}
		}
		return writeFakeJSON(cmd.Stdout, emptyResponse{})
	}
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
	if got.Machine != "hermes" || got.Repo != "owner/repo" || got.Number != 17 || got.Head != head[:7] || !got.Trial || !got.Started.Equal(started) {
		t.Fatalf("running CI = %+v", got)
	}
	if got.Title != record.Title || got.URL != record.URL {
		t.Fatalf("running CI names %q at %q, want the PR", got.Title, got.URL)
	}
	// A merge trial runs every workflow suite, not only the ones its files select.
	if suites := strings.Join(got.Suites, ","); suites != "relay,pi,go,cross,computer-smoke" {
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

// localCIEnv opens one local change in the local lane, with a fake machine and
// a blocked GitHub, so a local head can take a run.
func localCIEnv(t *testing.T) (*testEnv, Record, string, *fakeLocalCI) {
	t.Helper()
	e, dir, records, ci := localMergeEnvFor(t, false, "local")
	return e, records[0], dir, ci
}

// A local CI run must end with its engine. A run that outlives the engine leaves
// a keeper on the machine, and that keeper holds the cache lock for hours, so an
// engine that stops cancels every run it admitted and waits for its work on the
// machine to end.
func TestStopLocalCICancelsRunsAndWaitsForTheirNodeWork(t *testing.T) {
	e, record, dir, _ := localCIEnv(t)
	s := e.s
	trialHead := addCommit(t, dir, "trial.txt")
	syncHead := addCommit(t, dir, "sync.txt")
	trial, integration := newFakeNodeWork(), newFakeNodeWork()
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, req localci.Request) (localci.Result, error) {
		if req.SHA == trialHead {
			return trial.work(ctx)
		}
		return integration.work(ctx)
	}
	sync := Record{Kind: "local", Key: "integration", RoomID: record.RoomID, RepoPath: record.RepoPath}
	if _, err := s.localTrialCI(context.Background(), record, trialHead); err != nil {
		t.Fatal(err)
	}
	if _, err := s.localSyncCI(context.Background(), sync, syncHead); err != nil {
		t.Fatal(err)
	}
	// Admission registers a run before it starts, so a missing run fails here
	// instead of waiting.
	if runs := s.RunningCI(); len(runs) != 2 {
		t.Fatalf("machine runs = %+v, want the trial and the sync", runs)
	}
	<-trial.started
	<-integration.started

	// Cancellation starts with the engine and does not wait for the teardown.
	s.CancelLocalCI()
	if trial.ctx.Err() == nil || integration.ctx.Err() == nil {
		t.Fatalf("a run was not cancelled with the engine: trial=%v sync=%v", trial.ctx.Err(), integration.ctx.Err())
	}

	// A stop whose budget is already gone names the runs still working on the
	// machine, because neither has ended its node work.
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	want := []string{trialHead[:7] + " on hermes", syncHead[:7] + " on hermes"}
	slices.Sort(want)
	left := s.StopLocalCI(expired)
	slices.Sort(left)
	if !slices.Equal(left, want) {
		t.Fatalf("leftover runs = %v, want %v", left, want)
	}

	// Both teardowns end, so a stop with a live budget finds nothing left.
	close(trial.teardown)
	close(integration.teardown)
	<-trial.finished
	<-integration.finished
	if left := s.StopLocalCI(context.Background()); len(left) != 0 {
		t.Fatalf("stop reported %v", left)
	}
}

// oneSlotRegistry offers one two-CPU machine, so the queue order decides
// which head runs.
type oneSlotRegistry struct{ runningCIRegistry }

func (r *oneSlotRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "hermes", Enabled: true, CPUs: 2}}, nil
}

// hostedJob is a tracked pull request record that shares the local repository, so
// one queue can hold both kinds of work. Its head is a placeholder: the local
// trial takes the machine's only slot, so the pull request never prepares.
func hostedJob(t *testing.T, e *testEnv, local Record) *Record {
	t.Helper()
	r := local
	r.Key, r.ChangeID, r.Kind = "pull-request", "pull-request", "github"
	r.Host, r.Repo, r.HeadRepo, r.Number = "github.com", "owner/repo", "owner/repo", 17
	r.Head, r.Branch, r.Title = strings.Repeat("c", 40), "feature", "Fix the parser"
	r.URL = "https://github.com/owner/repo/pull/17"
	if err := e.s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &r
}

// A full node queue keeps a pull request head from preparing, and the local
// trial still runs once the node frees: a pull request head never queues for a
// machine, so a backlog of pull requests cannot starve a local merge.
func TestLocalTrialRunsWhilePullRequestHeadsWaitForAFullNode(t *testing.T) {
	e, record, dir, ci := localCIEnv(t)
	e.s.LocalCIRegistry = &oneSlotRegistry{}
	hostedJob(t, e, record)
	trialHead := addCommit(t, dir, "trial.txt")
	ci.started = make(chan struct{}, 1)
	ci.release = make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(ci.release)
		}
		e.s.CancelLocalCI()
	})
	// Other work holds the node's whole queue.
	queue := jobs.Admission{DataDir: filepath.Dir(e.s.Store.Root)}
	if _, err := queue.Hold(t.Context(), jobs.Demand{Node: "hermes", ID: "occupied", CPUs: 2}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(record): {Phase: state.MergeTrialPhaseRunning, Branch: "dyna/merge-trial/7", Base: record.Base, Head: trialHead,
		Changes: []state.MergeTrialChange{{Key: record.Key, Head: record.Head}}, Created: e.clock}}
	if err := e.s.scheduleRegisteredCI(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	// The pull request head prepared nothing on the full node: no run, and no
	// contact with GitHub.
	if runs := e.s.RunningCI(); len(runs) != 0 {
		t.Fatalf("a full node started %+v", runs)
	}
	// The node frees, and the trial takes it through its own reader.
	if err := queue.Release(t.Context(), "hermes", "occupied"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.localTrialCI(context.Background(), record, trialHead); err != nil {
		t.Fatal(err)
	}
	runs := e.s.RunningCI()
	if len(runs) != 1 || runs[0].Head != trialHead[:7] || runs[0].Kind != RunningCILocalChange {
		t.Fatalf("machine runs = %+v, want the local trial head %s", runs, trialHead[:7])
	}
}

// A held merge queue admits no local run. The trial that reads the result cannot
// stop a run the scheduler started on its own.
func TestHeldMergeQueueAdmitsNoLocalRun(t *testing.T) {
	e, record, dir, ci := localCIEnv(t)
	trialHead := addCommit(t, dir, "trial.txt")
	ci.started = make(chan struct{}, 1)
	ci.release = make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(ci.release)
		}
		e.s.CancelLocalCI()
	})
	if err := e.s.Store.update(func(read *State) error {
		read.MergePauses = map[string]state.MergePause{record.RoomID: {Reason: "merge queue paused for recovery"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(record): {Phase: state.MergeTrialPhaseRunning, Branch: "dyna/merge-trial/7", Base: record.Base, Head: trialHead,
		Changes: []state.MergeTrialChange{{Key: record.Key, Head: record.Head}}, Created: e.clock}}
	if err := e.s.scheduleRegisteredCI(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if head := runningLocalCIHead(e.s); head != "" {
		t.Fatalf("a held queue admitted the run for %s", head)
	}
	if _, err := e.s.localTrialCI(context.Background(), record, trialHead); err != nil {
		t.Fatal(err)
	}
	if head := runningLocalCIHead(e.s); head != "" {
		t.Fatalf("a held queue admitted the run for %s", head)
	}
}

// The machine view names what a run checks, so a local change and an
// integration sync, which have no pull request number, read clearly.
func TestRunningCINamesLocalAndSyncWork(t *testing.T) {
	e, record, dir, ci := localCIEnv(t)
	trialHead := addCommit(t, dir, "trial.txt")
	syncHead := addCommit(t, dir, "sync.txt")
	ci.started = make(chan struct{}, 2)
	ci.release = make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(ci.release)
		}
		e.s.CancelLocalCI()
	})
	sync := Record{Kind: "local", Key: "integration", RoomID: record.RoomID, RepoPath: record.RepoPath}
	if _, err := e.s.localTrialCI(context.Background(), record, trialHead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.localSyncCI(context.Background(), sync, syncHead); err != nil {
		t.Fatal(err)
	}
	<-ci.started
	runs := map[string]RunningCI{}
	for _, run := range e.s.RunningCI() {
		runs[run.Head] = run
	}
	change, ok := runs[trialHead[:7]]
	if !ok || change.Kind != RunningCILocalChange || change.Number != 0 || change.Title != record.Title {
		t.Fatalf("local change run = %+v", change)
	}
	integration, ok := runs[syncHead[:7]]
	if !ok || integration.Kind != RunningCISync || integration.Number != 0 || integration.Trial {
		t.Fatalf("sync run = %+v", integration)
	}
}

// A poll tick never cancels a local head's run. No hosted poll decides that
// head, so only the engine's stop ends its run.
func TestPollTickKeepsALocalRunInFlight(t *testing.T) {
	e, record, dir, ci := localCIEnv(t)
	trialHead := addCommit(t, dir, "trial.txt")
	ci.started = make(chan struct{}, 1)
	ci.release = make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(ci.release)
		}
		e.s.CancelLocalCI()
	})
	if _, err := e.s.localTrialCI(context.Background(), record, trialHead); err != nil {
		t.Fatal(err)
	}
	<-ci.started
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.scheduleRegisteredCI(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	if head := runningLocalCIHead(e.s); head != trialHead {
		t.Fatalf("a poll tick dropped the local run for %s", head)
	}
}

// A stop closes admission once. A tick and a local trial that are still
// finishing cannot start a run after the engine has cancelled the runs it knew:
// that run's keeper would outlive the process.
func TestStopLocalCIRefusesRunsScheduledAfterTheStop(t *testing.T) {
	e, record, dir, ci := localCIEnv(t)
	s := e.s
	trialHead := addCommit(t, dir, "trial.txt")
	ci.started = make(chan struct{}, 1)
	ci.release = make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(ci.release)
		}
	})
	if _, err := s.localTrialCI(context.Background(), record, trialHead); err != nil {
		t.Fatal(err)
	}
	<-ci.started
	if runs := s.RunningCI(); len(runs) != 1 {
		t.Fatalf("machine runs = %+v, want the local trial", runs)
	}
	s.CancelLocalCI()

	// The run set refuses new work, so a head that arrives after the stop gets no
	// run of its own.
	lateHead := addCommit(t, dir, "late.txt")
	late := record
	late.Key, late.Head = "late", lateHead
	if _, err := s.localTrialCI(context.Background(), late, lateHead); err != nil {
		t.Fatal(err)
	}
	job, err := s.localTrialJob(late, lateHead)
	if err != nil {
		t.Fatal(err)
	}
	if admittedRun(s, job) {
		t.Fatal("a local head's run was admitted after the stop")
	}
	if head := runningLocalCIHead(s); head == lateHead {
		t.Fatalf("a run was admitted for %s after the stop", head)
	}
}

// addCommit writes one file and commits it, and returns the new head.
// addCommit writes one file and commits it through the shared git helper, which
// sets the identity a CI container has none of.
func addCommit(t *testing.T, repo, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, name), []byte(name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, repo, "add", name)
	changeGit(t, repo, "commit", "--quiet", "-m", name)
	return changeGit(t, repo, "rev-parse", "HEAD")
}

// A run the engine's own stop cancelled leaves durable evidence: the saved
// result carries the engine-stop cause, the head is not finished, and the local
// trial reader treats it as unfinished, so the same commit runs again.
func TestEngineStopLeavesDurableRestartEvidence(t *testing.T) {
	e, record, dir, ci := localCIEnv(t)
	s := e.s
	head := addCommit(t, dir, "engine-stop.txt")
	work := newFakeNodeWork()
	s.RunNodeCI = func(ctx context.Context, _ localci.Node, _ localci.Request) (localci.Result, error) {
		return work.work(ctx)
	}
	ci.saved = make(chan struct{}, 1)
	// The engine marks itself before it cancels, as stopEngine does.
	localci.MarkEngineStopping()
	t.Cleanup(localci.ClearEngineStopping)
	done := make(chan error, 1)
	go func() {
		_, err := s.localTrialCI(context.Background(), record, head)
		done <- err
	}()
	<-work.started
	s.CancelLocalCI()
	close(work.teardown)
	<-work.finished
	// Wait for the recovery to persist before the result is read.
	select {
	case <-ci.saved:
	case <-time.After(5 * time.Second):
		t.Fatal("the engine-stop result was never saved")
	}
	if err := <-done; err != nil {
		t.Fatalf("the cancelled trial reported %v", err)
	}
	repo, err := localci.CanonicalRepo(t.Context(), record.RepoPath)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := ci.LoadResult(t.Context(), repo, head)
	if err != nil {
		t.Fatalf("the engine-stop run saved no evidence: %v", err)
	}
	if saved.Cause != localci.CauseEngineStop || saved.Status != localci.OutcomeError {
		t.Fatalf("saved evidence = %s/%s, want error/%s", saved.Status, saved.Cause, localci.CauseEngineStop)
	}
	job := registeredCIJob{record: record, head: head, trial: true}
	if !s.registeredJobResultComplete(t.Context(), job, saved) {
		t.Fatal("a stopped run must reach the retry decision, not be re-admitted blindly")
	}
	if act := savedResultActionFor(saved); act != retryStoppedRun {
		t.Fatalf("a stopped run action = %v, want retryStoppedRun", act)
	}
	// The real reader the trial uses must stay undecided on that evidence.
	trialJob, err := s.localTrialJob(record, head)
	if err != nil {
		t.Fatal(err)
	}
	read, err := s.savedTrialCI(t.Context(), trialJob)
	if err != nil {
		t.Fatal(err)
	}
	if read.Status != VerdictPending {
		t.Fatalf("the trial reader decided %s on an engine stop, want pending", read.Status)
	}
	// The restart forgets the runs in flight, and the same observation must then
	// admit another run for that head: a sync head has no other scheduler entry,
	// so pending alone would wait forever.
	second := newFakeNodeWork()
	restarted := *s
	restarted.registeredRuns = &registeredCIRuns{runs: map[string]*registeredCIRun{}}
	restarted.RunNodeCI = func(ctx context.Context, _ localci.Node, _ localci.Request) (localci.Result, error) {
		return second.work(ctx)
	}
	again := make(chan trialCI, 1)
	go func() {
		read, err := restarted.savedTrialCI(context.Background(), trialJob)
		if err != nil {
			again <- trialCI{Status: VerdictFailed, Reason: err.Error()}
			return
		}
		again <- read
	}()
	select {
	case <-second.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the reader did not admit another run after the engine stop")
	}
	close(second.teardown)
	if read := <-again; read.Status != VerdictPending {
		t.Fatalf("the retried run reported %s, want pending", read.Status)
	}
}
