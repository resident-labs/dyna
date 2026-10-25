package watch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

func TestRegisteredCIRaceDoesNotHideFailuresOrBlockHostedRecovery(t *testing.T) {
	for _, tc := range []struct {
		hosted, local CIVerdict
		want          CIVerdict
	}{
		{"pending", "passed", "passed"}, {"passed", "pending", "passed"},
		{"failed", "passed", "failed"}, {"passed", "failed", "failed"},
		{"pending", "pending", "pending"}, {"passed", "passed", "passed"},
	} {
		got := chooseRegisteredCI("head", tc.hosted, "hosted", []string{"hosted-check"}, nil, tc.local, []string{"local-check"}, "local log")
		if got.Status != tc.want {
			t.Fatalf("hosted %s/local %s = %s, want %s", tc.hosted, tc.local, got.Status, tc.want)
		}
	}
	pending := Snapshot{Checks: []Check{actionsCheck(Check{Name: "go-static", Status: "completed", Conclusion: "failure"}), actionsCheck(Check{Name: "go-test", Status: "in_progress"})}}
	if ci, _, _, _ := (&Service{}).registeredHostedStatus(pending, Record{}, ChangePolicy{}); ci != "failed" {
		t.Fatalf("pending hosted job hid current failure: %s", ci)
	}
	pending.Checks = []Check{actionsCheck(Check{Name: "go-test", Status: "in_progress"})}
	pending.Statuses = []Status{{ID: 1, Context: "security", State: "failure"}, {ID: 2, Context: "security", State: "success"}}
	if ci, _, _, _ := (&Service{}).registeredHostedStatus(pending, Record{}, ChangePolicy{RequiredChecks: []string{"security"}}); ci != "pending" {
		t.Fatalf("old hosted status blocked successful replacement: %s", ci)
	}
	// A successful replacement hosted attempt clears its earlier failure.
	failed := chooseRegisteredCI("head", "failed", "attempt-1", nil, nil, "pending", nil, "")
	recovered := chooseRegisteredCI("head", "passed", "attempt-2", nil, nil, "pending", nil, "")
	if failed.Status != "failed" || recovered.Status != "passed" {
		t.Fatal("hosted rerun retained an old failure")
	}
	// Posted local statuses must not become a second hosted provider.
	snap := Snapshot{Statuses: []Status{{Context: "go-test", State: "failure", Description: localStatusPrefix + "node-a"}}}
	if status, _, _, _ := snap.CIStatus("go-test"); status != "pending" {
		t.Fatalf("local status fed hosted gate: %s", status)
	}
	infra := localci.Result{Node: "node-a", Status: "error", Checks: []localci.SuiteResult{{CheckName: "go-static", Status: "error"}, {CheckName: "go-test", Status: "failed", Aggregate: true}}}
	if status, _, _ := registeredResultStatus(infra); status != "pending" {
		t.Fatalf("infrastructure error blocks fallback: %s", status)
	}
	if summaries := registeredCheckSummaries(infra); summaries[0].State != "skipped" {
		t.Fatalf("infrastructure error displayed as a test failure: %+v", summaries[0])
	}
	infra.Checks = append(infra.Checks, localci.SuiteResult{CheckName: "relay", Status: "failed"})
	if status, _, _ := registeredResultStatus(infra); status != "failed" {
		t.Fatalf("infrastructure error hid a real test failure: %s", status)
	}
}

func TestRegisteredCIRequiresCompleteWorkflowChecks(t *testing.T) {
	definition := watchDynaDefinition(t)
	suites := definition.WorkflowSuites()
	required := definition.RequiredChecks(suites)
	result := localci.Result{Node: "node-a", Status: "passed"}
	for _, name := range required {
		result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: "passed"})
	}
	if !definition.CoversSuites(result, suites) {
		t.Fatal("complete run rejected")
	}
	result.Checks = result.Checks[:len(result.Checks)-1]
	if definition.CoversSuites(result, suites) {
		t.Fatal("missing check passed the trial gate")
	}
	result.Node = ""
	if definition.CoversSuites(result, []localci.Suite{localci.SuiteRelay}) {
		t.Fatal("manual subset passed the registered gate")
	}
	if got := definition.SuitesForFiles([]string{"internal/computerweb/handler.go"}); !reflect.DeepEqual(got, []localci.Suite{localci.SuiteGo, localci.SuiteCrossBuild, localci.SuiteComputerSmoke}) {
		t.Fatalf("computer suites: %v", got)
	}
	if got := definition.SuitesForFiles([]string{"docs/readme.md"}); len(got) != 0 {
		t.Fatalf("docs suites: %v", got)
	}
}

