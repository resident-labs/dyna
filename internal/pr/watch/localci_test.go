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

	"dyna-agent/internal/jobs"
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
	if got := definition.SuitesForFiles([]string{"internal/computerweb/handler.go"}); !reflect.DeepEqual(got, []localci.Suite{localci.SuiteGo, localci.SuiteCross, localci.SuiteComputerSmoke}) {
		t.Fatalf("computer suites: %v", got)
	}
	if got := definition.SuitesForFiles([]string{"docs/readme.md"}); len(got) != 0 {
		t.Fatalf("docs suites: %v", got)
	}
}

// Only a merge trial asks for a local run. A pull request head is checked by
// GitHub's hosted CI, so the queue never lists one, however old or reviewed.
func TestRegisteredCIDoesNotQueuePullRequestHeads(t *testing.T) {
	now := time.Unix(1000, 0)
	record := func(key string, created time.Time) *Record {
		return &Record{Kind: "github", Key: key, Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: key, Branch: key, Watching: true, State: "open", Created: created}
	}
	ready, recent, fork := record("ready", now), record("recent", now.Add(time.Minute)), record("fork", now.Add(-time.Minute))
	fork.HeadRepo = "other/repo"
	st := State{Records: map[string]*Record{"ready": ready, "recent": recent, "fork": fork}, MergeTrials: map[string]*state.MergeTrial{
		"repo":       {Head: "trial", Branch: "dyna/merge-trial/1", Phase: "running", Changes: []state.MergeTrialChange{{Key: "recent", Head: "recent"}}, Created: now},
		"older/repo": {Head: "older", Branch: "dyna/merge-trial/2", Phase: "running", Changes: []state.MergeTrialChange{{Key: "ready", Head: "ready"}}, Created: now.Add(-time.Hour)},
	}}
	jobs := registeredCIJobs(st)
	if len(jobs) != 2 || !jobs[0].trial || jobs[0].head != "older" || jobs[1].head != "trial" {
		t.Fatalf("queue: %+v", jobs)
	}
	// A fork cannot enter a local merge trial, and its head needs no local run.
	recent.HeadRepo = "other/repo"
	jobs = registeredCIJobs(st)
	if len(jobs) != 1 || jobs[0].head != "older" {
		t.Fatalf("fork code entered local CI: %+v", jobs)
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
	trialHead := func(st *State) *state.MergeTrial {
		return st.MergeTrials[defaultRepo(job.record)]
	}
	if err := s.Store.update(func(st *State) error {
		st.Records[job.record.Key] = &job.record
		st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(job.record): {Phase: state.MergeTrialPhaseRunning, Branch: job.branch, Head: head, Changes: []state.MergeTrialChange{{Key: job.record.Key, Head: head}}}}
		return nil
	}); err != nil {
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
	// An old trial head must not restart work after the batch moved on.
	registry.hosted.Resumed = false
	if err := s.Store.update(func(st *State) error {
		st.Records[job.record.Key].Head = strings.Repeat("b", 40)
		trialHead(st).Head = strings.Repeat("b", 40)
		return nil
	}); err != nil {
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
	return []localci.Node{{Name: "node", Enabled: true, CPUs: 2}}, nil
}
func (r *occupiedCIRegistry) Mode(context.Context, string) (string, error) {
	return localci.ModeLocal, nil
}
func (r *occupiedCIRegistry) LoadResult(context.Context, string, string) (localci.Result, error) {
	return localci.Result{}, os.ErrNotExist
}
func TestRegisteredCICancellationKeepsItsQueuePlaceUntilExecutionStops(t *testing.T) {
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	r := &Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: strings.Repeat("a", 40), Branch: "feature", RepoPath: repo, Watching: true, State: "open"}
	cancelled := 0
	old := &registeredCIRun{node: "node", cancel: func() { cancelled++ }}
	dataDir := t.TempDir()
	// The run a newer head cancelled still executes, so its work still holds
	// the node's whole queue.
	if _, err := (jobs.Admission{DataDir: dataDir}).Hold(context.Background(), jobs.Demand{Node: "node", ID: "old", CPUs: 2}); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: &Store{Root: filepath.Join(dataDir, "state.db")}, LocalCIRegistry: &occupiedCIRegistry{}, registeredRuns: &registeredCIRuns{runs: map[string]*registeredCIRun{"old-head": old}}}
	if err := s.scheduleRegisteredCI(context.Background(), State{Records: map[string]*Record{r.Key: r}}); err != nil {
		t.Fatal(err)
	}
	if cancelled != 1 || len(s.registeredRuns.runs) != 1 || s.registeredRuns.runs["old-head"] != old {
		t.Fatalf("cancellation freed a live place: cancelled=%d runs=%v", cancelled, s.registeredRuns.runs)
	}
}

