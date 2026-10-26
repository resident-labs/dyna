package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/hooks"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
	statewrite "dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func TestLocalMergeQueueUsesExactApproval(t *testing.T) {
	e, _, _ := mergeEnv(t, 1)
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Records {
		r.Kind = "local"
		r.RepoPath = "/repo/.git"
		r.CI = ""
		st.RepositoryFacts = map[string]state.RepositoryFacts{defaultRepo(*r): {DefaultBranch: "dyna/integration", MergeTrialCI: true, CheckedAt: e.clock}}
	}
	rows := mergeQueue(st, nil)
	if len(rows) != 1 || rows[0].State != "ready" {
		t.Fatalf("local queue = %+v", rows)
	}
	for _, r := range st.Records {
		r.RoundHead = "old-head"
	}
	if rows := mergeQueue(st, nil); len(rows) != 1 || rows[0].State != "review" {
		t.Fatalf("stale approval = %+v", rows)
	}
}

// fakeLocalCI is the local lane's machine, run set and result store. It saves
// what a run produced, as the node runner does, and it holds a run when the test
// wants to observe it in flight.
type fakeLocalCI struct {
	mu        sync.Mutex
	results   map[string]localci.Result
	completed chan struct{}
	saved     chan struct{}
	requests  []localci.Request
	bad       bool
	started   chan struct{}
	release   chan struct{}
	// nodes, when set, are the machines this registry offers. A test that leaves
	// it empty gets one 16-slot machine named hermes that allows every room and
	// every suite.
	nodes []localci.Node
}

func (f *fakeLocalCI) Nodes(context.Context) ([]localci.Node, error) {
	if len(f.nodes) > 0 {
		return f.nodes, nil
	}
	return []localci.Node{{Name: "hermes", Slots: 16, Enabled: true}}, nil
}
func (f *fakeLocalCI) Mode(context.Context, string) (string, error) { return localci.ModeLocal, nil }
func (f *fakeLocalCI) SaveResult(_ context.Context, result localci.Result) error {
	f.mu.Lock()
	f.results[result.SHA] = result
	f.mu.Unlock()
	if f.saved != nil {
		f.saved <- struct{}{}
	}
	return nil
}

// requestFor returns the request of the run that checked a head.
func (f *fakeLocalCI) requestFor(t *testing.T, sha string) localci.Request {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, req := range f.requests {
		if req.SHA == sha {
			return req
		}
	}
	t.Fatalf("no run checked %s", sha)
	return localci.Request{}
}
func (f *fakeLocalCI) LoadResult(_ context.Context, _, sha string) (localci.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if result, ok := f.results[sha]; ok {
		return result, nil
	}
	return localci.Result{}, os.ErrNotExist
}
func (f *fakeLocalCI) LoadHostedRuns(context.Context, string, string) (localci.HostedRuns, error) {
	return localci.HostedRuns{}, nil
}
func (f *fakeLocalCI) SaveHostedRuns(context.Context, string, string, localci.HostedRuns) error {
	return nil
}

func (f *fakeLocalCI) run(ctx context.Context, _ localci.Node, req localci.Request) (localci.Result, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	if f.started != nil {
		f.started <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
			return localci.Result{}, ctx.Err()
		}
	}
	status := localci.OutcomePassed
	if f.bad {
		// The fake tests the actual trial tree, rather than the original change SHA.
		cmd := gitCommand(ctx, req.Repo, "cat-file", "-e", req.SHA+":bad.txt")
		if cmd.Run() == nil {
			status = localci.OutcomeFailed
		}
	}
	var checks []localci.SuiteResult
	for _, name := range req.Definition.RequiredChecks(req.Suites) {
		checks = append(checks, localci.SuiteResult{Suite: localci.Suite(name), CheckName: name, Node: "local", Status: status})
	}
	errText := ""
	if status == localci.OutcomeFailed {
		errText = "bad change failed"
	}
	result := localci.Result{Repo: req.Repo, SHA: req.SHA, Node: "local", Status: status, Suites: checks, Checks: checks, Error: errText}
	f.completed <- struct{}{}
	return result, nil
}