func TestRegisteredCIQueuesTrialsBeforeOldestSameRepoHeads(t *testing.T) {
	now := time.Unix(1000, 0)
	record := func(key string, created time.Time) *Record {
		return &Record{Kind: "github", Key: key, Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: key, Branch: key, Watching: true, State: "open", Created: created}
	}
	oldest, recent, fork := record("old", now), record("recent", now.Add(time.Minute)), record("fork", now.Add(-time.Minute))
	fork.HeadRepo = "other/repo"
	st := State{Records: map[string]*Record{"old": oldest, "recent": recent, "fork": fork}, MergeTrials: map[string]*state.MergeTrial{"repo": {Head: "trial", Branch: "dyna/merge-trial/1", Phase: "running", Changes: []state.MergeTrialChange{{Key: "recent", Head: "recent"}}}}}
	jobs := registeredCIJobs(st)
	if len(jobs) != 3 || !jobs[0].trial || jobs[0].head != "trial" || jobs[1].head != "old" || jobs[2].head != "recent" {
		t.Fatalf("queue: %+v", jobs)
	}
	recent.HeadRepo = "other/repo"
	for _, job := range registeredCIJobs(st) {
		if job.trial {
			t.Fatal("fork code entered a local merge trial")
		}
	}
}

type hostedCIRegistry struct {
	localCIRegistry
	hosted  localci.HostedRuns
	results []localci.Result
}

func (r *hostedCIRegistry) SaveResult(_ context.Context, result localci.Result) error {
	r.results = append(r.results, result)
	return nil
}
func (r *hostedCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeGitHub, nil
}
func (r *hostedCIRegistry) LoadHostedRuns(context.Context, string, string) (localci.HostedRuns, error) {
	return r.hosted, nil
}
func (r *hostedCIRegistry) SaveHostedRuns(_ context.Context, _, _ string, runs localci.HostedRuns) error {
	r.hosted = runs
	return nil
}

