package watch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

type localCIRegistry interface {
	Nodes(context.Context) ([]localci.Node, error)
	Mode(context.Context, string) (string, error)
	SaveResult(context.Context, localci.Result) error
	LoadResult(context.Context, string, string) (localci.Result, error)
	LoadHostedRuns(context.Context, string, string) (localci.HostedRuns, error)
	SaveHostedRuns(context.Context, string, string, localci.HostedRuns) error
}

func (s *Service) ciRegistry() localCIRegistry {
	if s.LocalCIRegistry != nil {
		return s.LocalCIRegistry
	}
	return localci.Store{DataDir: filepath.Dir(s.Store.Root)}
}

type registeredCIRuns struct {
	sync.Mutex
	// closed stops admission when the engine stops. It is atomic, so a stop
	// sets it without waiting for a tick that holds the run lock over its work.
	closed atomic.Bool
	runs   map[string]*registeredCIRun
}

// registeredCIRun is one local CI run in flight. node, slots and cancel belong
// to the scheduler; the other fields describe the run to a reader of
// RunningCI.
type registeredCIRun struct {
	node   string
	slots  int
	cancel context.CancelFunc
	// stopped is closed when the run's work on the node has ended. An engine that
	// stops waits on it, because a run that outlives its engine leaves a keeper
	// on the machine that holds the cache lock for hours.
	stopped  chan struct{}
	stopOnce sync.Once

	repo   string
	number int
	// kind is the work the run checks, which the machine view words.
	kind RunningCIKind
	// branch names a local change's branch, which has no pull request number.
	branch string
	trial  bool
	// local marks a run for a local head. No hosted poll decides that head: the
	// trial or the sync operation that admitted the run reads its own saved
	// result, and only the engine's stop cancels the run.
	local   bool
	head    string
	title   string
	url     string
	started time.Time
	suites  []localci.Suite
}
type registeredCIJob struct {
	record       Record
	head, branch string
	base         string
	trial        bool
	// wholeSuite runs every workflow suite of the base definition instead of
	// only the suites that the changed files select.
	wholeSuite bool
	// sync marks the CI of an integration sync, which has no change of its own.
	sync    bool
	created time.Time
}

// hosted reports whether this job's CI evidence comes from GitHub. A local
// change has no hosted provider, so its evidence and publication stay in the
// local lane and the job skips every GitHub step.
func (j registeredCIJob) hosted() bool { return changeKind(j.record) == "github" }

// kind names the work the job checks, so the machine view words a run that has
// no pull request number.
func (j registeredCIJob) kind() RunningCIKind {
	switch {
	case j.sync:
		return RunningCISync
	case j.hosted():
		return RunningCIPullRequest
	}
	return RunningCILocalChange
}

// nodeRuns returns the run set for this service. Every service over the same
// PR store shares the set of the process, so a read-only service reports the
// runs the engine's workers scheduled.
func (s *Service) nodeRuns() *registeredCIRuns {
	localCIInit.Lock()
	defer localCIInit.Unlock()
	if s.registeredRuns == nil {
		root := ""
		if s.Store != nil {
			root = s.Store.Root
		}
		s.registeredRuns = runningCISet(root)
	}
	return s.registeredRuns
}

func registeredJobKey(j registeredCIJob) string { return defaultRepo(j.record) + "\x00" + j.head }

// markStopped records that the run has no work left on the node.
func (run *registeredCIRun) markStopped() { run.stopOnce.Do(func() { close(run.stopped) }) }

// localCIStopTarget is one local CI run the engine has to end: its cancel
// handle, the channel that closes when its work on the machine ends, and a name
// for a log line.
type localCIStopTarget struct {
	label   string
	cancel  context.CancelFunc
	stopped <-chan struct{}
}

