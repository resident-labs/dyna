package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/locallane"
	"dyna-agent/internal/pr/facts"
	"dyna-agent/internal/pr/state"
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

func (s *Service) localRuns() *sync.Map {
	localCIInit.Lock()
	defer localCIInit.Unlock()
	if s.localCIRuns == nil {
		s.localCIRuns = &sync.Map{}
	}
	return s.localCIRuns
}

func localMergeReady(r Record) bool {
	return len(facts.MissingRequiredReviewers(r)) == 0 && !facts.OpenOwnerFinding(r) && r.Watching && r.State == "open" && facts.HeadVerified(r) && facts.ApprovedAtHead(r) && (r.MergeTrialFailure == nil || r.MergeTrialFailure.Change.Head != r.Head)
}

func (s *Service) refreshLocalMergeFacts(ctx context.Context) error {
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range st.Records {
		if changeKind(*r) != "local" || !r.Watching || r.State != "open" {
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

type localCIRunner interface {
	Show(string, string) (localci.Result, error)
	Run(context.Context, localci.Request) (localci.Result, error)
}
type localCIRun struct {
	done   chan struct{}
	result localci.Result
	err    error
}

// completeLocalCI excludes cached manual runs of only part of the trial suite.
func completeLocalCI(result localci.Result) bool {
	return localci.CoversSuites(result, localci.WorkflowSuites())
}

func localCIStatus(result localci.Result, repo, head string) (CIVerdict, string, string, error) {
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
		if !completeLocalCI(result) {
			return VerdictPending, "", "waiting for complete local CI", nil
		}
		return VerdictPassed, "", "", nil
	case localci.OutcomeFailed, localci.OutcomeError:
		return VerdictFailed, logs, "local CI failed", nil
	default:
		return VerdictPending, "", "waiting for local CI", nil
	}
}

func (s *Service) localTrialCI(ctx context.Context, r Record, head string) (trialCI, error) {
	dataDir := filepath.Dir(s.Store.Root)
	runner := s.LocalCI
	if runner == nil {
		runner = localci.Runner{DataDir: dataDir}
	}
	show := func(result localci.Result) (trialCI, error) {
		status, logs, reason, err := localCIStatus(result, r.RepoPath, head)
		return trialCI{Status: status, Logs: logs, Reason: reason}, err
	}
	result, err := runner.Show(r.RepoPath, head)
	if err == nil && completeLocalCI(result) {
		s.localRuns().Delete(r.RepoPath + "\x00" + head)
		return show(result)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return trialCI{}, err
	}
	key := r.RepoPath + "\x00" + head
	run := &localCIRun{done: make(chan struct{})}
	value, loaded := s.localRuns().LoadOrStore(key, run)
	if loaded {
		run = value.(*localCIRun)
		select {
		case <-run.done:
			if run.err != nil {
				return trialCI{Status: VerdictFailed, Logs: run.err.Error(), Reason: "local CI failed"}, nil
			}
			return show(run.result)
		default:
			return trialCI{Status: VerdictPending, Reason: "waiting for local CI"}, nil
		}
	}
	go func() {
		defer close(run.done)
		runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Minute)
		defer cancel()
		runRunner := runner
		if s.LocalCI == nil {
			node, err := (localci.Store{DataDir: dataDir}).LegacyRemoteNode(runCtx)
			if err != nil {
				run.err = err
				return
			}
			runRunner = localci.Runner{DataDir: dataDir, Node: &node}
		}
		run.result, run.err = runRunner.Run(runCtx, localci.Request{Repo: r.RepoPath, SHA: head, Suites: localci.WorkflowSuites()})
	}()
	return trialCI{Status: VerdictPending, Reason: "waiting for local CI"}, nil
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

func returnedLocalFailure(logs string) (string, string) {
	suite := ""
	excerpt := logs
	names := localci.RequiredChecks(localci.WorkflowSuites())
	// Results from the old runner used the go suite name without an aggregate.
	names = append(names, localci.CheckName(localci.SuiteGo))
	for _, name := range names {
		for _, status := range []string{"failed", "error"} {
			if at := strings.Index(logs, string(name)+" ("+status+"):"); at >= 0 {
				suite = string(name)
				excerpt = logs[at:]
				break
			}
		}
		if suite != "" {
			break
		}
	}
	if len(excerpt) > 2000 {
		excerpt = excerpt[:2000]
	}
	return suite, excerpt
}