func TestRegisteredCICancelsOnlyQueuedExactHeadAndResumesAfterInfraError(t *testing.T) {
	head := strings.Repeat("a", 40)
	job := registeredCIJob{record: Record{Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Branch: "feature"}, head: head, branch: "feature"}
	makeRun := func(id int64, status, event, branch, sha string) Run {
		return Run{ID: id, Attempt: 1, HeadSHA: sha, HeadBranch: branch, Event: event, Status: status, Path: ".github/workflows/ci.yml"}
	}
	runs := []Run{
		makeRun(1, "queued", "pull_request", "feature", head),
		makeRun(2, "in_progress", "pull_request", "feature", head),
		makeRun(3, "queued", "push", "main", head),
		makeRun(4, "queued", "pull_request", "feature", strings.Repeat("b", 40)),
		makeRun(5, "queued", "pull_request", "feature", head),
		makeRun(6, "pending", "pull_request", "feature", head),
		makeRun(7, "requested", "pull_request", "feature", head),
	}
	security := makeRun(8, "queued", "pull_request", "feature", head)
	security.Path, security.Name = ".github/workflows/security.yml", "CI"
	changed := makeRun(9, "queued", "pull_request", "feature", head)
	runs = append(runs, security, changed)
	registry := &hostedCIRegistry{}
	var cancelled, rerun []int64
	s := &Service{LocalCIRegistry: registry}
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		path := ""
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "repos/") {
				path = arg
			}
		}
		switch {
		case strings.Contains(path, "actions/runs?"):
			return writeFakeJSON(cmd.Stdout, workflowRunsResponse{WorkflowRuns: runs})
		case strings.HasSuffix(path, "/cancel"):
			cancelled = append(cancelled, 1)
			return writeFakeJSON(cmd.Stdout, emptyResponse{})
		case strings.HasSuffix(path, "/rerun"):
			rerun = append(rerun, 1)
			return writeFakeJSON(cmd.Stdout, emptyResponse{})
		case strings.HasSuffix(path, "/1"):
			fresh := runs[0]
			if len(cancelled) > 0 {
				fresh.Status, fresh.Conclusion = "completed", "cancelled"
			}
			return writeFakeJSON(cmd.Stdout, fresh)
		case strings.HasSuffix(path, "/9"):
			fresh := runs[len(runs)-1]
			fresh.Path = ".github/workflows/security.yml"
			return writeFakeJSON(cmd.Stdout, fresh)
		case strings.HasSuffix(path, "/5"):
			fresh := runs[4]
			fresh.Status = "in_progress"
			return writeFakeJSON(cmd.Stdout, fresh)
		default:
			return fmt.Errorf("unexpected API call %s", path)
		}
	}
	if err := s.cancelQueuedHostedCI(context.Background(), job, "repo"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cancelled, []int64{1}) || !reflect.DeepEqual(registry.hosted.IDs, []int64{1}) {
		t.Fatalf("cancelled=%v saved=%+v", cancelled, registry.hosted)
	}
	if err := s.resumeHostedCI(context.Background(), job, "repo"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rerun, []int64{1}) || !registry.hosted.Resumed {
		t.Fatalf("rerun=%v saved=%+v", rerun, registry.hosted)
	}
	cancelledRun := runs[0]
	cancelledRun.Status, cancelledRun.Conclusion, cancelledRun.CheckSuiteID = "completed", "cancelled", 9
	check := actionsCheck(Check{Status: "completed", Conclusion: "cancelled"})
	check.CheckSuite.ID = 9
	snap := withoutLocalHostedCancellations(Snapshot{Runs: []Run{cancelledRun}, Checks: []Check{check}}, registry.hosted, watchDynaDefinition(t))
	if ci, _, _, _ := snap.CIStatus(); ci != "pending" {
		t.Fatalf("our cancellation became a failure: %s", ci)
	}
	// Turning local CI off must restore our cancelled hosted run too.
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	registry.hosted.Resumed = false
	job.record.Kind, job.record.Head, job.record.RepoPath = "github", head, repo
	fallback := Snapshot{Runs: []Run{cancelledRun}, Checks: []Check{check}}
	fallback.Pull.Head.SHA = head
	fallback.Pull.Head.Repo.FullName = job.record.Repo
	fallback, err := s.withRegisteredCI(context.Background(), job.record, fallback, ChangePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if ci, _, _, _ := fallback.CIStatus(); ci != "pending" || !registry.hosted.Resumed {
		t.Fatalf("disabled local CI did not restore hosted CI: %s %+v", ci, registry.hosted)
	}
	// A current-head node cancellation restores hosted CI before replacement.
	s.Store = &Store{Root: filepath.Join(t.TempDir(), "prs")}
	job.record.Key, job.record.State, job.record.Watching = "pr", "open", true
	if err := s.Store.update(func(st *State) error { st.Records[job.record.Key] = &job.record; return nil }); err != nil {
		t.Fatal(err)
	}
	registry.hosted.Resumed = false
	interrupted := interruptedCIContext{Context: context.Background(), reason: context.Canceled}
	if err := s.recoverInterruptedCI(interrupted, job, "repo", localci.Node{Name: "node"}, localci.Result{}); err != nil {
		t.Fatal(err)
	}
	if !registry.hosted.Resumed || len(registry.results) != 0 {
		t.Fatalf("current cancellation recovery: %+v results=%v", registry.hosted, registry.results)
	}
	// A deadline saves infrastructure evidence and uses the same hosted fallback.
	registry.hosted.Resumed = false
	interrupted.reason = context.DeadlineExceeded
	if err := s.recoverInterruptedCI(interrupted, job, "repo", localci.Node{Name: "node"}, localci.Result{}); err != nil {
		t.Fatal(err)
	}
	if !registry.hosted.Resumed || len(registry.results) != 1 || registry.results[0].Status != "error" {
		t.Fatalf("timeout recovery: %+v results=%v", registry.hosted, registry.results)
	}
	// An old head must not restart work after a replacement is tracked.
	registry.hosted.Resumed = false
	if err := s.Store.update(func(st *State) error { st.Records[job.record.Key].Head = strings.Repeat("b", 40); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.recoverInterruptedCI(interrupted, job, "repo", localci.Node{Name: "node"}, localci.Result{}); err != nil {
		t.Fatal(err)
	}
	if registry.hosted.Resumed || len(registry.results) != 1 {
		t.Fatal("superseded head restarted hosted CI")
	}
	cancelledRun.Attempt = 2
	cancelledRun.Conclusion = "failure"
	snap = withoutLocalHostedCancellations(Snapshot{Runs: []Run{cancelledRun}}, registry.hosted, watchDynaDefinition(t))
	if ci, _, _, _ := snap.CIStatus(); ci != "failed" {
		t.Fatalf("new hosted attempt failure hidden: %s", ci)
	}
}

type occupiedCIRegistry struct{ hostedCIRegistry }

func (r *occupiedCIRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "node", Slots: 1, Enabled: true}}, nil
}
func (r *occupiedCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}
func (r *occupiedCIRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	return localci.Result{}, os.ErrNotExist
}
func TestRegisteredCICancellationKeepsSlotUntilExecutionStops(t *testing.T) {
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	r := &Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: strings.Repeat("a", 40), Branch: "feature", RepoPath: repo, Watching: true, State: "open"}
	cancelled := 0
	old := &registeredCIRun{node: "node", cancel: func() { cancelled++ }}
	s := &Service{LocalCIRegistry: &occupiedCIRegistry{}, registeredRuns: &registeredCIRuns{runs: map[string]*registeredCIRun{"old-head": old}}}
	if err := s.scheduleRegisteredCI(context.Background(), State{Records: map[string]*Record{r.Key: r}}); err != nil {
		t.Fatal(err)
	}
	if cancelled != 1 || len(s.registeredRuns.runs) != 1 || s.registeredRuns.runs["old-head"] != old {
		t.Fatalf("cancellation freed a live slot: cancelled=%d runs=%v", cancelled, s.registeredRuns.runs)
	}
}