type interruptedCIContext struct {
	context.Context
	reason error
}

func (c interruptedCIContext) Err() error { return c.reason }

func TestRegisteredCISkipsCurrentConclusiveHeadsAndTrials(t *testing.T) {
	head := strings.Repeat("a", 40)
	r := Record{Head: head, CI: "passed", Polling: Polling{PullHead: head}}
	s := &Service{LocalCIRegistry: &hostedCIRegistry{}}
	for _, tc := range []struct {
		ci, polled  string
		stale, want bool
	}{{"passed", head, false, true}, {"failed", head, false, true}, {"pending", head, false, false}, {"passed", "old", false, false}, {"passed", head, true, false}} {
		r.CI, r.Polling.PullHead, r.HeadStale = tc.ci, tc.polled, tc.stale
		got, err := s.scheduledCIConclusive(context.Background(), registeredCIJob{record: r, head: head}, "repo", watchDynaDefinition(t), nil)
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
	if done, err := s.scheduledCIConclusive(context.Background(), job, "repo", watchDynaDefinition(t), nil); err != nil || !done {
		t.Fatalf("passing exact trial: %t %v", done, err)
	}
	run.HeadBranch = "main"
	if done, err := s.scheduledCIConclusive(context.Background(), job, "repo", watchDynaDefinition(t), nil); err != nil || done {
		t.Fatalf("unrelated main push settled trial: %t %v", done, err)
	}
	// A settled trial result does not remove an already active run from the queue.
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	r = Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Branch: "dyna/merge-trial/1", RepoPath: repo, Watching: true, State: "open", CI: "passed", Polling: Polling{PullHead: head}}
	activeJob := registeredCIJob{record: r, head: head, branch: r.Branch, trial: true}
	cancelled := false
	active := &registeredCIRun{node: "node", cancel: func() { cancelled = true }}
	s = &Service{LocalCIRegistry: &occupiedCIRegistry{}, registeredRuns: &registeredCIRuns{runs: map[string]*registeredCIRun{registeredJobKey(activeJob): active}}}
	batch := &state.MergeTrial{Phase: state.MergeTrialPhaseRunning, Branch: r.Branch, Head: head, Changes: []state.MergeTrialChange{{Key: r.Key, Head: head}}}
	if err := s.scheduleRegisteredCI(context.Background(), State{Records: map[string]*Record{r.Key: &r}, MergeTrials: map[string]*state.MergeTrial{defaultRepo(r): batch}}); err != nil {
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
	return []localci.Node{{Name: "node", Enabled: true}}, nil
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

// A completed local run stays in Dyna: no commit status reaches GitHub, so no
// private machine name is published and the evidence is Dyna's own record.
// Automatic work that holds every node slot still keeps its reservation.
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

func TestRegisteredCISavedResultPostsNoGitHubStatus(t *testing.T) {
	repo := t.TempDir()
	if raw, err := exec.Command("git", "init", "--quiet", repo).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	for _, earlierWaiting := range []bool{false, true} {
		t.Run(fmt.Sprintf("earlier_waiting=%t", earlierWaiting), func(t *testing.T) {
			head := strings.Repeat("a", 40)
			r := &Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: head, Branch: "dyna/merge-trial/1", RepoPath: repo, Watching: true, State: "open", Created: time.Unix(2, 0)}
			registry := &savedCIRegistry{result: localci.Result{Repo: repo, SHA: head, Node: "node", Status: "failed", Checks: []localci.SuiteResult{{CheckName: "go-static", Status: "failed"}}}}
			key := registeredJobKey(registeredCIJob{record: *r, head: head})
			cancelled := false
			run := &registeredCIRun{node: "node", cancel: func() { cancelled = true }}
			// The active run's work still holds the node's whole queue.
			dataDir := t.TempDir()
			if _, err := (jobs.Admission{DataDir: dataDir}).Hold(context.Background(), jobs.Demand{Node: "node", ID: "active", CPUs: 2}); err != nil {
				t.Fatal(err)
			}
			s := &Service{Store: &Store{Root: filepath.Join(dataDir, "state.db")}, LocalCIRegistry: registry, registeredRuns: &registeredCIRuns{runs: map[string]*registeredCIRun{key: run}}}
			s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
				t.Fatalf("a local CI result reached GitHub: %v", cmd.Args)
				return nil
			}
			batch := &state.MergeTrial{Phase: state.MergeTrialPhaseRunning, Branch: r.Branch, Head: head, Changes: []state.MergeTrialChange{{Key: r.Key, Head: head}}, Created: time.Unix(2, 0)}
			if earlierWaiting {
				batch.Stacks = []state.MergeTrialStack{{Branch: "dyna/merge-trial/8", Head: strings.Repeat("b", 40), Phase: state.MergeTrialPhaseRunning}}
			}
			st := State{Records: map[string]*Record{r.Key: r}, MergeTrials: map[string]*state.MergeTrial{defaultRepo(*r): batch}}
			if err := s.scheduleRegisteredCI(context.Background(), st); err != nil {
				t.Fatal(err)
			}
			if cancelled || len(s.registeredRuns.runs) != 1 || s.registeredRuns.runs[key] != run {
				t.Fatal("a saved local result changed the active slot reservation")
			}
		})
	}
}

