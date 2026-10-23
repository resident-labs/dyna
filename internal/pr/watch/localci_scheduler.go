package watch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	runs map[string]*registeredCIRun
}

// registeredCIRun is one local CI run in flight. node, slots and cancel belong
// to the scheduler; the other fields describe the run to a reader of
// RunningCI.
type registeredCIRun struct {
	node   string
	slots  int
	cancel context.CancelFunc

	repo    string
	number  int
	trial   bool
	head    string
	title   string
	url     string
	started time.Time
	suites  []localci.Suite
}
type registeredCIJob struct {
	record       Record
	head, branch string
	trial        bool
	created      time.Time
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

// Trials use the same node slots as PR heads and enter the queue first.
func registeredCIJobs(st State) []registeredCIJob {
	var jobs []registeredCIJob
	for _, b := range st.MergeTrials {
		if len(b.Changes) == 0 {
			continue
		}
		r := st.Records[b.Changes[0].Key]
		if r == nil || changeKind(*r) != "github" {
			continue
		}
		valid := true
		for _, c := range b.Changes {
			if member := st.Records[c.Key]; !currentTrialMember(member, c) || member == nil || !strings.EqualFold(member.HeadRepo, member.Repo) {
				valid = false
			}
		}
		if !valid {
			continue
		}
		if b.Head != "" && (b.Phase == state.MergeTrialPhaseRunning || b.Phase == state.MergeTrialPhaseBuilding || b.Phase == state.MergeTrialPhasePassed) {
			jobs = append(jobs, registeredCIJob{record: *r, head: b.Head, branch: b.Branch, trial: true, created: b.Created})
		}
		for _, stack := range b.Stacks {
			if stack.Head != "" && stack.Phase == state.MergeTrialPhaseRunning {
				jobs = append(jobs, registeredCIJob{record: *r, head: stack.Head, branch: stack.Branch, trial: true, created: b.Created})
			}
		}
	}
	for _, r := range st.Records {
		if changeKind(*r) == "github" && r.Watching && r.State == "open" && !r.IsDraft && headVerified(*r) && r.Head != "" && strings.EqualFold(r.HeadRepo, r.Repo) {
			jobs = append(jobs, registeredCIJob{record: *r, head: r.Head, branch: r.Branch, created: r.Created})
		}
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].trial != jobs[j].trial {
			return jobs[i].trial
		}
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
		mode, ok := modes[job.record.RoomID]
		if !ok {
			mode, err = registry.Mode(ctx, job.record.RoomID)
			if err != nil {
				return err
			}
			modes[job.record.RoomID] = mode
		}
		if mode != localci.ModeLocal && mode != localci.ModeBoth || len(enabled) == 0 {
			continue
		}
		paused, err := s.RoomPaused(job.record.RoomID)
		if err != nil {
			return err
		}
		if paused {
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
		if _, ok := desired[key]; !ok {
			run.cancel()
			active[run.node] += max(1, run.slots)
			continue
		}
		found := false
		for _, node := range enabled {
			if node.Name == run.node {
				found = true
			}
		}
		if !found {
			run.cancel()
			active[run.node] += max(1, run.slots)
			continue
		}
		active[run.node] += max(1, run.slots)
	}
	for _, job := range jobs {
		key := registeredJobKey(job)
		repo, err := localci.CanonicalRepo(ctx, job.record.RepoPath)
		if err != nil {
			return err
		}
		result, err := registry.LoadResult(ctx, repo, job.head)
		if err == nil && s.registeredJobResultComplete(ctx, job, result) {
			applyLocalBudgets(&result, EffectivePolicy(st, job.record))
			if localInfrastructureError(result) {
				if err := s.resumeHostedCI(ctx, job, repo); err != nil {
					return err
				}
			}
			if !localInfrastructureError(result) {
				if err := s.publishLocalStatuses(ctx, job, result); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if runs.runs[key] != nil {
			continue
		}
		chosen := availableCINode(enabled, active)
		if chosen == nil {
			continue
		}
		settled, err := s.scheduledCIConclusive(ctx, job, repo)
		if err != nil {
			return err
		}
		if settled {
			continue
		}
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		slots := min(registeredCIRunSlots, chosen.Slots-active[chosen.Name])
		run := &registeredCIRun{
			node: chosen.Name, slots: slots, cancel: cancel,
			repo: job.record.Repo, number: job.record.Number, trial: job.trial,
			head: job.head, title: job.record.Title, url: job.record.URL, started: s.now(),
		}
		runs.runs[key] = run
		started = true
		active[chosen.Name] += slots
		go s.executeRegisteredCI(runCtx, job, repo, *chosen, key, run)
	}
	return nil
}

// Bound each run so queued heads can share a node. Slot locks in the runner
// still enforce the node-wide limit across processes. Six is the measured best
// on a 16-slot node: three heads took 121 s and gave 14.8 runs per 10 minutes.
const registeredCIRunSlots = 6

// Prefer the node with the most unreserved check slots. A machine that is low
// on disk takes no new run, so its work waits for a node that has space.
func availableCINode(nodes []localci.Node, active map[string]int) *localci.Node {
	var chosen *localci.Node
	free := 0
	for i := range nodes {
		node := &nodes[i]
		if node.LowDisk {
			continue
		}
		if available := node.Slots - active[node.Name]; node.Enabled && available > free {
			chosen, free = node, available
		}
	}
	return chosen
}

func (s *Service) executeRegisteredCI(ctx context.Context, job registeredCIJob, repo string, node localci.Node, key string, run *registeredCIRun) {
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
	suites, err := s.prepareRegisteredCI(ctx, job)
	if len(suites) > 0 {
		s.setRunningCISuites(key, run, suites)
	}
	result := localci.Result{Repo: repo, SHA: job.head, RoomID: job.record.RoomID, Node: node.Name, Started: s.now(), Status: "error"}
	if err == nil && len(suites) > 0 {
		req := localci.Request{Repo: job.record.RepoPath, SHA: job.head, RoomID: job.record.RoomID, Suites: suites, MaxTest: maxTest, Allowed: allowed, DeferSave: true, Parallelism: run.slots}
		req.OnStart = func(startCtx context.Context) error {
			if err := s.cancelQueuedHostedCI(startCtx, job, repo); err != nil {
				log.Printf("cancel queued hosted CI: %v", err)
			}
			return startCtx.Err()
		}
		if s.RunNodeCI != nil {
			result, err = s.RunNodeCI(ctx, node, req)
		} else {
			result, err = (localci.Runner{DataDir: filepath.Dir(s.Store.Root), Node: &node}).Run(ctx, req)
		}
	} else if err == nil {
		result.Status = "passed"
		result.Checks = []localci.SuiteResult{{Suite: localci.SuiteChanges, CheckName: localci.CheckName(localci.SuiteChanges), Node: node.Name, Status: localci.OutcomePassed}}
	}
	if ctx.Err() != nil {
		if err := s.recoverInterruptedCI(ctx, job, repo, node, result); err != nil {
			log.Printf("restore interrupted local CI: %v", err)
		}
		return
	}
	result.Repo, result.SHA, result.RoomID, result.Node = repo, job.head, job.record.RoomID, node.Name
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
		if err := s.resumeHostedCI(ctx, job, repo); err != nil {
			log.Printf("resume hosted CI: %v", err)
		}
	} else {
		if err := s.publishLocalStatuses(ctx, job, result); err != nil {
			log.Printf("publish local CI: %v", err)
		}
	}
	// A local completion must force a full snapshot even if GitHub did not change.
	if err := s.Store.updateRecords([]string{job.record.Key}, func(st *State) error {
		if r := st.Records[job.record.Key]; r != nil && r.Head == job.record.Head {
			r.Polling.Next = time.Time{}
			r.Polling.FullChecked = time.Time{}
		}
		return nil
	}); err != nil {
		log.Printf("refresh local CI: %v", err)
	}
}

func (s *Service) prepareRegisteredCI(ctx context.Context, job registeredCIJob) ([]localci.Suite, error) {
	r := job.record
	args := []string{"fetch", "--quiet", "--no-tags", "--", "https://" + r.Host + "/" + r.Repo + ".git", job.head}
	if !job.trial && headSHAPattern.MatchString(r.Base) {
		args = append(args, r.Base)
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
		return nil, fmt.Errorf("fetch local CI head: %w", err)
	}
	if job.trial {
		return localci.WorkflowSuites(), nil
	}
	if !headSHAPattern.MatchString(r.Base) {
		return nil, fmt.Errorf("local CI requires a verified base SHA")
	}
	files, err := gitRead(ctx, r.RepoPath, "diff", "--name-only", r.Base+"..."+job.head)
	if err != nil {
		return nil, err
	}
	return localci.SuitesForFiles(strings.Split(files, "\n")), nil
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
	if !current {
		return nil
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.recordBudget())
	defer cancel()
	var saveErr error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		result.Repo, result.SHA, result.RoomID, result.Node = repo, job.head, job.record.RoomID, node.Name
		result.Status, result.Error = "error", "local CI exceeded its overall time limit"
		saveErr = s.ciRegistry().SaveResult(recoveryCtx, result)
	}
	return errors.Join(saveErr, s.resumeHostedCI(recoveryCtx, job, repo))
}

// Saved CI belongs to a PR head only after that head received a full poll.
// Trial heads have separate push runs and must use their own exact-head facts.
func (s *Service) scheduledCIConclusive(ctx context.Context, job registeredCIJob, repo string) (bool, error) {
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
	snap = withoutLocalHostedCancellations(snap, saved)
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