type interruptedCIContext struct {
	context.Context
	reason error
}

func (c interruptedCIContext) Err() error { return c.reason }

func TestRegisteredCIRepublishesReplacementResultsAndBudgets(t *testing.T) {
	head := strings.Repeat("a", 40)
	registry := &hostedCIRegistry{hosted: localci.HostedRuns{Published: true}}
	var states []string
	s := &Service{LocalCIRegistry: registry}
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "state=") {
				states = append(states, strings.TrimPrefix(arg, "state="))
			}
		}
		_, err := fmt.Fprint(cmd.Stdout, "HTTP/2.0 201 Created\r\n\r\n{}")
		return err
	}
	job := registeredCIJob{record: Record{Host: "github.com", Repo: "owner/repo"}, head: head}
	result := localci.Result{Repo: "repo", SHA: head, Node: "node", Status: "failed", Started: time.Unix(100, 0), Checks: []localci.SuiteResult{{CheckName: "go-test", Status: "failed"}}}
	if err := s.publishLocalStatuses(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("legacy publication marker hid current result: %v", states)
	}
	if err := s.publishLocalStatuses(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("unchanged result posted again: %v", states)
	}
	result.Started = result.Started.Add(time.Second)
	result.Status = "passed"
	result.Checks[0].Status = "passed"
	if err := s.publishLocalStatuses(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	result.Checks[0].Duration = 4 * time.Minute
	applyLocalBudgets(&result, ChangePolicy{CITimeBudget: "enforce"})
	if err := s.publishLocalStatuses(context.Background(), job, result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(states, []string{"failure", "success", "failure"}) {
		t.Fatalf("replacement and budget statuses: %v", states)
	}
}

func TestRegisteredCISkipsCurrentConclusiveHeadsAndTrials(t *testing.T) {
	head := strings.Repeat("a", 40)
	r := Record{Head: head, CI: "passed", Polling: Polling{PullHead: head}}
	s := &Service{LocalCIRegistry: &hostedCIRegistry{}}
	for _, tc := range []struct {
		ci, polled  string
		stale, want bool
	}{{"passed", head, false, true}, {"failed", head, false, true}, {"pending", head, false, false}, {"passed", "old", false, false}, {"passed", head, true, false}} {
		r.CI, r.Polling.PullHead, r.HeadStale = tc.ci, tc.polled, tc.stale
		got, err := s.scheduledCIConclusive(context.Background(), registeredCIJob{record: r, head: head}, "repo", watchDynaDefinition(t))
		if err != nil || got != tc.want {
			t.Fatalf("ci=%s polled=%s stale=%t: settled=%t err=%v", tc.ci, tc.polled, tc.stale, got, err)
		}
	}
	job := registeredCIJob{record: Record{Host: "github.com", Repo: "owner/repo"}, head: head, branch: "dyna/merge-trial/1", trial: true}
	run := Run{ID: 1, HeadSHA: head, HeadBranch: job.branch, Event: "push", Status: "completed", Conclusion: "success"}
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		for _, arg := range cmd.Args {
			switch {
			case strings.Contains(arg, "check-runs?"):
				return writeFakeJSON(cmd.Stdout, checkRunsResponse{CheckRuns: []Check{actionsCheck(Check{Name: "go-test", Status: "completed", Conclusion: "success"})}})
			case strings.Contains(arg, "actions/runs?"):
				return writeFakeJSON(cmd.Stdout, workflowRunsResponse{WorkflowRuns: []Run{run}})
			case strings.Contains(arg, "/statuses?"):
				return writeFakeJSON(cmd.Stdout, []Status{})
			}
		}
		return writeFakeJSON(cmd.Stdout, emptyResponse{})
	}
	if done, err := s.scheduledCIConclusive(context.Background(), job, "repo", watchDynaDefinition(t)); err != nil || !done {
		t.Fatalf("passing exact trial: %t %v", done, err)
	}
	run.HeadBranch = "main"
	if done, err := s.scheduledCIConclusive(context.Background(), job, "repo", watchDynaDefinition(t)); err != nil || done {
		t.Fatalf("unrelated main push settled trial: %t %v", done, err)
	}
	// A settled record does not remove an already active run from the queue.
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	r = Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Branch: "feature", RepoPath: repo, Watching: true, State: "open", CI: "passed", Polling: Polling{PullHead: head}}
	activeJob := registeredCIJob{record: r, head: head}
	cancelled := false
	active := &registeredCIRun{node: "node", cancel: func() { cancelled = true }}
	s = &Service{LocalCIRegistry: &occupiedCIRegistry{}, registeredRuns: &registeredCIRuns{runs: map[string]*registeredCIRun{registeredJobKey(activeJob): active}}}
	if err := s.scheduleRegisteredCI(context.Background(), State{Records: map[string]*Record{r.Key: &r}}); err != nil {
		t.Fatal(err)
	}
	if cancelled || s.registeredRuns.runs[registeredJobKey(activeJob)] != active {
		t.Fatal("settled hosted result stopped an active local run")
	}
}