// closeLocalCIAdmission stops the run set from admitting new runs and copies
// the runs admitted so far. Admission closes before a snapshot waits for the
// scheduled runs' lock, which a tick holds over its own work, so a tick that is
// still finishing cannot add a run that the snapshot then misses.
func (s *Service) closeLocalCIAdmission() []localCIStopTarget {
	runs := s.nodeRuns()
	runs.closed.Store(true)
	var targets []localCIStopTarget
	runs.Lock()
	for _, run := range runs.runs {
		targets = append(targets, localCIStopTarget{label: ciRunLabel(run), cancel: run.cancel, stopped: run.stopped})
	}
	runs.Unlock()
	return targets
}

// CancelLocalCI closes admission and cancels the local CI runs this process
// started, without waiting for them. An engine calls it as soon as it stops, so
// their teardown overlaps the workers that are still finishing.
func (s *Service) CancelLocalCI() {
	for _, target := range s.closeLocalCIAdmission() {
		target.cancel()
	}
}

// StopLocalCI cancels the local CI runs this process started and waits for their
// work on the machine to end, bounded by ctx. A run that outlives its engine
// leaves a detached keeper on the machine, and that keeper holds the cache lock
// for hours, so an engine that stops ends its runs first. It returns the runs
// that were still working when ctx ended, for the caller to report.
func (s *Service) StopLocalCI(ctx context.Context) []string {
	targets := s.closeLocalCIAdmission()
	for _, target := range targets {
		target.cancel()
	}
	for i, target := range targets {
		select {
		case <-target.stopped:
		case <-ctx.Done():
			left := make([]string, 0, len(targets)-i)
			for _, working := range targets[i:] {
				left = append(left, working.label)
			}
			return left
		}
	}
	return nil
}

// ciRunLabel names one scheduled run in a log line: its head and its machine.
func ciRunLabel(run *registeredCIRun) string {
	return shortHead(run.head) + " on " + run.node
}

func shortHead(head string) string {
	if len(head) > 7 {
		return head[:7]
	}
	return head
}

// A merge trial uses the same node slots as an integration sync, and it is the
// only work these machines schedule: a pull request head is checked by GitHub's
// hosted CI, so nothing here queues one.
func registeredCIJobs(st State) []registeredCIJob {
	var jobs []registeredCIJob
	for _, b := range st.MergeTrials {
		if len(b.Changes) == 0 {
			continue
		}
		r := st.Records[b.Changes[0].Key]
		if r == nil {
			continue
		}
		local := changeKind(*r) == "local"
		if !local && changeKind(*r) != "github" {
			continue
		}
		valid := true
		for _, c := range b.Changes {
			if member := st.Records[c.Key]; !currentTrialMember(member, c) || member == nil || !local && !strings.EqualFold(member.HeadRepo, member.Repo) {
				valid = false
			}
		}
		if !valid {
			continue
		}
		// A local trial enters the queue once its branch is built, because the
		// trial asks for CI then. A hosted trial also takes its turn while the
		// branch builds: its push run starts before the branch exists.
		switch {
		case local && b.Phase == state.MergeTrialPhaseRunning:
		case !local && (b.Phase == state.MergeTrialPhaseRunning || b.Phase == state.MergeTrialPhaseBuilding || b.Phase == state.MergeTrialPhasePassed):
		default:
			continue
		}
		if b.Head != "" {
			jobs = append(jobs, registeredCIJob{record: *r, head: b.Head, branch: b.Branch, base: b.Base, trial: true, wholeSuite: true, created: b.Created})
		}
		for _, stack := range b.Stacks {
			if stack.Head != "" && stack.Phase == state.MergeTrialPhaseRunning {
				jobs = append(jobs, registeredCIJob{record: *r, head: stack.Head, branch: stack.Branch, base: b.Base, trial: true, wholeSuite: true, created: b.Created})
			}
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if !jobs[i].created.Equal(jobs[j].created) {
			return jobs[i].created.Before(jobs[j].created)
		}
		return registeredJobKey(jobs[i]) < registeredJobKey(jobs[j])
	})
	return jobs
}

