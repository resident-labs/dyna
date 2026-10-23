package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
	"dyna-agent/internal/pr/state"
)

func TestRegisteredTrialProgressUsesLocalExecutionAndHostedFallback(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	repoDir := filepath.Join(t.TempDir(), "repo")
	if raw, err := exec.Command("git", "init", "--quiet", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	repo, err := localci.CanonicalRepo(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	registry := localci.Store{DataDir: dataDir}
	ciMachine := machines.Store{DataDir: dataDir}
	if _, err := ciMachine.Save(ctx, machines.Machine{Name: "worker", Roles: []machines.Role{machines.RoleCI}, SSH: "ari@worker", CISlots: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetMode(ctx, "room-test", localci.ModeLocal); err != nil {
		t.Fatal(err)
	}
	head, prHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
	branch := "dyna/merge-trial/1"
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: prHead, Branch: "feature", RepoPath: repoDir, RoomID: "room-test", State: "open", Watching: true, CI: "passed", Polling: Polling{PullHead: prHead}}
	s := &Service{Store: &Store{Root: state.Root(dataDir)}, LocalCIRegistry: registry}
	if err := s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(r): {Head: head, Branch: branch, Phase: "running", Changes: []state.MergeTrialChange{{Key: r.Key, Head: prHead}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.SaveHostedRuns(ctx, repo, head, localci.HostedRuns{IDs: []int64{81}, Attempts: map[int64]int{81: 1}}); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: 81, Attempt: 1, CheckSuiteID: 9, Path: ".github/workflows/ci.yml", HeadSHA: head, HeadBranch: branch, Event: "push", Status: "completed", Conclusion: "cancelled"}
	jobReads, reruns := 0, 0
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		endpoint := ""
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "repos/") {
				endpoint = arg
			}
		}
		var body any = map[string]any{}
		switch {
		case strings.Contains(endpoint, "/check-runs?"):
			body = map[string]any{"check_runs": []Check{}}
		case strings.Contains(endpoint, "/statuses?"):
			body = []Status{}
		case strings.Contains(endpoint, "/actions/runs?"):
			body = map[string]any{"workflow_runs": []Run{run}}
		case strings.HasSuffix(endpoint, "/rerun"):
			reruns++
			run.Attempt = 2
			run.CheckSuiteID = 10
			run.Status = "queued"
			run.Conclusion = ""
		case strings.HasSuffix(endpoint, "/actions/runs/81"):
			body = run
		case strings.Contains(endpoint, "/jobs?"):
			jobReads++
			jobs := make([]budgetJob, 17)
			for i := range jobs {
				jobs[i] = budgetJob{ID: int64(i + 1), Status: "completed", Conclusion: "cancelled"}
			}
			body = map[string]any{"jobs": jobs}
		default:
			return fmt.Errorf("unexpected endpoint %s", endpoint)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n%s", raw)
		return err
	}
	job := registeredCIJob{record: r, head: head, branch: branch, trial: true}
	s.registeredRuns = &registeredCIRuns{runs: map[string]*registeredCIRun{registeredJobKey(job): {node: "worker", cancel: func() {}}}}
	got, err := s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || got.Status != "pending" || got.Reason != "running local CI on worker" {
		t.Fatalf("local execution: ci=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
	s.registeredRuns.runs = map[string]*registeredCIRun{}
	got, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || got.Status != "pending" || got.Reason != "waiting for local CI" {
		t.Fatalf("local queue: ci=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
	result := localci.Result{Repo: repo, SHA: head, RoomID: r.RoomID, Node: "worker", Status: "passed"}
	for _, name := range requiredRegisteredChecks(localci.WorkflowSuites()) {
		result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: "passed"})
	}
	if err := registry.SaveResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	got, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || got.Status != "passed" {
		t.Fatalf("completed local pass: ci=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
	result.Status = "failed"
	result.Checks[0].Status = "failed"
	if err := registry.SaveResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	got, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || got.Status != "failed" {
		t.Fatalf("completed local failure: ci=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
	result.Status = "error"
	result.Checks = nil
	result.Error = "node unavailable"
	if err := registry.SaveResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	st, err := s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.scheduleRegisteredCI(ctx, st); err != nil {
		t.Fatal(err)
	}
	if reruns != 1 {
		t.Fatalf("infrastructure failure did not restore hosted run: reruns=%d", reruns)
	}
	got, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || got.Status != "pending" || got.Reason != "waiting for hosted CI fallback" {
		t.Fatalf("queued fallback: ci=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
	run.Status = "in_progress"
	got, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || got.Status != "pending" || got.Reason != "running hosted CI fallback" {
		t.Fatalf("running fallback: ci=%s reason=%q err=%v", got.Status, got.Reason, err)
	}
	if jobReads != 0 {
		t.Fatalf("read cancelled hosted job counts %d times", jobReads)
	}
}

// A hosted check that fails while the rest of its run is still going leaves no
// run log. The trial read must name the check, so a failed batch always records
// the reason it failed.
func TestTrialCINamesAFailedCheckWithoutARunLog(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	repoDir := filepath.Join(t.TempDir(), "repo")
	if raw, err := exec.Command("git", "init", "--quiet", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	registry := localci.Store{DataDir: dataDir}
	ciMachine := machines.Store{DataDir: dataDir}
	if _, err := ciMachine.Save(ctx, machines.Machine{Name: "worker", Roles: []machines.Role{machines.RoleCI}, SSH: "ari@worker", CISlots: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetMode(ctx, "room-test", localci.ModeLocal); err != nil {
		t.Fatal(err)
	}
	head, prHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
	branch := "dyna/merge-trial/1"
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: prHead, Branch: "feature", RepoPath: repoDir, RoomID: "room-test", State: "open", Watching: true, CI: "passed", Polling: Polling{PullHead: prHead}}
	s := &Service{Store: &Store{Root: state.Root(dataDir)}, LocalCIRegistry: registry}
	if err := s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(r): {Head: head, Branch: branch, Phase: "running", Changes: []state.MergeTrialChange{{Key: r.Key, Head: prHead}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: 81, Attempt: 1, CheckSuiteID: 9, Path: ".github/workflows/ci.yml", HeadSHA: head, HeadBranch: branch, Event: "push", Status: "in_progress"}
	check := Check{ID: 7, Name: "frontend-check", Status: "completed", Conclusion: "failure"}
	check.App.Slug = "github-actions"
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		endpoint := ""
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "repos/") {
				endpoint = arg
			}
		}
		var raw []byte
		var err error
		switch {
		case strings.Contains(endpoint, "/check-runs?"):
			raw, err = json.Marshal(trialChecks{Checks: []Check{check}})
		case strings.Contains(endpoint, "/statuses?"):
			raw, err = json.Marshal([]Status{})
		case strings.Contains(endpoint, "/actions/runs?"):
			raw, err = json.Marshal(trialRuns{Runs: []Run{run}})
		default:
			return fmt.Errorf("unexpected endpoint %s", endpoint)
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n%s", raw)
		return err
	}
	got, err := s.mergeTrialCI(ctx, r, branch, head)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" {
		t.Fatalf("ci=%s reason=%q", got.Status, got.Reason)
	}
	if len(got.Failed) != 1 || got.Failed[0] != "frontend-check" {
		t.Fatalf("failed checks = %v, want the failed check named", got.Failed)
	}
	if !strings.Contains(got.Logs, "frontend-check") || !strings.Contains(got.Reason, "frontend-check") {
		t.Fatalf("evidence logs=%q reason=%q", got.Logs, got.Reason)
	}
}

// trialRuns is the GitHub workflow-runs response of the trial head.
type trialRuns struct {
	Runs []Run `json:"workflow_runs"`
}

// writeFakeJSON answers one fake GitHub read with a concrete response.
func writeFakeJSON[T any](stdout io.Writer, value T) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "HTTP/2.0 200 OK\r\n\r\n%s", raw)
	return err
}

// savedLocalEnv builds a local-mode service for a saved-result case: no CI node
// is registered, so only a saved result can decide the head.
func savedLocalEnv(t *testing.T, hosted localci.HostedRuns, result *localci.Result, runs []Run, checks []Check, required []string, withNode bool) (*Service, Record, string, string, *int) {
	t.Helper()
	ctx := context.Background()
	dataDir := t.TempDir()
	repoDir := filepath.Join(t.TempDir(), "repo")
	if raw, err := exec.Command("git", "init", "--quiet", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	repo, err := localci.CanonicalRepo(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	registry := localci.Store{DataDir: dataDir}
	if err := registry.SetMode(ctx, "room-test", localci.ModeLocal); err != nil {
		t.Fatal(err)
	}
	if withNode {
		ciMachine := machines.Store{DataDir: dataDir}
		if _, err := ciMachine.Save(ctx, machines.Machine{Name: "worker", Roles: []machines.Role{machines.RoleCI}, SSH: "ari@worker", CISlots: 1, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	head, prHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
	branch := "dyna/merge-trial/1"
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: prHead, Branch: "feature", RepoPath: repoDir, RoomID: "room-test", State: "open", Watching: true, CI: "passed", Polling: Polling{PullHead: prHead}}
	r.PolicyOverride.RequiredChecks = required
	s := &Service{Store: &Store{Root: state.Root(dataDir)}, LocalCIRegistry: registry}
	if err := s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(r): {Head: head, Branch: branch, Phase: "publishing", Changes: []state.MergeTrialChange{{Key: r.Key, Head: prHead}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.SaveHostedRuns(ctx, repo, head, hosted); err != nil {
		t.Fatal(err)
	}
	if result != nil {
		result.Repo, result.SHA, result.RoomID = repo, head, r.RoomID
		if err := registry.SaveResult(ctx, *result); err != nil {
			t.Fatal(err)
		}
	}
	for i := range runs {
		runs[i].HeadSHA, runs[i].HeadBranch = head, branch
		if runs[i].Attempt == 0 {
			runs[i].Attempt = 1
		}
	}
	reruns := 0
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		endpoint := ""
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "repos/") {
				endpoint = arg
			}
		}
		switch {
		case strings.HasSuffix(endpoint, "/rerun"):
			reruns++
			return writeFakeJSON(cmd.Stdout, struct{}{})
		case strings.HasSuffix(endpoint, "/actions/runs/81"):
			return writeFakeJSON(cmd.Stdout, runs[0])
		case strings.Contains(endpoint, "/jobs?"):
			return writeFakeJSON(cmd.Stdout, struct {
				Jobs []budgetJob `json:"jobs"`
			}{})
		case strings.Contains(endpoint, "/check-runs?"):
			return writeFakeJSON(cmd.Stdout, trialChecks{Checks: checks})
		case strings.Contains(endpoint, "/statuses?"):
			return writeFakeJSON(cmd.Stdout, []Status{})
		case strings.Contains(endpoint, "/actions/runs?"):
			return writeFakeJSON(cmd.Stdout, trialRuns{Runs: runs})
		default:
			return fmt.Errorf("unexpected endpoint %s", endpoint)
		}
	}
	return s, r, repo, branch, &reruns
}

// fullLocalPass is a saved local result that covers every trial suite.
func fullLocalPass(t *testing.T) localci.Result {
	t.Helper()
	result := localci.Result{Node: "worker", Status: "passed"}
	for _, name := range requiredRegisteredChecks([]string{"relay", "go", "cross-build", "computer-smoke"}) {
		result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: "passed"})
	}
	return result
}

// cancelledTrialRun is the hosted run Dyna cancelled for its own local CI. Its
// suite matches cancelledTrialChecks.
func cancelledTrialRun() Run {
	return Run{ID: 81, CheckSuiteID: 7, Path: ".github/workflows/ci.yml", Event: "push", Status: "completed", Conclusion: "cancelled"}
}

// runningTrialRun is a hosted replacement that is still going.
func runningTrialRun() Run {
	return Run{ID: 81, CheckSuiteID: 9, Path: ".github/workflows/ci.yml", Event: "push", Status: "in_progress"}
}

// A saved local pass decides a head on its own. A host that is briefly
// unavailable must not restart the hosted run that local CI replaced: that wait
// costs a full hosted run and holds work the local pass already released.
func TestSavedLocalPassNeedsNoHostedFallback(t *testing.T) {
	result := fullLocalPass(t)
	s, r, repo, branch, reruns := savedLocalEnv(t, localci.HostedRuns{IDs: []int64{81}, Attempts: map[int64]int{81: 1}}, &result, []Run{cancelledTrialRun()}, nil, nil, false)
	_ = repo
	got, err := s.mergeTrialCI(t.Context(), r, branch, result.SHA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" {
		t.Fatalf("ci=%s reason=%q; a saved local pass waited for hosted CI", got.Status, got.Reason)
	}
	if *reruns != 0 {
		t.Fatalf("reran hosted CI %d times for a head that local CI already passed", *reruns)
	}
}

// A saved local pass is judged by the same gates as a fresh one: a hosted
// failure still vetoes it, a partial suite cannot release a merge, and a
// required check without evidence holds it pending.
func TestSavedLocalPassKeepsTheCIGates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     func(t *testing.T) localci.Result
		runs       []Run
		checks     []Check
		required   []string
		want       string
		wantReason string
	}{
		{
			name:   "hosted failure vetoes a saved pass",
			result: fullLocalPass,
			runs:   []Run{cancelledTrialRun()},
			checks: []Check{actionsCheck(Check{ID: 700, Name: "security", Status: "completed", Conclusion: "failure"})},
			want:   "failed",
		},
		{
			name: "partial suite does not pass",
			result: func(*testing.T) localci.Result {
				return localci.Result{Node: "worker", Status: "passed", Checks: []localci.SuiteResult{{CheckName: "go-test-shard (0)", Status: "passed"}}}
			},
			runs: []Run{runningTrialRun()},
			want: "pending",
		},
		{
			name:       "required check without evidence",
			result:     fullLocalPass,
			runs:       []Run{runningTrialRun()},
			required:   []string{"security"},
			want:       "pending",
			wantReason: "required CI checks: security",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.result(t)
			s, r, _, branch, _ := savedLocalEnv(t, localci.HostedRuns{}, &result, tc.runs, tc.checks, tc.required, false)
			got, err := s.mergeTrialCI(t.Context(), r, branch, result.SHA)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Fatalf("ci=%s reason=%q, want %s", got.Status, got.Reason, tc.want)
			}
			if tc.wantReason != "" && !strings.Contains(got.Reason, tc.wantReason) {
				t.Fatalf("reason=%q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// A hosted workflow that only failed through its own cancellations never tested
// the head, so it must not veto a passing local result. Trial 159 failed this
// way: Dyna's CI time budget ended the hosted run, every job was cancelled, and
// only the go-test aggregate reported failure while local CI had passed.
func TestCancelledHostedWorkflowDoesNotFailATrial(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	repoDir := filepath.Join(t.TempDir(), "repo")
	if raw, err := exec.Command("git", "init", "--quiet", repoDir).CombinedOutput(); err != nil {
		t.Fatalf("init repository: %v: %s", err, raw)
	}
	repo, err := localci.CanonicalRepo(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	registry := localci.Store{DataDir: dataDir}
	ciMachine := machines.Store{DataDir: dataDir}
	if _, err := ciMachine.Save(ctx, machines.Machine{Name: "worker", Roles: []machines.Role{machines.RoleCI}, SSH: "ari@worker", CISlots: 1, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetMode(ctx, "room-test", localci.ModeLocal); err != nil {
		t.Fatal(err)
	}
	head, prHead := strings.Repeat("a", 40), strings.Repeat("b", 40)
	branch := "dyna/merge-trial/159"
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Head: prHead, Branch: "feature", RepoPath: repoDir, RoomID: "room-test", State: "open", Watching: true, CI: "passed", Polling: Polling{PullHead: prHead}}
	s := &Service{Store: &Store{Root: state.Root(dataDir)}, LocalCIRegistry: registry}
	if err := s.Store.update(func(st *State) error {
		st.Records[r.Key] = &r
		st.MergeTrials = map[string]*state.MergeTrial{defaultRepo(r): {Head: head, Branch: branch, Phase: "running", Changes: []state.MergeTrialChange{{Key: r.Key, Head: prHead}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	result := localci.Result{Repo: repo, SHA: head, RoomID: r.RoomID, Node: "worker", Status: "passed"}
	for _, name := range requiredRegisteredChecks([]string{"relay", "go", "cross-build", "computer-smoke"}) {
		result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: "passed"})
	}
	if err := registry.SaveResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: 81, Attempt: 1, CheckSuiteID: 7, Path: ".github/workflows/ci.yml", HeadSHA: head, HeadBranch: branch, Event: "push", Status: "completed", Conclusion: "cancelled"}
	var checks []Check
	for i, job := range []struct{ name, conclusion string }{
		{"changes", "success"}, {"cache-warm", "skipped"}, {"computer-smoke", "success"}, {"relay", "success"},
		{"frontend", "cancelled"}, {"frontend-check", "success"}, {"go-static", "cancelled"},
		{"go-test-shard (0)", "cancelled"}, {"go-test-shard (1)", "cancelled"},
		{"go-test", "failure"},
	} {
		check := actionsCheck(Check{ID: int64(i + 1), Name: job.name, Status: "completed", Conclusion: job.conclusion})
		check.CheckSuite.ID = 7
		checks = append(checks, check)
	}
	s.GH.run = func(_ context.Context, cmd *exec.Cmd) error {
		endpoint := ""
		for _, arg := range cmd.Args {
			if strings.HasPrefix(arg, "repos/") {
				endpoint = arg
			}
		}
		switch {
		case strings.Contains(endpoint, "/check-runs?"):
			return writeFakeJSON(cmd.Stdout, trialChecks{Checks: checks})
		case strings.Contains(endpoint, "/statuses?"):
			return writeFakeJSON(cmd.Stdout, []Status{})
		case strings.Contains(endpoint, "/actions/runs?"):
			return writeFakeJSON(cmd.Stdout, trialRuns{Runs: []Run{run}})
		default:
			return fmt.Errorf("unexpected endpoint %s", endpoint)
		}
	}
	got, err := s.mergeTrialCI(ctx, r, branch, head)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" {
		t.Fatalf("ci=%s reason=%q; a cancelled hosted workflow vetoed a passing local result", got.Status, got.Reason)
	}
}

// cancelledTrialChecks is the job list of a trial run that ended cancelled, as
// trial 159 left it: the shards and slow jobs are cancelled, the aggregate
// reports them, and the other jobs passed.
func cancelledTrialChecks() []Check {
	var checks []Check
	for i, job := range []struct{ name, conclusion string }{
		{"changes", "success"}, {"cache-warm", "skipped"}, {"computer-smoke", "success"}, {"relay", "success"},
		{"frontend", "cancelled"}, {"frontend-check", "success"}, {"go-static", "cancelled"},
		{"go-test-shard (0)", "cancelled"}, {"go-test-shard (1)", "cancelled"},
		{"go-test", "failure"},
	} {
		check := actionsCheck(Check{ID: int64(i + 1), Name: job.name, Status: "completed", Conclusion: job.conclusion})
		check.CheckSuite.ID = 7
		checks = append(checks, check)
	}
	return checks
}

// independentWorkflow is another workflow of the same head that passed.
func independentWorkflow(head, branch string) Run {
	return Run{ID: 90, Attempt: 1, WorkflowID: 2, CheckSuiteID: 8, Path: ".github/workflows/security.yml", HeadSHA: head, HeadBranch: branch, Event: "push", Status: "completed", Conclusion: "success"}
}

func independentCheck() Check {
	check := actionsCheck(Check{ID: 90, Name: "security", Status: "completed", Conclusion: "success"})
	check.CheckSuite.ID = 8
	return check
}

// A cancelled workflow must not become a hosted pass. With no complete local
// result behind it, the cancellation keeps the head from being released even
// when another workflow of the same head passed.
func TestCancelledHostedWorkflowCannotPassAHead(t *testing.T) {
	checks := append(cancelledTrialChecks(), independentCheck())
	runs := []Run{cancelledTrialRun(), independentWorkflow(strings.Repeat("a", 40), "dyna/merge-trial/1")}
	s, r, _, branch, _ := savedLocalEnv(t, localci.HostedRuns{}, nil, runs, checks, nil, false)
	got, err := s.mergeTrialCI(t.Context(), r, branch, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == "passed" {
		t.Fatalf("a cancelled workflow and an independent pass released a head without local CI")
	}
}

// With a complete local pass, the same cancellation does not veto it.
func TestCompleteLocalPassIgnoresACancelledWorkflow(t *testing.T) {
	result := fullLocalPass(t)
	checks := append(cancelledTrialChecks(), independentCheck())
	runs := []Run{cancelledTrialRun(), independentWorkflow(strings.Repeat("a", 40), "dyna/merge-trial/1")}
	s, r, _, branch, _ := savedLocalEnv(t, localci.HostedRuns{}, &result, runs, checks, nil, false)
	got, err := s.mergeTrialCI(t.Context(), r, branch, strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" {
		t.Fatalf("ci=%s reason=%q; a cancelled workflow vetoed a complete local pass", got.Status, got.Reason)
	}
}

// The scheduler must still run local CI when the only hosted evidence is a
// cancelled workflow: treating it as settled would skip local execution and
// leave the trial waiting for CI that never comes.
func TestSchedulerRunsLocalCIAfterHostedCancellation(t *testing.T) {
	head := strings.Repeat("a", 40)
	s, r, repo, branch, _ := savedLocalEnv(t, localci.HostedRuns{}, nil, []Run{cancelledTrialRun()}, cancelledTrialChecks(), nil, false)
	job := registeredCIJob{record: r, head: head, branch: branch, trial: true}
	conclusive, err := s.scheduledCIConclusive(t.Context(), job, repo)
	if err != nil {
		t.Fatal(err)
	}
	if conclusive {
		t.Fatal("a cancellation-only hosted read settled the head, so local CI was skipped")
	}
}

// With an enabled node and no complete local result, a cancellation-only hosted
// failure must keep the trial pending: failing here freezes the batch before the
// local CI that is still queued gets to decide. A complete local pass then
// decides the head.
func TestEnabledNodeWaitsForLocalCIAfterHostedCancellation(t *testing.T) {
	s, r, repo, branch, _ := savedLocalEnv(t, localci.HostedRuns{}, nil, []Run{cancelledTrialRun()}, cancelledTrialChecks(), nil, true)
	head := strings.Repeat("a", 40)
	got, err := s.mergeTrialCI(t.Context(), r, branch, head)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" {
		t.Fatalf("ci=%s reason=%q; a cancellation froze the batch before local CI could run", got.Status, got.Reason)
	}
	result := fullLocalPass(t)
	result.Repo, result.SHA, result.RoomID = repo, head, r.RoomID
	if err := s.ciRegistry().SaveResult(t.Context(), result); err != nil {
		t.Fatal(err)
	}
	got, err = s.mergeTrialCI(t.Context(), r, branch, head)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" {
		t.Fatalf("ci=%s reason=%q; the complete local pass did not decide the head", got.Status, got.Reason)
	}
}