type requiredCIRegistry struct {
	hostedCIRegistry
	result localci.Result
}

func (r *requiredCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}
func (r *requiredCIRegistry) Nodes(context.Context) ([]localci.Node, error) {
	return []localci.Node{{Name: "node", Slots: 1, Enabled: true}}, nil
}
func (r *requiredCIRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	return r.result, nil
}

func TestRegisteredCIPreservesRequiredCheckCoverage(t *testing.T) {
	repo, base := changeRepo(t)
	head := changeCommit(t, repo, "trial\n")
	common, err := localci.CanonicalRepo(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	r := Record{Kind: "github", Host: "github.com", Repo: "owner/repo", Head: head, Base: base, Branch: "dyna/merge-trial/1", RepoPath: repo}
	var checks []localci.SuiteResult
	for _, name := range watchDynaRequiredChecks(t) {
		checks = append(checks, localci.SuiteResult{CheckName: name, Status: "passed"})
	}
	for _, tc := range []struct {
		name, want                          string
		check                               *Check
		statuses                            []Status
		localExtra, hostedOnly, localFailed bool
	}{
		{name: "missing", want: "pending"},
		{name: "pending", want: "pending", check: &Check{ID: 1, Name: "security", Status: "in_progress"}},
		{name: "failure", want: "failed", check: &Check{ID: 1, Name: "security", Status: "completed", Conclusion: "failure"}},
		{name: "hosted check success", want: "passed", check: &Check{ID: 1, Name: "security", Status: "completed", Conclusion: "success"}},
		{name: "new pending status", want: "pending", statuses: []Status{{ID: 1, Context: "security", State: "success"}, {ID: 2, Context: "security", State: "pending"}}},
		{name: "new successful status", want: "passed", statuses: []Status{{ID: 1, Context: "security", State: "failure"}, {ID: 2, Context: "security", State: "success"}}},
		{name: "posted local status is not hosted", want: "pending", statuses: []Status{{ID: 1, Context: "security", State: "success", Description: localStatusPrefix + "node"}}},
		{name: "hosted failure cannot be hidden by local status", want: "failed", localExtra: true, statuses: []Status{{ID: 1, Context: "security", State: "failure"}, {ID: 2, Context: "security", State: "success", Description: localStatusPrefix + "node"}}},
		{name: "local exact check success", want: "passed", localExtra: true, check: &Check{ID: 1, Name: "security", Status: "in_progress"}},
		{name: "hosted pass cannot bypass missing", want: "pending", hostedOnly: true},
		{name: "failure still vetoes missing", want: "failed", localFailed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := localci.Result{Repo: common, SHA: head, Node: "node", Status: "passed", Checks: append([]localci.SuiteResult(nil), checks...)}
			if tc.localExtra {
				result.Checks = append(result.Checks, localci.SuiteResult{CheckName: "security", Status: "passed"})
			}
			if tc.localFailed {
				result.Status = "failed"
				result.Checks[0].Status = "failed"
			}
			if tc.hostedOnly {
				result = localci.Result{}
			}
			snap := Snapshot{Runs: []Run{{ID: 1, Status: "completed", Conclusion: "success"}}, Statuses: tc.statuses}
			snap.Pull.Head.SHA = head
			snap.Pull.Head.Repo.FullName = r.Repo
			if tc.check != nil {
				snap.Checks = []Check{*tc.check}
			}
			s := &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "prs")}, LocalCIRegistry: &requiredCIRegistry{result: result}}
			if err := s.Store.update(func(st *State) error {
				st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(r): {Base: base, Head: head, Branch: r.Branch}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			got, err := s.withRegisteredCI(context.Background(), r, snap, ChangePolicy{RequiredChecks: []string{"security"}})
			if err != nil {
				t.Fatal(err)
			}
			if ci, _, _, _ := got.CIStatus(); ci != tc.want {
				t.Fatalf("CI=%s, want %s", ci, tc.want)
			}
		})
	}
}