func localMergeEnv(t *testing.T, bad bool) (*testEnv, string, []Record, *fakeLocalCI) {
	t.Helper()
	return localMergeEnvFor(t, bad, "good", "bad", "later")
}

// localMergeEnvFor opens one approved local change per name. Fewer changes mean fewer git processes.
func localMergeEnvFor(t *testing.T, bad bool, names ...string) (*testEnv, string, []Record, *fakeLocalCI) {
	t.Helper()
	e, _, _ := mergeEnv(t)
	dir, base := changeRepo(t)
	changeGit(t, dir, "update-ref", "refs/remotes/origin/main", base)
	changeGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	v.LocalLaneRepos = []string{dir}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	ci := &fakeLocalCI{results: map[string]localci.Result{}, completed: make(chan struct{}, 20), saved: make(chan struct{}, 20), bad: bad}
	e.s.LocalCIRegistry = ci
	e.s.RunNodeCI = ci.run
	e.s.GH.run = func(context.Context, *exec.Cmd) error {
		t.Error("local merge contacted GitHub")
		return fmt.Errorf("unexpected GitHub call")
	}
	var records []Record
	for i, name := range names {
		changeGit(t, dir, "checkout", "-b", name, "main")
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		changeGit(t, dir, "add", ".")
		changeGit(t, dir, "commit", "-m", name)
		r, _, err := e.s.OpenChange(context.Background(), authority.Actor{Role: authority.Owner}, e.author, OpenChangeOptions{Dir: dir})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.s.Store.update(func(st *State) error {
			live := st.Records[r.Key]
			live.Verdict = "approve"
			live.RoundHead = live.Head
			live.Created = e.clock.Add(time.Duration(i) * time.Second)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		r, _ = e.s.Store.Get(r.Key)
		records = append(records, r)
	}
	return e, dir, records, ci
}

// admittedRun reports whether the run set holds this job.
func admittedRun(s *Service, job registeredCIJob) bool {
	runs := s.nodeRuns()
	runs.Lock()
	defer runs.Unlock()
	return runs.runs[registeredJobKey(job)] != nil
}

// runningLocalCIHead names the head of a run in flight, or "" when the run set
// is empty.
func runningLocalCIHead(s *Service) string {
	runs := s.nodeRuns()
	runs.Lock()
	defer runs.Unlock()
	for _, run := range runs.runs {
		return run.head
	}
	return ""
}

func localTick(t *testing.T, e *testEnv) {
	t.Helper()
	if _, err := e.s.MergeTick(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func finishLocalTrials(t *testing.T, e *testEnv, ci *fakeLocalCI, bisect bool) {
	t.Helper()
	deadline, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 20; i++ {
		localTick(t, e)
		st, _ := e.s.Store.Read()
		if len(st.MergeTrials) == 0 {
			return
		}
		if bisect {
			for _, b := range st.MergeTrials {
				if b.Phase != "awaiting-root" {
					continue
				}
				if st.MergePauses[e.author.RoomID].Reason == "" || len(b.Stacks) != 0 || !b.Notified {
					t.Fatalf("failed local batch did not stop and notify root: %+v", b)
				}
				localTick(t, e)
				if err := e.s.MergeControl(t.Context(), e.author.RoomID, "bisect", nil, nil, nil); err != nil {
					t.Fatal(err)
				}
				bisect = false
			}
		}
		// Wait for the result a run in flight is about to save, so the next tick
		// reads evidence instead of starting the same head again.
		if head := runningLocalCIHead(e.s); head != "" {
			select {
			case <-ci.saved:
			case <-deadline.Done():
				t.Fatalf("local CI for %s did not save its result", head)
			}
		}
	}
	t.Fatal("local trials did not finish")
}

func TestLocalTrialPublishesBatchWithoutGitHub(t *testing.T) {
	e, dir, records, ci := localMergeEnv(t, false)
	ci.started = make(chan struct{}, 4)
	ci.release = make(chan struct{})
	if err := statewrite.MigrateMirrors(store.DataDir()); err != nil {
		t.Fatal(err)
	}
	h := hooks.New()
	if err := h.Add(context.Background(), dir, hooks.Hook{Name: "after", Cwd: "worktree", Event: hooks.PRMerged, Command: "true", Timeout: "1m", OnFail: "alert"}); err != nil {
		t.Fatal(err)
	}
	localTick(t, e)
	base := changeGit(t, dir, "rev-parse", "dyna/integration")
	localTick(t, e)
	if got := changeGit(t, dir, "rev-parse", "dyna/integration"); got != base {
		t.Fatal("integration moved before CI result")
	}
	// Admission registers the run before it starts, so a run in flight is
	// visible here without waiting on a clock.
	if runningLocalCIHead(e.s) == "" {
		t.Fatal("local trial CI did not start")
	}
	localTick(t, e)
	if got := changeGit(t, dir, "rev-parse", "dyna/integration"); got != base {
		t.Fatal("integration moved while local CI ran")
	}
	close(ci.release)
	finishLocalTrials(t, e, ci, false)
	head := changeGit(t, dir, "rev-parse", "dyna/integration")
	for _, r := range records {
		saved, _ := e.s.Store.Get(r.Key)
		if saved.State != "merged" || saved.Watching {
			t.Fatalf("change not merged: %+v", saved)
		}
		changeGit(t, dir, "merge-base", "--is-ancestor", r.Head, head)
	}
	if refs := changeGit(t, dir, "for-each-ref", "--format=%(refname)", "refs/heads/dyna/merge-trial/"); refs != "" {
		t.Fatalf("trial refs remain: %s", refs)
	}
	spool, err := os.ReadDir(filepath.Join(store.DataDir(), "hook-spool"))
	if err != nil || len(spool) == 0 {
		t.Fatalf("hook spool: %v, %v", spool, err)
	}
	// One landed event for the trial and one per local change that merged.
	if landed := mergeNotices(e.sent, room.MergeLanded); len(landed) != len(records)+1 {
		t.Fatalf("landed notices = %d, want one per change and one for the trial", len(landed))
	}
}
func TestLocalTrialRootBisectPublishesPrefixAndReturnsBadChange(t *testing.T) {
	e, dir, records, ci := localMergeEnvFor(t, true, "good", "bad")
	localTick(t, e)
	finishLocalTrials(t, e, ci, true)
	good, _ := e.s.Store.Get(records[0].Key)
	bad, _ := e.s.Store.Get(records[1].Key)
	if good.State != "merged" || bad.State != "open" || bad.MergeTrialFailure == nil {
		t.Fatalf("prefix states: %s %+v", good.State, bad)
	}
	if got := changeGit(t, dir, "ls-tree", "--name-only", "dyna/integration"); strings.Contains(got, "bad.txt") {
		t.Fatalf("bad change published: %s", got)
	}
	found := false
	for _, n := range e.sent {
		if strings.Contains(n.Text, "bad change failed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("failure notice missing: %+v", e.sent)
	}
}

func TestLocalTrialRootBisectKeepsChangeAfterBadOpen(t *testing.T) {
	e, dir, records, ci := localMergeEnvFor(t, true, "bad", "later")
	localTick(t, e)
	finishLocalTrials(t, e, ci, true)
	bad, _ := e.s.Store.Get(records[0].Key)
	later, _ := e.s.Store.Get(records[1].Key)
	if bad.State != "open" || bad.MergeTrialFailure == nil || later.State != "open" {
		t.Fatalf("states: %+v %s", bad, later.State)
	}
	if got := changeGit(t, dir, "ls-tree", "--name-only", "dyna/integration"); strings.Contains(got, "later.txt") || strings.Contains(got, "bad.txt") {
		t.Fatalf("change published: %s", got)
	}
}

func TestLocalTrialOutsideIntegrationMoveDiscardsTrial(t *testing.T) {
	e, dir, records, _ := localMergeEnv(t, false)
	localTick(t, e)
	changeGit(t, dir, "update-ref", "refs/heads/dyna/integration", records[0].Head)
	localTick(t, e)
	st, _ := e.s.Store.Read()
	if len(st.MergeTrials) != 0 {
		t.Fatalf("outside move retained trial: %+v", st.MergeTrials)
	}
	if got := changeGit(t, dir, "rev-parse", "dyna/integration"); got != records[0].Head {
		t.Fatalf("outside move overwritten: %s", got)
	}
	for _, r := range records {
		saved, _ := e.s.Store.Get(r.Key)
		if saved.State != "open" {
			t.Fatal("change marked merged after outside move")
		}
	}
}

func TestLocalMergeGuardsAndGitHubLane(t *testing.T) {
	for _, guard := range []string{"read-only", "paused", "github-lane"} {
		t.Run(guard, func(t *testing.T) {
			e, dir, _, _ := localMergeEnv(t, false)
			changeGit(t, dir, "branch", "-D", "dyna/integration")
			switch guard {
			case "read-only":
				if err := e.s.Notify.Rooms.UpdatePreferences(e.author.RoomID, func(r *room.Record) error { r.GitHubAccess = "read-only"; return nil }); err != nil {
					t.Fatal(err)
				}
			case "paused":
				if _, err := e.s.Notify.Rooms.SetSessionsPaused(context.Background(), e.author.RoomID, true); err != nil {
					t.Fatal(err)
				}
			case "github-lane":
				v, err := settings.Load("")
				if err != nil {
					t.Fatal(err)
				}
				v.LocalLaneRepos = nil
				if err := v.Save(); err != nil {
					t.Fatal(err)
				}
			}
			localTick(t, e)
			st, _ := e.s.Store.Read()
			if len(st.MergeTrials) != 0 {
				t.Fatal("held local change started a trial")
			}
			if refs := changeGit(t, dir, "for-each-ref", "--format=%(refname)", "refs/heads/dyna/integration"); refs != "" {
				t.Fatal("held local lane created integration")
			}
		})
	}
}

func TestGitHubLaneQueueIsUnchanged(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	if _, err := f.e.s.refreshMergeFacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := f.e.s.Store.Read()
	rows := mergeQueue(st, nil)
	if len(rows) != 1 || rows[0].State != "ready" {
		t.Fatalf("GitHub queue: %+v", rows)
	}
	localTick(t, f.e)
	f.ci = "completed"
	localTick(t, f.e)
	if len(*f.merged) != 1 || (*f.merged)[0] != 1 {
		t.Fatalf("GitHub publication: %v", *f.merged)
	}
}

func TestLocalTrialCIStartsOnceAndOutlivesTickContext(t *testing.T) {
	e, _, records, ci := localMergeEnv(t, false)
	ci.started = make(chan struct{}, 2)
	ci.release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	r := records[0]
	trial, err := e.s.mergeTrialCI(ctx, r, "dyna/merge-trial/1", r.Head)
	if err != nil || trial.Status != "pending" {
		t.Fatalf("start = %s %v", trial.Status, err)
	}
	deadline, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	select {
	case <-ci.started:
	case <-deadline.Done():
		t.Fatal("CI did not start")
	}
	cancel()
	trial, err = e.s.mergeTrialCI(context.Background(), r, "dyna/merge-trial/1", r.Head)
	if err != nil || trial.Status != "pending" {
		t.Fatalf("running = %s %v", trial.Status, err)
	}
	close(ci.release)
	select {
	case <-ci.saved:
	case <-deadline.Done():
		t.Fatal("CI did not save its result")
	}
	select {
	case <-ci.started:
		t.Fatal("CI started twice")
	default:
	}
	trial, err = e.s.mergeTrialCI(context.Background(), r, "dyna/merge-trial/1", r.Head)
	if err != nil || trial.Status != "passed" {
		t.Fatalf("completed = %s %v", trial.Status, err)
	}
}

// A local head's run is the same work a pull request head gets: the room's
// test budget, a share of the machine's slots, and a run the machine view names.
func TestLocalTrialUsesTheRoomTestBudgetAndMachine(t *testing.T) {
	e, _, records, ci := localMergeEnv(t, false)
	ci.started = make(chan struct{}, 1)
	ci.release = make(chan struct{})
	ci.saved = make(chan struct{}, 1)
	released := false
	t.Cleanup(func() {
		if !released {
			close(ci.release)
		}
	})
	policy := RoomPolicy(State{}, e.author.RoomID)
	policy.CITimeBudget = "enforce"
	policy.CITestMax = "1s"
	policy.CITestAllow = []string{"pkg/slow"}
	if err := e.s.SetRoomChangePolicy(e.author.RoomID, authority.Actor{Role: authority.Owner}, policy); err != nil {
		t.Fatal(err)
	}
	r := records[0]
	trial, err := e.s.mergeTrialCI(context.Background(), r, "dyna/merge-trial/1", r.Head)
	if err != nil || trial.Status != "pending" {
		t.Fatalf("start = %s %v", trial.Status, err)
	}
	// Admission registers the run before it starts, and the fake records the
	// request before it signals, so no clock decides what this test sees.
	if runs := e.s.RunningCI(); len(runs) != 1 || runs[0].Machine != "hermes" || runs[0].Head != r.Head[:7] {
		t.Fatalf("machine view does not name the local run: %+v", runs)
	}
	<-ci.started
	req := ci.requestFor(t, r.Head)
	if req.MaxTest != time.Second || !req.Allowed["pkg/slow"] {
		t.Fatalf("run used max test %s and allowlist %v, want the room budget", req.MaxTest, req.Allowed)
	}
	if req.Parallelism != registeredCIRunSlots || !req.DeferSave {
		t.Fatalf("run asked for %d parallel slots and defer=%t", req.Parallelism, req.DeferSave)
	}
	close(ci.release)
	released = true
	<-ci.saved
	if trial, err := e.s.mergeTrialCI(context.Background(), r, "dyna/merge-trial/1", r.Head); err != nil || trial.Status != "passed" {
		t.Fatalf("finished = %s %v", trial.Status, err)
	}
}

// A suite that no enabled machine allows fails the trial at once and names the
// suite: the merge would otherwise wait for a machine that can never take it.
func TestLocalTrialFailsWhenNoMachineAllowsItsSuite(t *testing.T) {
	e, _, records, ci := localMergeEnvFor(t, false, "good")
	r := records[0]
	// This machine runs only the relay suite, and a trial runs every workflow
	// suite of its base definition.
	ci.nodes = []localci.Node{{Name: "hermes", Slots: 16, Enabled: true, Suites: []string{string(localci.SuiteRelay)}}}
	trial, err := e.s.localTrialCI(context.Background(), r, r.Head)
	if err != nil {
		t.Fatal(err)
	}
	if trial.Status != VerdictFailed || trial.Reason != trial.Logs {
		t.Fatalf("trial = %s %q, want a failure whose reason is its evidence", trial.Status, trial.Reason)
	}
	if !strings.Contains(trial.Reason, string(localci.SuiteGo)) || !strings.Contains(trial.Reason, "--suites") {
		t.Fatalf("trial reason = %q, want the blocked suite and the fix", trial.Reason)
	}
	// The failure is a typed error, so no run was admitted and none waits.
	if head := runningLocalCIHead(e.s); head != "" {
		t.Fatalf("a blocked trial admitted the run for %s", head)
	}
	// A scheduler tick reports the blocked suite and keeps taking the work it
	// can take, instead of failing every tick.
	if err := e.s.Store.update(func(st *State) error {
		st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(r): {Phase: state.MergeTrialPhaseRunning, Branch: "dyna/merge-trial/1", Base: r.Base, Head: r.Head, Changes: []state.MergeTrialChange{{Key: r.Key, Head: r.Head}}, Created: e.clock}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.scheduleRegisteredCI(context.Background(), st); err != nil {
		t.Fatalf("a blocked suite failed the scheduler: %v", err)
	}
	if head := runningLocalCIHead(e.s); head != "" {
		t.Fatalf("a blocked suite started a run for %s", head)
	}
}

// A machine of another room cannot take the run, so its suites do not count.
// Without that rule a room whose own machines lack a suite would wait for ever
// on a machine it may never use.
func TestLocalTrialFailsWhenAnotherRoomOwnsTheSuite(t *testing.T) {
	e, _, records, ci := localMergeEnvFor(t, false, "good")
	r := records[0]
	// This room's machine runs only the go suite. The machine that takes every
	// suite belongs to another room, so it is not a candidate.
	ci.nodes = []localci.Node{
		{Name: "room-ci", Slots: 16, Enabled: true, Suites: []string{string(localci.SuiteGo)}, Rooms: []string{r.RoomID}},
		{Name: "other-room-ci", Slots: 16, Enabled: true, Rooms: []string{"room_other"}},
	}
	trial, err := e.s.localTrialCI(context.Background(), r, r.Head)
	if err != nil {
		t.Fatal(err)
	}
	if trial.Status != VerdictFailed || !strings.Contains(trial.Reason, string(localci.SuiteRelay)) {
		t.Fatalf("trial = %s %q, want a failure that names the relay suite", trial.Status, trial.Reason)
	}
	if head := runningLocalCIHead(e.s); head != "" {
		t.Fatalf("a blocked trial admitted the run for %s", head)
	}
}

func TestLocalTrialDoesNotReusePartialCachedCI(t *testing.T) {
	e, _, records, ci := localMergeEnv(t, false)
	ci.started = make(chan struct{}, 1)
	ci.release = make(chan struct{})
	ci.saved = make(chan struct{}, 1)
	r := records[0]
	ci.results[r.Head] = localci.Result{Repo: r.RepoPath, SHA: r.Head, Status: "passed", Suites: []localci.SuiteResult{{Suite: "frontend", Status: "passed"}}}
	trial, err := e.s.mergeTrialCI(context.Background(), r, "dyna/merge-trial/1", r.Head)
	if err != nil || trial.Status != "pending" {
		t.Fatalf("partial cached result authorized trial: %s %v", trial.Status, err)
	}
	deadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-ci.started:
	case <-deadline.Done():
		t.Fatal("full trial CI did not start")
	}
	close(ci.release)
	select {
	case <-ci.saved:
	case <-deadline.Done():
		t.Fatal("full CI did not save its result")
	}
	trial, err = e.s.mergeTrialCI(context.Background(), r, "dyna/merge-trial/1", r.Head)
	if err != nil || trial.Status != "passed" {
		t.Fatalf("full CI result = %s %v", trial.Status, err)
	}
}

func TestGitHubLaneLocalChangesDoNotWriteRepositoryFacts(t *testing.T) {
	e, _, records, _ := localMergeEnv(t, false)
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	v.LocalLaneRepos = nil
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	githubFacts := state.RepositoryFacts{DefaultBranch: "main", Head: records[0].Base, MergeTrialCI: true, CheckedAt: e.clock}
	if err := e.s.Store.update(func(st *State) error {
		st.RepositoryFacts = map[string]state.RepositoryFacts{"github.com/owner/repo": githubFacts}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.s.refreshLocalMergeFacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	if len(st.RepositoryFacts) != 1 || st.RepositoryFacts["github.com/owner/repo"] != githubFacts {
		t.Fatalf("local path changed GitHub-lane facts: %+v", st.RepositoryFacts)
	}
}

func TestLocalLaneGitHubFactsAndPublicationAreHeld(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	dir, base := changeRepo(t)
	changeGit(t, dir, "remote", "add", "origin", "https://github.com/owner/repo.git")
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	v.LocalLaneRepos = []string{dir}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	var record Record
	if err := f.e.s.Store.update(func(st *State) error {
		for _, r := range st.Records {
			r.RepoPath = dir
			record = *r
		}
		st.RepositoryFacts = map[string]state.RepositoryFacts{defaultRepo(record): {DefaultBranch: "dyna/integration", Head: base, MergeTrialCI: true, CheckedAt: f.e.clock}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.refreshMergeFacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := f.e.s.Store.Read()
	if got := st.RepositoryFacts[defaultRepo(record)]; got.DefaultBranch != "dyna/integration" || got.Head != base {
		t.Fatalf("GitHub overwrote local-lane facts: %+v", got)
	}
	if _, ready, err := f.e.s.mergeReady(context.Background(), record); err != nil || ready {
		t.Fatalf("GitHub path can merge in local lane: %v %v", ready, err)
	}
	// A GitHub record without a retained checkout still uses the configured repo's lane.
	record.RepoPath = ""
	if err := f.e.s.Store.update(func(st *State) error { st.Records[record.Key].RepoPath = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if why, err := f.e.s.trialRoomHold(context.Background(), record); err != nil || why == "" {
		t.Fatalf("checkout-free GitHub trial is not held: %q %v", why, err)
	}
	if _, err := f.e.s.refreshMergeFacts(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ = f.e.s.Store.Read()
	if got := st.RepositoryFacts[defaultRepo(record)]; got.DefaultBranch != "dyna/integration" || got.Head != base {
		t.Fatalf("checkout-free refresh overwrote facts: %+v", got)
	}

}

func TestLocalCIReadsNodeChecks(t *testing.T) {
	definition := watchDynaDefinition(t)
	suites := definition.WorkflowSuites()
	result := localci.Result{Repo: "repo", SHA: "head", Node: "hermeslab", Status: localci.OutcomePassed}
	for _, name := range definition.RequiredChecks(suites) {
		result.Checks = append(result.Checks, localci.SuiteResult{Suite: localci.Suite(name), CheckName: name, Node: "hermeslab", Status: localci.OutcomePassed})
	}
	if status, _, _, err := localCIStatus(result, "repo", "head", definition, suites); err != nil || status != VerdictPassed {
		t.Fatalf("complete node checks did not pass: %s %v", status, err)
	}
	result.Checks = result.Checks[:len(result.Checks)-1]
	if status, _, _, err := localCIStatus(result, "repo", "head", definition, suites); err != nil || status != VerdictPending {
		t.Fatalf("partial node checks did not stay pending: %s %v", status, err)
	}
}

func TestReturnedLocalFailureKeepsFailedShardEvidence(t *testing.T) {
	logs := "go-test-shard (2) (failed): shard compile error\nmore shard evidence\ngo-test (failed):\n"
	suite, excerpt := returnedLocalFailure(logs)
	if suite != "go-test-shard (2)" || !strings.Contains(excerpt, "shard compile error\nmore shard evidence") {
		t.Fatalf("lost concrete shard failure: suite=%q excerpt=%q", suite, excerpt)
	}
}

func TestLocalCIRequiresEachWorkflowSuiteToPass(t *testing.T) {
	definition := watchDynaDefinition(t)
	suites := definition.WorkflowSuites()
	result := localci.Result{Repo: "repo", SHA: "head", Status: "passed"}
	for _, suite := range suites {
		result.Suites = append(result.Suites, localci.SuiteResult{Suite: suite, Status: "passed"})
	}
	result.Suites[0].Status = "failed"
	if status, _, _, err := localCIStatus(result, "repo", "head", definition, suites); err != nil || status != "failed" {
		t.Fatalf("failed suite accepted: %s %v", status, err)
	}
}

func TestBrokenLocalCheckoutDoesNotBlockHealthyGitHubTrial(t *testing.T) {
	f := mergeTrialEnv(t, 1)
	repo, _ := changeRepo(t)
	v, err := settings.Load("")
	if err != nil {
		t.Fatal(err)
	}
	v.LocalLaneRepos = []string{repo}
	if err := v.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.e.s.MergeTick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := f.e.s.Store.update(func(st *State) error {
		broken := *st.Records["github.com/owner/repo#1"]
		broken.Key, broken.ChangeID, broken.Kind = "change_broken", "change_broken", "local"
		broken.Number, broken.RepoPath, broken.Branch = 0, filepath.Join(t.TempDir(), "missing"), "local"
		if !localMergeReady(broken) {
			t.Fatal("fixture must start with verified local facts")
		}
		st.Records[broken.Key] = &broken
		st.RepositoryFacts[defaultRepo(broken)] = state.RepositoryFacts{DefaultBranch: "dyna/integration", MergeTrialCI: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.ci = "completed"
	if _, err := f.e.s.MergeTick(t.Context()); err == nil {
		t.Fatal("missing checkout error was discarded")
	}
	if len(*f.merged) != 1 || (*f.merged)[0] != 1 {
		t.Fatalf("healthy GitHub trial did not advance: %v", *f.merged)
	}
	broken, err := f.e.s.Store.Get("change_broken")
	if err != nil {
		t.Fatal(err)
	}
	if broken.Error == "" || localMergeReady(broken) {
		t.Fatalf("broken local record remains ready: %+v", broken)
	}
	if _, ready, err := f.e.s.mergeReady(t.Context(), broken); ready {
		t.Fatalf("broken local publication: ready=%v err=%v", ready, err)
	}
}
