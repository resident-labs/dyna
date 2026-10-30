package watch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/gitexec"
	"dyna-agent/internal/jobs"
	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
	cisched "dyna-agent/internal/pr/watch/ci"
)

type localCIRegistry = cisched.Registry

// orphanedCIRuns is the registry seam for runs a stopped engine left behind.
type orphanedCIRuns interface {
	OrphanedAttachments(context.Context) ([]localci.OrphanedAttachment, error)
	AbandonOrphanedRun(context.Context, localci.OrphanedAttachment) error
}

func (s *Service) ciRegistry() localCIRegistry {
	if s.LocalCIRegistry != nil {
		return s.LocalCIRegistry
	}
	return localci.Store{DataDir: filepath.Dir(s.Store.Root)}
}

type registeredCIRuns = cisched.Runs
type registeredCIRun = cisched.Run

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

type localCIStopTarget = cisched.StopTarget

// closeLocalCIAdmission stops the run set from admitting new runs and copies
// the runs admitted so far. Admission closes before a snapshot waits for the
// scheduled runs' lock, which a tick holds over its own work, so a tick that is
// still finishing cannot add a run that the snapshot then misses.
func (s *Service) closeLocalCIAdmission() []localCIStopTarget {
	return s.nodeRuns().CloseAdmission()
}

// CancelLocalCI closes admission and cancels the local CI runs this process
// started, without waiting for them. An engine calls it as soon as it stops, so
// their teardown overlaps the workers that are still finishing.
func (s *Service) CancelLocalCI() {
	if s.registeredWarms != nil {
		s.registeredWarms.cancelAll()
	}
	for _, target := range s.closeLocalCIAdmission() {
		target.Cancel()
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
		target.Cancel()
	}
	for i, target := range targets {
		select {
		case <-target.Stopped:
		case <-ctx.Done():
			left := make([]string, 0, len(targets)-i)
			for _, working := range targets[i:] {
				left = append(left, working.Label)
			}
			return left
		}
	}
	return nil
}

// A merge trial uses the same node slots as an integration sync. A pull request
// head is checked by GitHub's hosted CI, except in a local-only room, where
// localOnlyPRHeadJobs queues it.
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

// localOnlyPRHeadJobs returns a job for the current head of each open, watched
// pull request in a room whose CI mode is local. Such a room has no hosted CI to
// test the head, and the merge queue waits for a head result, so the scheduler
// runs it as it runs a trial. Hosted and both modes keep GitHub's run as it was.
func (s *Service) localOnlyPRHeadJobs(ctx context.Context, st State) ([]registeredCIJob, error) {
	var jobs []registeredCIJob
	modes := map[string]string{}
	for _, r := range st.Records {
		if r == nil || changeKind(*r) != "github" || !r.Watching || r.State != state.PROpen || r.Head == "" || !headVerified(*r) ||
			!strings.EqualFold(r.HeadRepo, r.Repo) || strings.HasPrefix(r.Branch, "dyna/merge-trial/") {
			continue
		}
		mode, ok := modes[r.RoomID]
		if !ok {
			var err error
			if mode, err = s.ciRegistry().Mode(ctx, r.RoomID); err != nil {
				return nil, err
			}
			modes[r.RoomID] = mode
		}
		if mode == localci.ModeLocal {
			jobs = append(jobs, registeredCIJob{record: *r, head: r.Head, branch: r.Branch, base: r.Base})
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return registeredJobKey(jobs[i]) < registeredJobKey(jobs[j]) })
	return jobs, nil
}

// tickJobs decides which registered CI jobs this tick wants and admits. The
// caller gathers every input first; this function does no IO and keeps trial
// jobs ahead of local-only pull request heads.
func tickJobs(wanted, headJobs []registeredCIJob, modes map[string]string, paused map[string]bool, haveNodes bool) ([]registeredCIJob, map[string]struct{}) {
	desired := map[string]struct{}{}
	var admitJobs []registeredCIJob
	for _, job := range append(wanted, headJobs...) {
		if job.hosted() {
			// The room's hosted CI mode decides hosted work only: admission
			// applies the local trial hold to a local head, so a room in GitHub
			// mode does not starve the local merges that share these machines.
			mode := modes[job.record.RoomID]
			if mode != localci.ModeLocal && mode != localci.ModeBoth || paused[job.record.RoomID] {
				continue
			}
		}
		if !haveNodes {
			continue
		}
		key := registeredJobKey(job)
		if _, exists := desired[key]; !exists {
			desired[key] = struct{}{}
			admitJobs = append(admitJobs, job)
		}
	}
	return admitJobs, desired
}