// A completed manual rerun must reach GitHub even while automatic work holds
// every node slot, including an older job that is still waiting for a slot.
type savedCIRegistry struct {
	occupiedCIRegistry
	result localci.Result
}

func (r *savedCIRegistry) LoadResult(_ context.Context, _, sha string) (localci.Result, error) {
	if sha == r.result.SHA {
		return r.result, nil
	}
	return localci.Result{}, os.ErrNotExist
}

func TestRegisteredCIPublishesSavedResultWhileNodesAreBusy(t *testing.T) {
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	for _, earlierWaiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("earlier_waiting=%t", earlierWaiting), func(t *testing.T) {
			head := strings.Repeat("a", 40)
			r := &Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Branch: "feature", RepoPath: repo, Watching: true, State: "open", Created: time.Unix(2, 0)}
			registry := &savedCIRegistry{result: localci.Result{Repo: repo, SHA: head, Node: "node", Status: "failed", Checks: []localci.SuiteResult{{CheckName: "go-static", Status: "failed"}}}}
			key := registeredJobKey(registeredCIJob{record: *r, head: head})
			cancelled := false
			run := &registeredCIRun{node: "node", cancel: func() { cancelled = true }}
			s := &Service{LocalCIRegistry: registry, registeredRuns: &registeredCIRuns{runs: map[string]*registeredCIRun{key: run}}}
			posts := 0
			s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
				if !strings.Contains(strings.Join(cmd.Args, " "), "state=failure") {
					return fmt.Errorf("unexpected GitHub call: %v", cmd.Args)
				}
				posts++
				_, err := fmt.Fprint(cmd.Stdout, "HTTP/2.0 201 Created\r\n\r\n{}")
				return err
			}
			st := State{Records: map[string]*Record{r.Key: r}}
			if earlierWaiting {
				waiting := *r
				waiting.Key, waiting.Head, waiting.Created = "waiting", strings.Repeat("b", 40), time.Unix(1, 0)
				st.Records[waiting.Key] = &waiting
			}
			if err := s.scheduleRegisteredCI(context.Background(), st); err != nil {
				t.Fatal(err)
			}
			if posts != 1 || registry.hosted.PublishedKey == "" {
				t.Fatalf("busy slots blocked saved result publication: posts=%d marker=%q", posts, registry.hosted.PublishedKey)
			}
			if cancelled || len(s.registeredRuns.runs) != 1 || s.registeredRuns.runs[key] != run {
				t.Fatal("publishing a saved result changed the active slot reservation")
			}
		})
	}
}