// scheduleRegisteredCI owns only live cancellation handles. Nodes, modes,
// completed results and hosted cancellations live in SQLite.
func (s *Service) scheduleRegisteredCI(ctx context.Context, st State) error {
	registry := s.ciRegistry()
	nodes, err := registry.Nodes(ctx)
	if err != nil {
		return err
	}
	var enabled []localci.Node
	for _, node := range nodes {
		if node.Enabled && node.Slots > 0 {
			enabled = append(enabled, node)
		}
	}
	runs := s.nodeRuns()
	runs.Lock()
	started := false
	defer func() {
		runs.Unlock()
		if started {
			s.runningCIChanged()
		}
	}()
	desired := map[string]registeredCIJob{}
	var jobs []registeredCIJob
	modes := map[string]string{}
	for _, job := range registeredCIJobs(st) {
		if job.hosted() {
			// The room's hosted CI mode decides hosted work only: admission
			// applies the local trial hold to a local head, so a room in GitHub
			// mode does not starve the local merges that share these machines.
			mode, ok := modes[job.record.RoomID]
			if !ok {
				mode, err = registry.Mode(ctx, job.record.RoomID)
				if err != nil {
					return err
				}
				modes[job.record.RoomID] = mode
			}
			if mode != localci.ModeLocal && mode != localci.ModeBoth {
				continue
			}
			paused, err := s.RoomPaused(job.record.RoomID)
			if err != nil {
				return err
			}
			if paused {
				continue
			}
		}
		if len(enabled) == 0 {
			continue
		}
		if job.record.RepoPath == "" {
			s.resolveCheckout(ctx, st, &job.record)
		}
		if job.record.RepoPath == "" {
			continue
		}
		key := registeredJobKey(job)
		if _, exists := desired[key]; !exists {
			desired[key] = job
			jobs = append(jobs, job)
		}
	}
	active := map[string]int{}
	for key, run := range runs.runs {
		active[run.node] += max(1, run.slots)
		ready := false
		for _, node := range enabled {
			ready = ready || node.Name == run.node
		}
		_, wanted := desired[key]
		// A local head's run belongs to the trial or the sync operation that
		// asked for it, and the list of wanted hosted jobs never names it.
		if !ready || !run.local && !wanted {
			run.cancel()
		}
	}
	for _, job := range jobs {
		// A stopping engine admits no new run: the runs it has are being torn
		// down, and a keeper would outlive the process.
		if runs.closed.Load() {
			break
		}
		another, err := s.admitRegisteredCI(ctx, registry, st, job, enabled, active, runs)
		if err != nil {
			// A suite that no machine allows fails the trial that asked for it,
			// and that trial reports the failure on its own read. The tick keeps
			// taking the work the fleet can hold.
			var blocked localci.SuiteBlockedError
			if errors.As(err, &blocked) {
				log.Printf("local CI: %v", blocked)
				continue
			}
			return err
		}
		started = started || another
	}
	return nil
}

// startRegisteredCI admits one job from outside the tick. A trial head that a
// merge trial or a sync operation asks about goes through the same admission,
// budget and machine choice as a pull request head, so both get one run.
func (s *Service) startRegisteredCI(ctx context.Context, job registeredCIJob) (bool, error) {
	registry := s.ciRegistry()
	enabled, err := s.registeredCINodes(ctx)
	if err != nil || len(enabled) == 0 {
		return false, err
	}
	st, err := s.Store.Read()
	if err != nil {
		return false, err
	}
	runs := s.nodeRuns()
	runs.Lock()
	defer runs.Unlock()
	if runs.closed.Load() {
		return false, nil
	}
	started, err := s.admitRegisteredCI(ctx, registry, st, job, enabled, activeRegisteredCISlots(runs), runs)
	if started {
		s.runningCIChanged()
	}
	return started, err
}

// registeredCINodes lists the machines that can take a run now.
func (s *Service) registeredCINodes(ctx context.Context) ([]localci.Node, error) {
	nodes, err := s.ciRegistry().Nodes(ctx)
	if err != nil {
		return nil, err
	}
	var enabled []localci.Node
	for _, node := range nodes {
		if node.Enabled && node.Slots > 0 {
			enabled = append(enabled, node)
		}
	}
	return enabled, nil
}

