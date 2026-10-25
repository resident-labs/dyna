package watch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

func (s *Service) localRuns() *localCIStore {
	localCIInit.Lock()
	defer localCIInit.Unlock()
	if s.localCIRuns == nil {
		s.localCIRuns = &localCIStore{}
	}
	return s.localCIRuns
}

// localCIStore is the local lane's admitted CI runs, by repository and head. One
// mutex covers admission, the runs and a snapshot, so a closing engine refuses a
// new run at the same moment it takes the runs it has.
type localCIStore struct {
	mu     sync.Mutex
	closed bool
	runs   map[string]*localCIRun
}

// admit keeps run for key when no other run holds that head and the engine still
// admits work. The caller starts the run only when start is true.
func (l *localCIStore) admit(key string, run *localCIRun) (current *localCIRun, start, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil, false, false
	}
	if existing, found := l.runs[key]; found {
		return existing, false, true
	}
	if l.runs == nil {
		l.runs = map[string]*localCIRun{}
	}
	l.runs[key] = run
	return run, true, true
}

// load returns the run for one repository and head.
func (l *localCIStore) load(key string) (*localCIRun, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	run, ok := l.runs[key]
	return run, ok
}

// forget drops one finished run.
func (l *localCIStore) forget(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.runs, key)
}

// snapshot copies the runs admitted so far.
func (l *localCIStore) snapshot() []*localCIRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	runs := make([]*localCIRun, 0, len(l.runs))
	for _, run := range l.runs {
		runs = append(runs, run)
	}
	return runs
}

// close refuses new runs and returns the ones admitted so far, in one step.
func (l *localCIStore) close() []*localCIRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	runs := make([]*localCIRun, 0, len(l.runs))
	for _, run := range l.runs {
		runs = append(runs, run)
	}
	return runs
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
	cancel context.CancelFunc
	head   string
	result localci.Result
	err    error
}

// completeLocalCI excludes cached manual runs of only part of the base-defined trial suite.
func completeLocalCI(result localci.Result, definition *localci.Definition, suites []localci.Suite) bool {
	return definition.CoversSuites(result, suites)
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
	result, err := runner.Show(r.RepoPath, head)
	if err == nil && result.Node != "" && (result.Status == localci.OutcomeFailed || result.Status == localci.OutcomeError) {
		status, logs, reason, statusErr := localCIStatus(result, r.RepoPath, head, nil, nil)
		return trialCI{Status: status, Logs: logs, Reason: reason}, statusErr
	}
	plan, err := s.planLocalLaneCI(ctx, r, head)
	if err != nil {
		return trialCI{}, err
	}
	if !plan.runnable() {
		return trialCI{Status: VerdictPending, Reason: "local CI is not configured at the trial head and its base"}, nil
	}
	show := func(result localci.Result) (trialCI, error) {
		status, logs, reason, err := localCIStatus(result, r.RepoPath, head, plan.required, plan.suites)
		return trialCI{Status: status, Logs: logs, Reason: reason}, err
	}
	if err == nil && completeLocalCI(result, plan.required, plan.suites) {
		s.localRuns().forget(r.RepoPath + "\x00" + head)
		return show(result)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return trialCI{}, err
	}
	key := r.RepoPath + "\x00" + head
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Minute)
	run := &localCIRun{done: make(chan struct{}), cancel: cancel, head: head}
	current, start, admitted := s.localRuns().admit(key, run)
	if !admitted {
		cancel()
		return trialCI{Status: VerdictPending, Reason: "waiting for local CI"}, nil
	}
	if !start {
		cancel()
		run = current
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
		defer cancel()
		runRunner := runner
		if s.LocalCI == nil {
			node, err := (localci.Store{DataDir: dataDir}).LegacyRemoteNode(runCtx)
			if err != nil {
				run.err = err
				return
			}
			nodeRunner := localci.Runner{DataDir: dataDir, Node: &node}
			nodeRunner.OnCacheRepair = s.CacheRepairReporter(r.RoomID)
			runRunner = nodeRunner
		}
		run.result, run.err = runRunner.Run(runCtx, localci.Request{Definition: plan.execution, Repo: r.RepoPath, SHA: head, Suites: plan.suites})
	}()
	return trialCI{Status: VerdictPending, Reason: "waiting for local CI"}, nil
}

func (s *Service) planLocalLaneCI(ctx context.Context, r Record, head string) (localCIPlan, error) {
	var plan localCIPlan
	execution, err := localci.LoadDefinition(ctx, r.RepoPath, head)
	if errors.Is(err, localci.ErrDefinitionNotFound) {
		plan.headMissing = true
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.execution = execution
	base, err := gitRead(ctx, r.RepoPath, "rev-parse", "--verify", "--end-of-options", head+"^1^{commit}")
	if err != nil {
		return plan, fmt.Errorf("resolve local trial base: %w", err)
	}
	required, err := localci.LoadDefinition(ctx, r.RepoPath, base)
	if errors.Is(err, localci.ErrDefinitionNotFound) {
		plan.baseMissing = true
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.required = required
	plan.suites = required.WorkflowSuites()
	return plan, nil
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