// scheduleRegisteredCI owns only live cancellation handles. Nodes, modes,
// completed results and hosted cancellations live in SQLite.
func (s *Service) scheduleRegisteredCI(ctx context.Context, st State) error {
	registry := s.ciRegistry()
	enabled, err := s.registeredCINodes(ctx)
	if err != nil {
		return err
	}
	runs := s.nodeRuns()
	runs.Lock()
	started := false
	var orphanRegistry orphanedCIRuns
	var abandon []localci.OrphanedAttachment
	defer func() {
		runs.Unlock()
		for _, orphan := range abandon {
			if runs.Closed() {
				break
			}
			go func(orphan localci.OrphanedAttachment) {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.recordBudget())
				defer cancel()
				if err := orphanRegistry.AbandonOrphanedRun(ctx, orphan); err != nil {
					log.Printf("local CI: abandon orphaned run %s on %s: %v", orphan.RunID, orphan.Node, err)
				}
			}(orphan)
		}
		if started {
			s.runningCIChanged()
		}
	}()
	modes := map[string]string{}
	paused := map[string]bool{}
	wanted := registeredCIJobs(st)
	headJobs, err := s.localOnlyPRHeadJobs(ctx, st)
	if err != nil {
		return err
	}
	for _, job := range append(wanted, headJobs...) {
		if !job.hosted() {
			continue
		}
		roomID := job.record.RoomID
		mode, ok := modes[roomID]
		if !ok {
			mode, err = registry.Mode(ctx, roomID)
			if err != nil {
				return err
			}
			modes[roomID] = mode
		}
		if mode != localci.ModeLocal && mode != localci.ModeBoth {
			continue
		}
		if _, err := s.roomPausedCached(roomID, paused); err != nil {
			return err
		}
	}
	admitJobs, desired := tickJobs(wanted, headJobs, modes, paused, len(enabled) > 0)
	resolved := admitJobs[:0]
	for _, job := range admitJobs {
		if job.record.RepoPath == "" {
			s.resolveCheckout(ctx, st, &job.record)
		}
		if job.record.RepoPath == "" {
			delete(desired, registeredJobKey(job))
			continue
		}
		resolved = append(resolved, job)
	}
	admitJobs = resolved
	runs.CancelUnwanted(desired, enabled)
	if registry, ok := registry.(orphanedCIRuns); ok {
		orphans, err := registry.OrphanedAttachments(ctx)
		if err != nil {
			return err
		}
		orphanRegistry = registry
		for _, orphan := range orphans {
			wanted := false
			switch {
			case orphan.Source.Kind == jobs.CheckKindPullRequest,
				orphan.Source.Kind == jobs.CheckKindLocalChange && orphan.Source.Trial:
				for _, job := range admitJobs {
					wanted = wanted || job.head == orphan.SHA
				}
			case orphan.Source.Kind == jobs.CheckKindSync:
				if s.SyncRunActive == nil {
					continue
				}
				active, err := s.SyncRunActive(orphan.Repo, orphan.SHA)
				if err != nil {
					return err
				}
				wanted = active
			default:
				// Standalone commands and legacy rows may still have a caller that
				// resumes them. Their keepers retain the existing three-hour bound.
				continue
			}
			if !wanted {
				abandon = append(abandon, orphan)
			}
		}
	}
	for _, job := range admitJobs {
		// A stopping engine admits no new run: the runs it has are being torn
		// down, and a keeper would outlive the process.
		if runs.Closed() {
			break
		}
		another, err := s.admitRegisteredCI(ctx, registry, st, job, enabled, runs)
		if err != nil {
			// A suite that no machine allows fails the trial that asked for it,
			// and that trial reports the failure on its own read. The tick keeps
			// taking the work the fleet can hold.
			if localci.NodeBlockedError(err) != nil {
				log.Printf("local CI: %v", err)
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
	if runs.Closed() {
		return false, nil
	}
	started, err := s.admitRegisteredCI(ctx, registry, st, job, enabled, runs)
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
		if node.TakesCI() {
			enabled = append(enabled, node)
		}
	}
	return enabled, nil
}

// roomServedByCI reports whether an enabled machine takes CI work for the room.
// A machine list that cannot be read counts as serving, so a store error never
// fails a head.
func (s *Service) roomServedByCI(roomID string) bool {
	if s.LocalCIRegistry == nil && s.Store == nil {
		return true
	}
	nodes, err := s.registeredCINodes(context.Background())
	return err != nil || len(localci.NodesForRoom(nodes, roomID)) > 0
}

// admitRegisteredCI is the one admission path for a job: it reports the saved
// result a head already has, plans the suites from the base definition, picks
// the machine with a free slot, and starts the run. It returns whether it
// started a run. Callers hold the runs lock.
func (s *Service) admitRegisteredCI(ctx context.Context, registry localCIRegistry, st State, job registeredCIJob, enabled []localci.Node, runs *registeredCIRuns) (bool, error) {
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
	retriedFrom, resumed := "", false
	if err == nil && s.registeredJobResultComplete(ctx, job, result) {
		switch cisched.SavedResultActionFor(result) {
		case cisched.RetryOnAnotherMachine:
			retriedFrom = cisched.MachineFailureRetryFrom(result)
		case cisched.RetryStoppedRun:
			// Dyna stopped this run, so it proves nothing about the head. The
			// replacement on the same machine carries Result.Resumed, so the old
			// failure stays untouched and a retry that never starts is not spent.
			resumed = true
		default:
			// Only a hosted head has GitHub work to resume. A local result needs
			// no GitHub call: its evidence stays in the local CI records.
			if job.hosted() {
				applyLocalBudgets(&result, EffectivePolicy(st, job.record))
				if cisched.LocalInfrastructureError(result) {
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
	if runs.Has(key) {
		return false, nil
	}
	// A machine with room in the shared node queue must exist before the plan
	// does any work, so a fleet that is only busy keeps a pull request head
	// waiting without a fetch. A trial or a sync instead joins the node queue:
	// only its own reader asks for it, so waiting for a machine nothing else
	// holds would starve the merge queue behind it.
	nodeFloor := 0
	if job.trial || job.sync {
		nodeFloor = -1
	}
	chosen, retryResumed := s.chooseCINode(ctx, registry, repo, job.head, enabled, job.record.RoomID, retriedFrom, nodeFloor)
	resumed = resumed || retryResumed
	if chosen == nil {
		return false, nil
	}
	plan, err := s.prepareRegisteredCI(ctx, job)
	if err != nil {
		// The run never started, so the result says nothing about the commit. The
		// cause keeps a trial from reporting it as a failed test, and the marker
		// says whether this was already the retry: a second failure of the same
		// kind is spent, so admission stops instead of retrying for ever.
		result := localci.Result{Repo: repo, SHA: job.head, RoomID: job.record.RoomID, Node: chosen.Name, Started: s.now(), Status: localci.OutcomeError, Cause: localci.CauseScheduling, Error: Sanitize(err.Error()), Resumed: resumed}
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
		var required []string
		if job.trial && plan.required != nil {
			required = trialRequiredChecks(EffectivePolicy(st, job.record), plan)
		}
		settled, err := s.scheduledCIConclusive(ctx, job, repo, plan.execution, required)
		if err != nil {
			return false, err
		}
		if settled {
			return false, nil
		}
	}
	// Only a machine that allows the run's room and suites and carries every
	// label of its checks takes the run. A requirement no machine of that room
	// satisfies fails at once; a fleet that is only busy keeps waiting.
	labels := plan.execution.LabelsForSuites(plan.suites)
	suited := localci.NodesForSuites(enabled, plan.suites)
	eligible := localci.NodesForLabels(suited, labels)
	chosen, retryResumed = s.chooseCINode(ctx, registry, repo, job.head, eligible, job.record.RoomID, retriedFrom, nodeFloor)
	// An attached run must return to its original node even when that node is
	// full. Otherwise prefer a node that can admit the first check now.
	if cisched.PinnedCINode(ctx, registry, repo, job.head, eligible, job.record.RoomID, retriedFrom) == nil {
		ready := s.readyCINodes(ctx, eligible, job, plan)
		if preferred, preferredResumed := s.chooseCINode(ctx, registry, repo, job.head, ready, job.record.RoomID, retriedFrom, -1); preferred != nil {
			chosen, retryResumed = preferred, preferredResumed
		}
	}
	resumed = resumed || retryResumed
	if chosen == nil {
		roomNodes := localci.NodesForRoom(enabled, job.record.RoomID)
		if err := localci.BlockedSuite(roomNodes, plan.suites); err != nil {
			return false, err
		}
		return false, localci.BlockedLabels(localci.NodesForSuites(roomNodes, plan.suites), labels)
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	run := &registeredCIRun{
		Node: chosen.Name, Cancel: cancel, Stopped: make(chan struct{}), Ended: make(chan struct{}),
		Repo: job.record.Repo, Number: job.record.Number, Kind: job.kind(), Branch: job.branch,
		Trial: job.trial, Local: !job.hosted(),
		Head: job.head, Title: job.record.Title, URL: job.record.URL, Started: s.now(),
	}
	if !runs.Add(key, run) {
		cancel()
		return false, nil
	}
	go s.executeRegisteredCI(runCtx, job, plan, repo, *chosen, key, run, retriedFrom, resumed)
	return true, nil
}

// readyCINodes predicts admission with each node's clamped first-check demand.
func (s *Service) readyCINodes(ctx context.Context, nodes []localci.Node, job registeredCIJob, plan localCIPlan) []localci.Node {
	var ready []localci.Node
	queue := s.ciAdmission()
	for _, node := range nodes {
		if !node.AllowsRoom(job.record.RoomID) {
			continue
		}
		demand, err := localci.FirstCheckDemand(plan.execution, plan.suites, node, job.record.RoomID, job.trial || job.sync)
		if err != nil {
			continue
		}
		if fits, err := queue.CanAdmit(ctx, demand); err == nil && fits {
			ready = append(ready, node)
		}
	}
	return ready
}

// chooseCINode picks the machine for a run from nodes: the head's pinned node
// when it has one, else a free one. A floor below zero also takes a node whose
// queue is full, so a merge trial joins the node queue instead of waiting for
// a machine nothing else holds. When the head retries from a failed machine
// and no other node can take the work, that machine runs it once more, and
// the returned resumed mark keeps that to one retry.
func (s *Service) chooseCINode(ctx context.Context, registry localCIRegistry, repo, head string, nodes []localci.Node, roomID, retriedFrom string, floor int) (chosen *localci.Node, resumed bool) {
	chosen = s.availableCINode(ctx, nodes, roomID, retriedFrom, floor)
	if pinned := cisched.PinnedCINode(ctx, registry, repo, head, nodes, roomID, retriedFrom); pinned != nil {
		chosen = pinned
	}
	if chosen == nil && retriedFrom != "" {
		// The machine that failed is the only one that can take this work, so it
		// runs there once more instead of the head waiting with nothing running.
		// The replacement carries Result.Resumed, which keeps that to one retry.
		chosen = s.availableCINode(ctx, nodes, roomID, "", floor)
		if chosen != nil {
			resumed = true
		}
	}
	return chosen, resumed
}

// RefreshSavedLocalResult marks every tracked change whose stored local result
// the watcher has not read yet, so its next tick takes a full poll and the new
// evidence decides. `dyna git ci run` is the one writer of a pull request
// head's local result, because no local CI runs for that head any more, and a
// quiet pull request would otherwise keep its old verdict.
func (s *Service) RefreshSavedLocalResult(result localci.Result) error {
	key := cisched.SavedLocalResultKey(result)
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

// registeredCIRunParallelism bounds how many checks one run starts at once,
// so queued heads share a node. The shared node queue still enforces the
// node-wide limits across every run and process. Six is the measured best on
// a 16-CPU node: three heads took 121 s and gave 14.8 runs per 10 minutes.
const registeredCIRunParallelism = 6

// ciAdmission reads the shared node queue of this engine's data folder. A
// service built without a store, as focused tests are, reads no queue: every
// node then reads as unprobed, which the queue counts as room for one of
// everything.
func (s *Service) ciAdmission() jobs.Admission {
	if s.Store == nil || s.Store.Root == "" {
		return jobs.Admission{Engine: true}
	}
	return jobs.Admission{DataDir: filepath.Dir(s.Store.Root), Engine: true}
}

// availableCINode reads each node's room from the shared node queue, against
// the capacity of the node view the scheduler already holds.
func (s *Service) availableCINode(ctx context.Context, nodes []localci.Node, roomID, skip string, floor int) *localci.Node {
	queue := s.ciAdmission()
	return cisched.AvailableCINode(nodes, roomID, skip, floor, func(node localci.Node) int { return queue.FreeCPUs(ctx, node.Name, node.CPUs) })
}

// freeCINodeCPUs reads the CPUs a node keeps free after the work its queue
// holds, from the shared node queue.
func (s *Service) freeCINodeCPUs(ctx context.Context, node localci.Node) int {
	return s.ciAdmission().FreeCPUs(ctx, node.Name, node.CPUs)
}

func (s *Service) executeRegisteredCI(ctx context.Context, job registeredCIJob, plan localCIPlan, repo string, node localci.Node, key string, run *registeredCIRun, retriedFrom string, resumed bool) {
	// The last deferred call, so every write below lands before ended closes.
	defer close(run.Ended)
	// A run that ends on any path has no work left on the node.
	defer run.MarkStopped()
	defer func() {
		run.Cancel()
		runs := s.nodeRuns()
		ended := runs.Finish(key, run)
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
		req := localci.Request{Definition: plan.execution, Repo: job.record.RepoPath, SHA: job.head, Base: job.record.Base, RoomID: job.record.RoomID, Suites: suites, MaxTest: maxTest, Allowed: allowed, DeferSave: true, Parallelism: registeredCIRunParallelism, Ahead: job.trial || job.sync, Source: jobs.CheckSource{Kind: cisched.CheckKind(job.kind()), Number: job.record.Number, Trial: job.trial, Branch: job.branch}}
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
		for {
			req.OnQueue = func(waitCtx context.Context) *localci.Node {
				nodes, listErr := s.registeredCINodes(waitCtx)
				if listErr != nil {
					return nil
				}
				nodes = localci.NodesForLabels(localci.NodesForSuites(nodes, suites), plan.execution.LabelsForSuites(suites))
				ready := s.readyCINodes(waitCtx, nodes, job, plan)
				if retriedFrom != "" {
					ready = slices.DeleteFunc(ready, func(candidate localci.Node) bool { return candidate.Name == retriedFrom })
				}
				return s.availableCINode(waitCtx, ready, job.record.RoomID, node.Name, -1)
			}
			if s.RunNodeCI != nil {
				result, err = s.RunNodeCI(ctx, node, req)
			} else {
				runner := localci.Runner{DataDir: filepath.Dir(s.Store.Root), Node: &node, Engine: true}
				runner.OnCacheRepair = s.CacheRepairReporter(job.record.RoomID)
				result, err = runner.Run(ctx, req)
			}
			var move *localci.RelocateError
			if !errors.As(err, &move) || ctx.Err() != nil {
				break
			}
			node = move.Node
			runs := s.nodeRuns()
			runs.Lock()
			run.Node = node.Name
			runs.Unlock()
			s.runningCIChanged()
		}
		// The node work is over: its teardown has stopped the keeper and released
		// the machine's locks.
		run.MarkStopped()
	} else if err == nil {
		result.Status = "passed"
		result.Checks = []localci.SuiteResult{{Suite: localci.SuiteChanges, CheckName: localci.CheckName(localci.SuiteChanges), Node: node.Name, Status: localci.OutcomePassed}}
	}
	// A resumed run replaces one Dyna stopped, and says so in its own result, so a
	// second interruption is bounded where the first was retried. The runner does
	// not know this, so the marker is set here, before any save or recovery.
	result.Resumed = resumed
	if ctx.Err() != nil {
		// Nothing is left on the node, so a stopping engine does not wait for the
		// recovery that follows.
		run.MarkStopped()
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
	if cisched.LocalInfrastructureError(result) {
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
	g := s.github(r.RoomID)
	ctx = ghclient.WithRepository(ghclient.WithRoom(ghclient.WithRooms(ctx, g.Rooms), r.RoomID), r.Host, r.Repo)
	// A trial branch is pushed just before this fetch, so the first attempt can
	// arrive before GitHub publishes the ref. That failure is a wait, not a
	// scheduling failure: the same head is fetched again, bounded.
	for attempt := 0; ; attempt++ {
		cmd := gitexec.Command(ctx, r.RepoPath, args...)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		var err error
		if g.run != nil {
			err = g.run(ctx, cmd)
		} else {
			err = ghclient.RunGit(ctx, g.Executable, cmd)
		}
		if err == nil {
			return s.planRegisteredCI(ctx, job)
		}
		detail := strings.TrimSpace(Sanitize(stderr.String()))
		if !cisched.MissingRemoteRef(detail) || attempt == ciRefFetchAttempts-1 {
			if detail != "" {
				return localCIPlan{}, fmt.Errorf("fetch local CI head and base: %w: %s", err, clip(detail, 500))
			}
			return localCIPlan{}, fmt.Errorf("fetch local CI head and base: %w", err)
		}
		if waitErr := s.waitForRef(ctx, attempt); waitErr != nil {
			return localCIPlan{}, fmt.Errorf("fetch local CI head and base: %w: %s", waitErr, clip(detail, 500))
		}
	}
}

// ciRefFetchAttempts bounds how often a head is fetched while a just-pushed ref
// becomes visible.
const ciRefFetchAttempts = 3

// ciRefWait grows the wait between those attempts: 2s, then 4s.
const ciRefWait = 2 * time.Second

// waitForRef waits before the next fetch attempt. Service.Wait is the seam a test
// replaces, so no test sleeps.
func (s *Service) waitForRef(ctx context.Context, attempt int) error {
	wait := time.Duration(attempt+1) * ciRefWait
	if s.Wait != nil {
		return s.Wait(ctx, wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Cancellation of a still-current head restores the hosted work we removed.
// Superseded heads stay cancelled. A timeout also saves an infrastructure error.
func (s *Service) recoverInterruptedCI(ctx context.Context, job registeredCIJob, repo string, node localci.Node, result localci.Result) error {
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	current := false
	candidates := registeredCIJobs(st)
	headJobs, err := s.localOnlyPRHeadJobs(ctx, st)
	if err != nil {
		return err
	}
	for _, candidate := range append(candidates, headJobs...) {
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
// required names the checks a trial head still needs; a hosted read that misses
// any of them settles nothing, exactly as the trial gate judges it.
func (s *Service) scheduledCIConclusive(ctx context.Context, job registeredCIJob, repo string, definition *localci.Definition, required []string) (bool, error) {
	r := job.record
	if !job.trial {
		return headVerified(r) && job.head == r.Head && r.Polling.PullHead == r.Head && verdictFromHosted(string(r.CI)).Conclusive(), nil
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
	if !ci.Conclusive() {
		return false, nil
	}
	// A hosted pass cannot settle a head while a required check the hosted run
	// never reports is still missing, such as the local cross builds a room
	// requires. The trial gate keeps waiting for that check, so this scheduler
	// must start the local run instead of skipping it for ever. A hosted
	// failure stays settled: it vetoes the head whatever local CI would add.
	return ci != VerdictPassed || len(missingRequiredCIChecks(baseRequiredCIChecks(required), localci.Result{}, snap)) == 0, nil
}