// activeRegisteredCISlots counts the slots the runs in flight hold per machine.
func activeRegisteredCISlots(runs *registeredCIRuns) map[string]int {
	active := map[string]int{}
	for _, run := range runs.runs {
		active[run.node] += max(1, run.slots)
	}
	return active
}

// admitRegisteredCI is the one admission path for a job: it reports the saved
// result a head already has, plans the suites from the base definition, picks
// the machine with a free slot, and starts the run. It returns whether it
// started a run. Callers hold the runs lock.
func (s *Service) admitRegisteredCI(ctx context.Context, registry localCIRegistry, st State, job registeredCIJob, enabled []localci.Node, active map[string]int, runs *registeredCIRuns) (bool, error) {
	if !job.hosted() {
		// A held room or merge queue admits no local run, whichever path asks
		// for it: the trial that reads the result cannot stop a run that the
		// scheduler started on its own.
		if why, err := s.trialRoomHold(ctx, job.record); err != nil {
			return false, err
		} else if why != "" {
			return false, nil
		}
	}
	key := registeredJobKey(job)
	dir, err := s.checkoutDir(ctx, job.record)
	if err != nil {
		return false, err
	}
	job.record.RepoPath = dir
	repo, err := localci.CanonicalRepo(ctx, dir)
	if err != nil {
		return false, err
	}
	result, err := registry.LoadResult(ctx, repo, job.head)
	// A machine that could not run a check proves nothing about the head, so that
	// run moves to another machine once instead of leaving the head pending for
	// evidence that will never come.
	retriedFrom := ""
	if err == nil && s.registeredJobResultComplete(ctx, job, result) {
		switch savedResultActionFor(result) {
		case retryOnAnotherMachine:
			retriedFrom = machineFailureRetryFrom(result)
		case retryStoppedRun:
			// Dyna stopped this run, so it proves nothing about the head. It runs
			// again on the same machine, and the marker bounds that to one retry.
			result.Resumed = true
			if err := registry.SaveResult(ctx, result); err != nil {
				return false, err
			}
		default:
			// Only a hosted head has GitHub work to resume. A local result needs
			// no GitHub call: its evidence stays in the local CI records.
			if job.hosted() {
				applyLocalBudgets(&result, EffectivePolicy(st, job.record))
				if localInfrastructureError(result) {
					if err := s.resumeHostedCI(ctx, job, repo); err != nil {
						return false, err
					}
				}
			}
			return false, nil
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if runs.runs[key] != nil {
		return false, nil
	}
	// A machine with a free slot must exist before the plan does any work, so a
	// fleet that is only busy keeps the job waiting without a fetch.
	chosen := availableCINodeExcept(enabled, active, job.record.RoomID, retriedFrom)
	if chosen == nil {
		return false, nil
	}
	plan, err := s.prepareRegisteredCI(ctx, job)
	if err != nil {
		result := localci.Result{Repo: repo, SHA: job.head, RoomID: job.record.RoomID, Node: chosen.Name, Started: s.now(), Status: localci.OutcomeError, Error: Sanitize(err.Error())}
		if saveErr := registry.SaveResult(ctx, result); saveErr != nil {
			log.Printf("save local CI preparation failure: %v", saveErr)
		}
		if job.hosted() {
			if resumeErr := s.resumeHostedCI(ctx, job, repo); resumeErr != nil {
				log.Printf("resume hosted CI after preparation failure: %v", resumeErr)
			}
		}
		return false, nil
	}
	if !plan.runnable() {
		return false, nil
	}
	// A local change has no hosted provider, so only a hosted job can already
	// hold conclusive evidence for its head.
	if job.hosted() {
		settled, err := s.scheduledCIConclusive(ctx, job, repo, plan.execution)
		if err != nil {
			return false, err
		}
		if settled {
			return false, nil
		}
	}
	// Only a machine that allows the run's room and every suite of it takes the
	// run. A suite that no machine of that room allows never arrives, so the
	// caller gets a typed error that names it; a fleet that is only busy or low
	// on disk keeps waiting.
	chosen = availableCINodeExcept(localci.NodesForSuites(enabled, plan.suites), active, job.record.RoomID, retriedFrom)
	if chosen == nil {
		return false, localci.BlockedSuite(localci.NodesForRoom(enabled, job.record.RoomID), plan.suites)
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	slots := min(registeredCIRunSlots, chosen.Slots-active[chosen.Name])
	run := &registeredCIRun{
		node: chosen.Name, slots: slots, cancel: cancel, stopped: make(chan struct{}),
		repo: job.record.Repo, number: job.record.Number, kind: job.kind(), branch: job.branch,
		trial: job.trial, local: !job.hosted(),
		head: job.head, title: job.record.Title, url: job.record.URL, started: s.now(),
	}
	runs.runs[key] = run
	active[chosen.Name] += slots
	go s.executeRegisteredCI(runCtx, job, plan, repo, *chosen, key, run, retriedFrom)
	return true, nil
}

// savedResultAction says what admission does with a saved result that already
// covers a head.
type savedResultAction int

const (
	// settleSavedResult keeps the result: it tested the commit, or Dyna has no
	// reason to run the head again.
	settleSavedResult savedResultAction = iota
	// retryOnAnotherMachine moves a machine-level failure to another machine.
	retryOnAnotherMachine
	// retryStoppedRun runs a head again because Dyna itself stopped its run. That
	// says nothing about the commit, so the same machine may take it.
	retryStoppedRun
)

// savedResultActionFor picks what to do with a complete saved result of a head
// whose local run is the only provider, such as a merge trial.
func savedResultActionFor(result localci.Result) savedResultAction {
	if machineFailureRetryFrom(result) != "" {
		return retryOnAnotherMachine
	}
	if stoppedLocalResult(result) && !result.Resumed {
		return retryStoppedRun
	}
	return settleSavedResult
}

// stoppedLocalResult reports a local result that Dyna itself stopped: an engine
// stop or a cancelled run. Every check in such a result may carry the cause, so
// one is enough.
func stoppedLocalResult(result localci.Result) bool {
	if !localInfrastructureError(result) {
		return false
	}
	if stoppedCause(result.Cause) {
		return true
	}
	for _, check := range result.Checks {
		// A group's row is derived from its members, so it has no cause of its own.
		if !check.Aggregate && stoppedCause(check.Cause) {
			return true
		}
	}
	return false
}

// stoppedCause reports a cause that names Dyna stopping the run, not the commit
// and not the machine.
func stoppedCause(cause localci.FailureCause) bool {
	return cause == localci.CauseEngineStop || cause == localci.CauseCancelled
}

// machineFailureRetryFrom names the machine whose failure made the result, when
// the result is a machine-level infrastructure failure that has not been retried
// yet. An empty string means the result stands: it either tested the commit or
// it is already the retry of another machine's failure.
func machineFailureRetryFrom(result localci.Result) string {
	if result.Node == "" || result.RetriedFrom != "" || !localInfrastructureError(result) {
		return ""
	}
	if result.Cause != "" && !machineCause(result.Cause) {
		return ""
	}
	failed := false
	for _, check := range result.Checks {
		// A group's row is derived from its members, so it has no cause of its
		// own. Only a member says why the group failed.
		if check.Aggregate {
			continue
		}
		if check.Status == localci.OutcomePassed {
			continue
		}
		if !machineCause(check.Cause) {
			return ""
		}
		failed = true
	}
	if !failed && !machineCause(result.Cause) {
		return ""
	}
	return result.Node
}

// machineCause reports a failure the machine caused, which says nothing about
// the commit. An unclassified failure is one where the check proved no test
// result at all, such as a scope that the node's systemd refused or a container
// that never ran; only a check that proves a test failure is evidence about the
// commit.
func machineCause(cause localci.FailureCause) bool {
	switch cause {
	case localci.CauseContainerFailure, localci.CauseSSHDrop, localci.CauseDiskFull, localci.CauseUnclassified:
		return true
	}
	return false
}

// savedLocalResultKey identifies one saved local result, so the watcher can tell
// a result the PR has already read from one a manual rerun just wrote.
func savedLocalResultKey(result localci.Result) string {
	body, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	return digest(string(body))
}

// RefreshSavedLocalResult marks every tracked change whose stored local result
// the watcher has not read yet, so its next tick takes a full poll and the new
// evidence decides. `dyna git ci run` is the one writer of a pull request
// head's local result, because no local CI runs for that head any more, and a
// quiet pull request would otherwise keep its old verdict.
func (s *Service) RefreshSavedLocalResult(result localci.Result) error {
	key := savedLocalResultKey(result)
	if key == "" || result.SHA == "" || s.Store == nil {
		return nil
	}
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	var keys []string
	for recordKey, r := range st.Records {
		if r.Head == result.SHA && r.Polling.LocalCI != key {
			keys = append(keys, recordKey)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	return s.Store.updateRecords(keys, func(st *State) error {
		for _, recordKey := range keys {
			r := st.Records[recordKey]
			if r == nil || r.Head != result.SHA || r.Polling.LocalCI == key {
				continue
			}
			r.Polling.LocalCI = key
			r.Polling.Next = time.Time{}
			r.Polling.FullChecked = time.Time{}
		}
		return nil
	})
}

// Bound each run so queued heads can share a node. Slot locks in the runner
// still enforce the node-wide limit across processes. Six is the measured best
// on a 16-slot node: three heads took 121 s and gave 14.8 runs per 10 minutes.
const registeredCIRunSlots = 6

// Prefer the node with the most unreserved check slots. A machine that is low
// on disk takes no new run, so its work waits for a node that has space. A
// machine that names rooms takes only those rooms' work, and one that names
// suites takes only runs whose every suite it allows, so callers pass the
// machines that allow the run's suites.
func availableCINode(nodes []localci.Node, active map[string]int, roomID string) *localci.Node {
	return availableCINodeExcept(nodes, active, roomID, "")
}

// availableCINodeExcept is availableCINode without the machine the run just
// failed on, so a retry lands somewhere else.
func availableCINodeExcept(nodes []localci.Node, active map[string]int, roomID, skip string) *localci.Node {
	var chosen *localci.Node
	free := 0
	for i := range nodes {
		node := &nodes[i]
		if node.LowDisk || !node.AllowsRoom(roomID) || node.Name == skip {
			continue
		}
		if available := node.Slots - active[node.Name]; node.Enabled && available > free {
			chosen, free = node, available
		}
	}
	return chosen
}

func (s *Service) executeRegisteredCI(ctx context.Context, job registeredCIJob, plan localCIPlan, repo string, node localci.Node, key string, run *registeredCIRun, retriedFrom string) {
	// A run that ends on any path has no work left on the node.
	defer run.markStopped()
	defer func() {
		run.cancel()
		runs := s.nodeRuns()
		runs.Lock()
		ended := runs.runs[key] == run
		if ended {
			delete(runs.runs, key)
		}
		runs.Unlock()
		if ended {
			s.runningCIChanged()
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, 90*time.Minute)
	defer cancel()
	policyState, err := s.Store.Read()
	if err != nil {
		log.Printf("local CI: %v", err)
		return
	}
	policy := EffectivePolicy(policyState, job.record)
	maxTest, allowed, err := policy.TestBudget()
	if err != nil {
		log.Printf("local CI: %v", err)
		return
	}
	if !policy.EnforceCITimeBudget() {
		maxTest = time.Duration(1<<63 - 1)
	}
	suites := plan.suites
	if len(suites) > 0 {
		s.setRunningCISuites(key, run, suites)
	}
	result := localci.Result{Repo: repo, SHA: job.head, RoomID: job.record.RoomID, Node: node.Name, Started: s.now(), Status: "error"}
	if err == nil && len(suites) > 0 {
		req := localci.Request{Definition: plan.execution, Repo: job.record.RepoPath, SHA: job.head, RoomID: job.record.RoomID, Suites: suites, MaxTest: maxTest, Allowed: allowed, DeferSave: true, Parallelism: run.slots}
		req.OnStart = func(startCtx context.Context) error {
			// Hosted work is only replaced for a hosted head. A local change has
			// no queued GitHub run to cancel.
			if job.hosted() {
				if err := s.cancelQueuedHostedCI(startCtx, job, repo); err != nil {
					log.Printf("cancel queued hosted CI: %v", err)
				}
			}
			return startCtx.Err()
		}
		if s.RunNodeCI != nil {
			result, err = s.RunNodeCI(ctx, node, req)
		} else {
			runner := localci.Runner{DataDir: filepath.Dir(s.Store.Root), Node: &node}
			runner.OnCacheRepair = s.CacheRepairReporter(job.record.RoomID)
			result, err = runner.Run(ctx, req)
		}
		// The node work is over: its teardown has stopped the keeper and released
		// the machine's locks.
		run.markStopped()
	} else if err == nil {
		result.Status = "passed"
		result.Checks = []localci.SuiteResult{{Suite: localci.SuiteChanges, CheckName: localci.CheckName(localci.SuiteChanges), Node: node.Name, Status: localci.OutcomePassed}}
	}
	if ctx.Err() != nil {
		// Nothing is left on the node, so a stopping engine does not wait for the
		// recovery that follows.
		run.markStopped()
		if err := s.recoverInterruptedCI(ctx, job, repo, node, result); err != nil {
			log.Printf("restore interrupted local CI: %v", err)
		}
		return
	}
	result.Repo, result.SHA, result.RoomID, result.Node = repo, job.head, job.record.RoomID, node.Name
	// The result says which machine's failure caused this attempt, so a machine
	// failure on the retry ends the head instead of walking the fleet.
	result.RetriedFrom = retriedFrom
	if err != nil {
		result.Status = "error"
		result.Error = Sanitize(err.Error())
	}
	applyLocalBudgets(&result, policy)
	registry := s.ciRegistry()
	if err := registry.SaveResult(ctx, result); err != nil {
		log.Printf("save local CI: %v", err)
		return
	}
	if localInfrastructureError(result) {
		if job.hosted() {
			if err := s.resumeHostedCI(ctx, job, repo); err != nil {
				log.Printf("resume hosted CI: %v", err)
			}
		}
	}
	// A completed trial or sync must force a full snapshot even if GitHub did
	// not change, so its reader sees the new evidence.
	if err := s.forceFullPoll(job.record.Key, job.head); err != nil {
		log.Printf("refresh local CI: %v", err)
	}
}

// forceFullPoll makes the next tick take a full poll of the change at this head.
func (s *Service) forceFullPoll(recordKey, head string) error {
	return s.Store.updateRecords([]string{recordKey}, func(st *State) error {
		if r := st.Records[recordKey]; r != nil && r.Head == head {
			r.Polling.Next = time.Time{}
			r.Polling.FullChecked = time.Time{}
		}
		return nil
	})
}

func (s *Service) prepareRegisteredCI(ctx context.Context, job registeredCIJob) (localCIPlan, error) {
	r := job.record
	if !job.hosted() {
		// A local head is already in this checkout: a merge trial and a sync
		// operation write their own refs, so there is nothing to fetch.
		return s.planRegisteredCI(ctx, job)
	}
	args := []string{"fetch", "--quiet", "--no-tags", "--", "https://" + r.Host + "/" + r.Repo + ".git", job.head}
	base := job.base
	if base == "" {
		base = r.Base
	}
	if headSHAPattern.MatchString(base) {
		args = append(args, base)
	}
	cmd := gitexec.Command(ctx, r.RepoPath, args...)
	g := s.github(r.RoomID)
	ctx = ghclient.WithRepository(ghclient.WithRoom(ghclient.WithRooms(ctx, g.Rooms), r.RoomID), r.Host, r.Repo)
	var err error
	if g.run != nil {
		err = g.run(ctx, cmd)
	} else {
		err = ghclient.RunGit(ctx, g.Executable, cmd)
	}
	if err != nil {
		return localCIPlan{}, fmt.Errorf("fetch local CI head and base: %w", err)
	}
	return s.planRegisteredCI(ctx, job)
}

// Cancellation of a still-current head restores the hosted work we removed.
// Superseded heads stay cancelled. A timeout also saves an infrastructure error.
func (s *Service) recoverInterruptedCI(ctx context.Context, job registeredCIJob, repo string, node localci.Node, result localci.Result) error {
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	current := false
	for _, candidate := range registeredCIJobs(st) {
		if registeredJobKey(candidate) == registeredJobKey(job) {
			job = candidate
			current = true
			break
		}
	}
	if !job.hosted() {
		// A local head belongs to the trial or the sync operation that asked
		// for it, so its timeout result stays readable for that reader.
		current = true
	}
	if !current {
		return nil
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.recordBudget())
	defer cancel()
	var saveErr error
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		result.Repo, result.SHA, result.RoomID, result.Node = repo, job.head, job.record.RoomID, node.Name
		result.Status, result.Error = "error", "local CI exceeded its overall time limit"
		saveErr = s.ciRegistry().SaveResult(recoveryCtx, result)
	case ctx.Err() != nil && localci.EngineStopping():
		// Dyna's own stop cancelled this run, so it tested nothing. The saved
		// cause says so, and the scheduler and a trial read it as unfinished work
		// and run the same commit again. Any other cancellation belongs to its
		// caller and stays unsaved.
		result.Repo, result.SHA, result.RoomID, result.Node = repo, job.head, job.record.RoomID, node.Name
		result.Status, result.Cause = localci.OutcomeError, localci.CauseEngineStop
		result.Error = "local CI stopped because the engine is restarting"
		saveErr = s.ciRegistry().SaveResult(recoveryCtx, result)
	}
	if !job.hosted() {
		return saveErr
	}
	return errors.Join(saveErr, s.resumeHostedCI(recoveryCtx, job, repo))
}

// Saved CI belongs to a PR head only after that head received a full poll.
// Trial heads have separate push runs and must use their own exact-head facts.
func (s *Service) scheduledCIConclusive(ctx context.Context, job registeredCIJob, repo string, definition *localci.Definition) (bool, error) {
	r := job.record
	if !job.trial {
		return headVerified(r) && job.head == r.Head && r.Polling.PullHead == r.Head && verdictFromHosted(r.CI).Conclusive(), nil
	}
	var snap Snapshot
	if err := s.github(r.RoomID).readCommitCI(ctx, refRecord(r), job.head, &snap); err != nil {
		return false, err
	}
	var matching []Run
	for _, run := range snap.Runs {
		if hostedRunMatches(job, run) {
			matching = append(matching, run)
		}
	}
	if len(matching) == 0 {
		return false, nil
	}
	snap.Runs = matching
	saved, err := s.ciRegistry().LoadHostedRuns(ctx, repo, job.head)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	snap = withoutLocalHostedCancellations(snap, saved, definition)
	snap.Pull.Head.SHA = job.head
	// A workflow that only cancellations ended proves nothing about the head, so
	// the scheduler must still run local CI instead of treating the head as
	// settled. Without this, a cancellation while the slots were busy would skip
	// local execution and the trial would wait for CI that never comes.
	if hostedCancelledOnly(snap, nil) {
		return false, nil
	}
	ci, _, _, _ := s.registeredHostedStatus(snap, Record{}, ChangePolicy{})
	return ci.Conclusive(), nil
}
