package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/locallane"
	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
	cisched "dyna-agent/internal/pr/watch/ci"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

// recordLocalLane also recognizes GitHub records tracked without a checkout.
func recordLocalLane(ctx context.Context, r Record) (bool, error) {
	if r.RepoPath != "" {
		enabled, err := locallane.Enabled(ctx, r.RepoPath)
		if err != nil || enabled {
			return enabled, err
		}
	}
	if changeKind(r) == "local" {
		return false, nil
	}
	v, err := settings.Load("")
	if err != nil {
		return false, err
	}
	for _, repo := range v.LocalLaneRepos {
		if matchingCheckout(ctx, r, repo) != "" {
			return true, nil
		}
	}
	return false, nil
}

var localCIInit sync.Mutex

func localMergeReady(r Record, records map[string]*Record) bool {
	_, dependencyWaiting := facts.UnmergedDependency(r, records)
	return !dependencyWaiting && len(facts.MissingRequiredReviewers(r)) == 0 && !facts.OpenOwnerFinding(r) && r.Watching && r.State == state.PROpen && facts.HeadVerified(r) && facts.ApprovedAtHead(r) && (r.MergeTrialFailure == nil || r.MergeTrialFailure.Change.Head != r.Head)
}

func (s *Service) refreshLocalMergeFacts(ctx context.Context) error {
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range st.Records {
		if changeKind(*r) != "local" || !r.Watching || r.State != state.PROpen {
			continue
		}
		if paused, err := s.RoomPaused(r.RoomID); err != nil {
			return err
		} else if paused {
			continue
		}
		if err := s.refreshLocal(ctx, r.Key); err != nil {
			return err
		}
		repo := defaultRepo(*r)
		if seen[repo] {
			continue
		}
		seen[repo] = true
		enabled, err := locallane.Enabled(ctx, r.RepoPath)
		if err != nil {
			return err
		}
		if !enabled {
			continue
		}
		if why, err := s.trialRoomHold(ctx, *r); err != nil {
			return err
		} else if why != "" {
			continue
		}
		head, err := locallane.Ensure(ctx, r.RepoPath)
		if err != nil {
			return err
		}
		f := state.RepositoryFacts{DefaultBranch: locallane.Integration, Head: head, MergeTrialCI: true, CheckedAt: s.now()}

		if err := s.Store.update(func(st *State) error {
			if st.RepositoryFacts == nil {
				st.RepositoryFacts = map[string]state.RepositoryFacts{}
			}
			st.RepositoryFacts[repo] = f
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// completeLocalCI excludes cached manual runs of only part of the base-defined trial suite.
func completeLocalCI(result localci.Result, definition *localci.Definition, suites []localci.Suite) bool {
	return definition.CoversSuites(result, suites)
}

// withLocalJobs names the checks a failed local result could not complete, so
// the trial card shows those jobs instead of an unknown failure.
func withLocalJobs(ci trialCI, result localci.Result) trialCI {
	if ci.Status != VerdictFailed {
		return ci
	}
	ci.Failed = localFailedChecks(result)
	return ci
}

func localCIStatus(result localci.Result, repo, head string, definition *localci.Definition, suites []localci.Suite) (CIVerdict, string, string, error) {
	if result.SHA != head || result.Repo != repo {
		return "", "", "", errors.New("local CI result does not match repository and trial head")
	}
	checks := result.Checks
	if len(checks) == 0 {
		checks = result.Suites
	}
	logs := result.Error
	for _, check := range checks {
		logs += "\n" + string(check.Name()) + " (" + string(check.Status) + "): " + check.LogTail + "\n" + check.Error + "\n" + strings.Join(check.Failures, "\n")
	}
	switch result.Status {
	case localci.OutcomePassed:
		for _, check := range checks {
			if check.Status != localci.OutcomePassed || check.Error != "" || len(check.Failures) > 0 {
				return VerdictFailed, logs, "local CI failed", nil
			}
		}
		if !completeLocalCI(result, definition, suites) {
			return VerdictPending, "", "waiting for complete local CI", nil
		}
		return VerdictPassed, "", "", nil
	case localci.OutcomeFailed:
		return VerdictFailed, logs, "local CI failed", nil
	case localci.OutcomeError:
		// A run the engine's own stop cancelled tested nothing: the same head runs
		// again instead of failing the batch. Once that replacement was stopped
		// too, nothing else will run it, so the trial fails with the cause instead
		// of waiting for a run that will never come.
		if cisched.StoppedLocalResult(result) {
			if result.Resumed {
				return VerdictFailed, logs, localFailureNote(result), nil
			}
			return VerdictPending, "", "local CI stopped with the engine; running it again", nil
		}
		// A run that never started says nothing about the commit, so the verdict
		// names the scheduling cause instead of reporting a failed test.
		if result.Cause == localci.CauseScheduling {
			if result.Resumed {
				return VerdictFailed, logs, localFailureNote(result), nil
			}
			return VerdictPending, "", "local CI could not start a run; trying again", nil
		}
		// A machine fault, including a git push or checkout failure, retries on
		// another machine. The trial waits for that retry. A spent retry falls
		// through and fails with the saved git error.
		if cisched.MachineFailureRetryFrom(result) != "" {
			return VerdictPending, "", "retrying local CI after a node failure", nil
		}
		return VerdictFailed, logs, "local CI failed", nil
	default:
		return VerdictPending, "", "waiting for local CI", nil
	}
}

func (s *Service) localTrialCI(ctx context.Context, r Record, head string) (trialCI, error) {
	job, err := s.localTrialJob(r, head)
	if err != nil {
		return trialCI{}, err
	}
	return s.savedTrialCI(ctx, job)
}

// localSyncCI is the CI of an integration sync head. It runs on the same
// machines as a trial, and the machine view names it as a sync instead of a
// change.
func (s *Service) localSyncCI(ctx context.Context, r Record, head string) (trialCI, error) {
	job, err := s.localTrialJob(r, head)
	if err != nil {
		return trialCI{}, err
	}
	// A sync runs every workflow suite, but it is not a merge trial: the
	// machine view must not call it one.
	job.sync, job.trial = true, false
	return s.savedTrialCI(ctx, job)
}

// localTrialJob names the scheduled CI job for a local head, with the frozen
// base of the batch that owns a trial head. Only a plain local head needs the
// first parent as its base, and withLocalTrialBase reads that then.
func (s *Service) localTrialJob(r Record, head string) (registeredCIJob, error) {
	job := registeredCIJob{record: r, head: head, branch: r.Branch, trial: true, wholeSuite: true}
	if s.Store == nil {
		return job, nil
	}
	st, err := s.Store.Read()
	if err != nil {
		return job, err
	}
	batch := st.MergeTrials[defaultRepo(r)]
	if batch == nil {
		return job, nil
	}
	if batch.Head == head {
		job.base = batch.Base
	}
	if job.base == "" {
		for _, stack := range batch.Stacks {
			if stack.Head == head {
				job.base = batch.Base
				break
			}
		}
	}
	return job, nil
}

// withLocalTrialBase fills in the base of a head that no batch owns. A hosted
// job names its base commit, and a local head's first parent is the commit its
// definition had before the change.
func (s *Service) withLocalTrialBase(ctx context.Context, job registeredCIJob) (registeredCIJob, error) {
	if job.base != "" {
		return job, nil
	}
	base, err := gitRead(ctx, job.record.RepoPath, "rev-parse", "--verify", "--end-of-options", job.head+"^1^{commit}")
	if err != nil {
		return job, fmt.Errorf("resolve local trial base: %w", err)
	}
	job.base = base
	return job, nil
}

// savedTrialCI reports what a local head's own scheduled run proves. A run that
// has not started yet is admitted here, on the same machines, with the same
// budget as a pull request head.
func (s *Service) savedTrialCI(ctx context.Context, job registeredCIJob) (trialCI, error) {
	registry := s.ciRegistry()
	dir, err := s.checkoutDir(ctx, job.record)
	if err != nil {
		return trialCI{}, err
	}
	job.record.RepoPath = dir
	repo, err := localci.CanonicalRepo(ctx, dir)
	if err != nil {
		return trialCI{}, err
	}
	st, err := s.Store.Read()
	if err != nil {
		return trialCI{}, err
	}
	result, err := registry.LoadResult(ctx, repo, job.head)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return trialCI{}, err
	}
	saved := err == nil && result.Node != ""
	// A run Dyna itself stopped tested nothing, so its replacement decides this
	// head. A replacement that was stopped too is spent, and localCIStatus then
	// fails the trial with the cause instead of waiting for a run that will never
	// come. Treating a stop as final here would also keep a sync head, whose only
	// scheduler entry is this reader, from ever retrying.
	retryStop := saved && cisched.StoppedLocalResult(result) && !result.Resumed
	if saved {
		// The budget of the room judges the saved run exactly as the gate does.
		applyLocalBudgets(&result, EffectivePolicy(st, job.record))
		if !retryStop && (result.Status == localci.OutcomeFailed || result.Status == localci.OutcomeError) {
			// A saved failure stays readable without the commits that produced it.
			status, logs, reason, statusErr := localCIStatus(result, repo, job.head, nil, nil)
			return withLocalJobs(trialCI{Status: status, Logs: logs, Reason: reason}, result), statusErr
		}
	}
	job, err = s.withLocalTrialBase(ctx, job)
	if err != nil {
		return trialCI{}, err
	}
	plan, err := s.planRegisteredCI(ctx, job)
	if err != nil {
		return trialCI{}, err
	}
	if !plan.runnable() {
		return trialCI{Status: VerdictPending, Reason: "local CI is not configured at the trial head and its base"}, nil
	}
	if saved && !retryStop && completeLocalCI(result, plan.required, plan.suites) {
		status, logs, reason, statusErr := localCIStatus(result, repo, job.head, plan.required, plan.suites)
		return withLocalJobs(trialCI{Status: status, Logs: logs, Reason: reason}, result), statusErr
	}
	if !s.roomServedByCI(job.record.RoomID) {
		note := localci.RoomNotServedError{RoomID: job.record.RoomID}.Error()
		return trialCI{Status: VerdictFailed, Logs: note, Reason: note}, nil
	}
	started, err := s.startRegisteredCI(ctx, job)
	// A suite that no machine allows never becomes available, so the trial fails
	// now with an error that names it instead of waiting for a machine that
	// cannot take the run.
	if blocked := localci.NodeBlockedError(err); blocked != nil {
		note := blocked.Error()
		return trialCI{Status: VerdictFailed, Logs: note, Reason: note}, nil
	}
	if err != nil {
		return trialCI{}, err
	}
	if !started {
		if node := s.runningCINode(job); node != "" {
			return trialCI{Status: VerdictPending, Reason: "running local CI on " + node}, nil
		}
	}
	return trialCI{Status: VerdictPending, Reason: "waiting for local CI"}, nil
}

// runningCINode names the machine of a run in flight for this job.
func (s *Service) runningCINode(job registeredCIJob) string {
	runs := s.nodeRuns()
	runs.Lock()
	defer runs.Unlock()
	if run := runs.Runs[registeredJobKey(job)]; run != nil {
		return run.Node
	}
	return ""
}

func (s *Service) publishLocalChange(ctx context.Context, r Record, expected, trialID string) error {
	if r.Head != expected {
		return errors.New("local change head changed after publication")
	}
	if err := s.Store.update(func(st *State) error {
		live := st.Records[r.Key]
		if live == nil || live.Head != expected {
			return errors.New("local change changed during publication")
		}
		endRecord(live, true, s.now())
		live.Watching = false
		live.PauseReason = "Change is merged"
		return nil
	}); err != nil {
		return err
	}
	ev := room.MergeEvent{Type: room.MergeLanded, TrialID: trialID, Repo: mergeRepo(r), Base: locallane.Integration, PRs: []room.MergePR{mergePR(r)}, Commit: expected}
	if err := s.postMergeNotice(ctx, r, "local-merged", Notice{Target: r.Author, Merge: &ev, Text: fmt.Sprintf("%s merged into %s. Watching stopped. Its clean worktree can be removed after this session closes.", changeLabel(r), locallane.Integration)}); err != nil {
		return err
	}
	return nil
}

var localFailureLine = regexp.MustCompile(`(?m)^([^\n]+) \((?:failed|error)\):`)

func returnedLocalFailure(logs string) (string, string) {
	suite, excerpt := "", logs
	if match := localFailureLine.FindStringSubmatchIndex(logs); match != nil {
		suite = strings.TrimSpace(logs[match[2]:match[3]])
		excerpt = logs[match[0]:]
	}
	if len(excerpt) > 2000 {
		excerpt = excerpt[:2000]
	}
	return suite, excerpt
}