func TestRegisteredCIPrefersFreeCapacity(t *testing.T) {
	nodes := []localci.Node{
		{Name: "local", Slots: 1, Enabled: true},
		{Name: "hermeslab", Slots: 8, Enabled: true},
		{Name: "disabled", Slots: 20},
	}
	for _, tc := range []struct {
		active map[string]int
		want   string
	}{
		{map[string]int{}, "hermeslab"},
		{map[string]int{"local": 1}, "hermeslab"},
		{map[string]int{"hermeslab": 4}, "hermeslab"},
		{map[string]int{"hermeslab": 8}, "local"},
		{map[string]int{"hermeslab": 8, "local": 1}, ""},
	} {
		node := availableCINode(nodes, tc.active)
		got := ""
		if node != nil {
			got = node.Name
		}
		if got != tc.want {
			t.Fatalf("active=%v: chose %q, want %q", tc.active, got, tc.want)
		}
	}
}

type preparationCIRegistry struct {
	occupiedCIRegistry
	onSave func(localci.Result)
}

func (r *preparationCIRegistry) SaveResult(_ context.Context, result localci.Result) error {
	r.results = append(r.results, result)
	if r.onSave != nil {
		r.onSave(result)
	}
	return nil
}

func TestRegisteredCIPreparationFailureDoesNotBlockLaterJobs(t *testing.T) {
	repo, base := changeRepo(t)
	writeTrialDefinition(t, repo, "version: 1\nunknown: true\n")
	changeGit(t, repo, "add", ".")
	changeGit(t, repo, "commit", "-m", "invalid local CI")
	invalid := changeGit(t, repo, "rev-parse", "HEAD")
	writeWatchDefinition(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "change.go"), []byte("package fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changeGit(t, repo, "add", ".")
	changeGit(t, repo, "commit", "-m", "valid local CI")
	valid := changeGit(t, repo, "rev-parse", "HEAD")
	invalidRecord := &Record{Kind: "github", Key: "invalid", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: invalid, Base: base, Branch: "invalid", RepoPath: repo, Watching: true, State: "open", CI: "pending", Created: time.Unix(1, 0)}
	validRecord := &Record{Kind: "github", Key: "valid", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: valid, Base: base, Branch: "valid", RepoPath: repo, Watching: true, State: "open", CI: "pending", Created: time.Unix(2, 0)}
	started := make(chan struct{})
	release := make(chan struct{})
	validSaved := make(chan struct{})
	changed := make(chan struct{}, 4)
	registry := &preparationCIRegistry{}
	registry.onSave = func(result localci.Result) {
		if result.SHA == valid {
			close(validSaved)
		}
	}
	s := &Service{Store: &Store{Root: filepath.Join(t.TempDir(), "prs")}, LocalCIRegistry: registry, registeredRuns: &registeredCIRuns{runs: map[string]*registeredCIRun{}}, RunningCIChanged: func() { changed <- struct{}{} }}
	if err := s.Store.update(func(st *State) error {
		st.Records[invalidRecord.Key] = invalidRecord
		st.Records[validRecord.Key] = validRecord
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) == "git" {
			return nil
		}
		return fmt.Errorf("unexpected GitHub call: %v", cmd.Args)
	}
	s.RunNodeCI = func(_ context.Context, _ localci.Node, req localci.Request) (localci.Result, error) {
		if req.SHA != valid {
			t.Errorf("ran unexpected head %s", req.SHA)
		}
		close(started)
		<-release
		return localci.Result{}, fmt.Errorf("stop after proving the valid job started")
	}
	st, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.scheduleRegisteredCI(t.Context(), st); err != nil {
		t.Fatal(err)
	}
	validKey := registeredJobKey(registeredCIJob{record: *validRecord, head: valid})
	runs := s.nodeRuns()
	runs.Lock()
	_, running := runs.runs[validKey]
	runs.Unlock()
	if !running {
		t.Fatal("the valid job was not admitted after the invalid job")
	}
	close(release)
	<-started
	<-validSaved
	for {
		<-changed
		runs := s.nodeRuns()
		runs.Lock()
		_, running := runs.runs[validKey]
		runs.Unlock()
		if !running {
			break
		}
	}
	if len(registry.results) == 0 || registry.results[0].SHA != invalid || registry.results[0].Status != localci.OutcomeError {
		t.Fatalf("invalid job did not save its own infrastructure result: %+v", registry.results)
	}
}