// A manual `dyna git ci run` on a pull request head must poll the PR again, so
// its new result replaces the old verdict without a GitHub change. The marker
// is the result itself, so a manual rerun's pass re-arms it too.
func TestRefreshSavedLocalResultForcesOneFullPoll(t *testing.T) {
	sha := strings.Repeat("a", 40)
	key := "pr"
	read := time.Unix(700, 0)
	settled := &Record{Kind: "github", Key: key, Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: sha, Branch: "feature", Watching: true, State: "open", CI: "failed", Polling: Polling{PullHead: sha, Next: read, FullChecked: read, CIVersion: "probe", PullVersion: "pull"}}
	result := localci.Result{Repo: "repo", SHA: sha, Node: "node", Status: "failed", Checks: []localci.SuiteResult{{CheckName: "go-static", Status: "failed"}}}
	s := &Service{Store: &Store{Root: t.TempDir()}}
	if err := s.Store.update(func(st *State) error {
		st.Records[key] = settled
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshSavedLocalResult(result); err != nil {
		t.Fatal(err)
	}
	refreshed := savedRecord(t, s, key)
	if refreshed.Polling.LocalCI != savedLocalResultKey(result) {
		t.Fatalf("the PR did not record which local result it read: %+v", refreshed.Polling)
	}
	if !refreshed.Polling.Next.IsZero() || !refreshed.Polling.FullChecked.IsZero() {
		t.Fatalf("a new saved result did not force a full poll: %+v", refreshed.Polling)
	}
	// Once the poll runs, the same result must not invalidate it again.
	if err := s.Store.update(func(st *State) error {
		st.Records[key].Polling.Next = read.Add(time.Minute)
		st.Records[key].Polling.FullChecked = read.Add(time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshSavedLocalResult(result); err != nil {
		t.Fatal(err)
	}
	again := savedRecord(t, s, key)
	if !again.Polling.FullChecked.Equal(read.Add(time.Minute)) {
		t.Fatalf("the same saved result forced another full poll: %+v", again.Polling)
	}
	// A result for another head leaves this PR alone.
	other := localci.Result{Repo: "repo", SHA: strings.Repeat("b", 40), Node: "node", Status: "passed"}
	if err := s.RefreshSavedLocalResult(other); err != nil {
		t.Fatal(err)
	}
	if got := savedRecord(t, s, key); !got.Polling.FullChecked.Equal(read.Add(time.Minute)) {
		t.Fatalf("another head refreshed this PR: %+v", got.Polling)
	}
}

// savedRecord reads one stored PR record.
func savedRecord(t *testing.T, s *Service, key string) Record {
	t.Helper()
	st, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	r, ok := st.Records[key]
	if !ok {
		t.Fatalf("record %q is gone", key)
	}
	return *r
}

func TestRegisteredCIPrefersFreeCapacity(t *testing.T) {
	nodes := []localci.Node{
		{Name: "local", Enabled: true},
		{Name: "hermeslab", Enabled: true},
		{Name: "disabled"},
	}
	for _, tc := range []struct {
		free map[string]int
		want string
	}{
		{map[string]int{"local": 1, "hermeslab": 8}, "hermeslab"},
		{map[string]int{"local": 1, "hermeslab": 7}, "hermeslab"},
		{map[string]int{"local": 1, "hermeslab": 1}, "local"},
		{map[string]int{"local": 0, "hermeslab": 0}, ""},
	} {
		node := availableCINode(nodes, "", "", func(n localci.Node) int { return tc.free[n.Name] })
		got := ""
		if node != nil {
			got = node.Name
		}
		if got != tc.want {
			t.Fatalf("free=%v: chose %q, want %q", tc.free, got, tc.want)
		}
	}
}

// A machine that names rooms takes only those rooms' work, so the scheduler and
// the merge trials that share it leave it to the room that owns it.
func TestRegisteredCISkipsMachinesLimitedToOtherRooms(t *testing.T) {
	nodes := []localci.Node{
		{Name: "hermeslab", Enabled: true, Rooms: []string{"room_dyna"}},
		{Name: "pve", Enabled: true, Rooms: []string{"room_fl-rea"}},
		{Name: "arch", Enabled: true},
	}
	free := func(localci.Node) int { return 4 }
	for _, tc := range []struct {
		room string
		full string
		want string
	}{
		{"room_dyna", "", "hermeslab"},
		{"room_fl-rea", "", "pve"},
		// A full machine of the room hands work to a machine with no limit
		// before another room's machine.
		{"room_dyna", "hermeslab", "arch"},
		// A machine with no room list takes every room's work.
		{"room_other", "", "arch"},
	} {
		node := availableCINode(nodes, tc.room, "", func(n localci.Node) int {
			if n.Name == tc.full {
				return 0
			}
			return free(n)
		})
		got := ""
		if node != nil {
			got = node.Name
		}
		if got != tc.want {
			t.Fatalf("room=%s full=%q: chose %q, want %q", tc.room, tc.full, got, tc.want)
		}
	}
	// A room that no machine allows waits for one, and takes no other room's
	// machine.
	if node := availableCINode(nodes[:2], "room_other", "", free); node != nil {
		t.Fatalf("room with no machine chose %q", node.Name)
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
	invalidRepo, invalidBase := changeRepo(t)
	writeTrialDefinition(t, invalidRepo, "version: 1\nunknown: true\n")
	changeGit(t, invalidRepo, "add", ".")
	changeGit(t, invalidRepo, "commit", "-m", "invalid local CI")
	invalid := changeGit(t, invalidRepo, "rev-parse", "HEAD")
	validRepo, validBase := changeRepo(t)
	changeCommit(t, validRepo, "valid local CI\n")
	valid := changeGit(t, validRepo, "rev-parse", "HEAD")
	invalidRecord := &Record{Kind: "github", Key: "invalid", Host: "github.com", Repo: "owner/invalid", HeadRepo: "owner/invalid", Head: invalid, Base: invalidBase, Branch: "dyna/merge-trial/1", RepoPath: invalidRepo, Watching: true, State: "open", CI: "pending", Created: time.Unix(1, 0)}
	validRecord := &Record{Kind: "github", Key: "valid", Host: "github.com", Repo: "owner/valid", HeadRepo: "owner/valid", Head: valid, Base: validBase, Branch: "dyna/merge-trial/2", RepoPath: validRepo, Watching: true, State: "open", CI: "pending", Created: time.Unix(2, 0)}
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
		st.MergeTrials = map[string]*state.MergeTrial{
			defaultRepo(*invalidRecord): {Phase: state.MergeTrialPhaseRunning, Branch: invalidRecord.Branch, Base: invalidBase, Head: invalid, Changes: []state.MergeTrialChange{{Key: invalidRecord.Key, Head: invalid}}},
			defaultRepo(*validRecord):   {Phase: state.MergeTrialPhaseRunning, Branch: validRecord.Branch, Base: validBase, Head: valid, Changes: []state.MergeTrialChange{{Key: validRecord.Key, Head: valid}}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		if filepath.Base(cmd.Path) == "git" {
			return nil
		}
		// The valid trial head has no hosted evidence, so it reads an empty one.
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
