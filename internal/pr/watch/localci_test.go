package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/state"
)

func TestRegisteredCIRaceDoesNotHideFailuresOrBlockHostedRecovery(t *testing.T) {
	for _, tc := range []struct{ hosted, local, want string }{
		{"pending", "passed", "passed"}, {"passed", "pending", "passed"},
		{"failed", "passed", "failed"}, {"passed", "failed", "failed"},
		{"pending", "pending", "pending"}, {"passed", "passed", "passed"},
	} {
		got := chooseRegisteredCI("head", tc.hosted, "hosted", []string{"hosted-check"}, nil, tc.local, []string{"local-check"}, "local log")
		if got.Status != tc.want {
			t.Fatalf("hosted %s/local %s = %s, want %s", tc.hosted, tc.local, got.Status, tc.want)
		}
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
	infra := localci.Result{Node: "node-a", Status: "error", Checks: []localci.SuiteResult{{CheckName: "go-test", Status: "error"}}}
	if status, _, _ := registeredResultStatus(infra); status != "pending" {
		t.Fatalf("infrastructure error blocks fallback: %s", status)
	}
	infra.Checks = append(infra.Checks, localci.SuiteResult{CheckName: "relay", Status: "failed"})
	if status, _, _ := registeredResultStatus(infra); status != "failed" {
		t.Fatalf("infrastructure error hid a real test failure: %s", status)
	}
}

func TestRegisteredCIRequiresCompleteWorkflowChecks(t *testing.T) {
	suites := []string{"relay", "go", "cross-build", "computer-smoke", "android"}
	required := requiredRegisteredChecks(suites)
	result := localci.Result{Node: "node-a", Status: "passed"}
	for _, name := range required {
		result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: "passed"})
	}
	if !coversRegisteredChecks(result, required) {
		t.Fatal("complete run rejected")
	}
	result.Checks = result.Checks[:len(result.Checks)-1]
	if coversRegisteredChecks(result, required) {
		t.Fatal("missing Android check passed the trial gate")
	}
	result.Node = ""
	if coversRegisteredChecks(result, []string{"relay"}) {
		t.Fatal("manual subset passed the registered gate")
	}
	if got := registeredSuites([]string{"internal/computerweb/handler.go"}); !reflect.DeepEqual(got, []string{"go", "cross-build", "computer-smoke"}) {
		t.Fatalf("computer suites: %v", got)
	}
	if got := registeredSuites([]string{"mobile/android/app/build.gradle"}); !reflect.DeepEqual(got, []string{"android"}) {
		t.Fatalf("Android suites: %v", got)
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
	hosted localci.HostedRuns
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
		return Run{ID: id, Attempt: 1, HeadSHA: sha, HeadBranch: branch, Event: event, Status: status}
	}
	runs := []Run{
		makeRun(1, "queued", "pull_request", "feature", head),
		makeRun(2, "in_progress", "pull_request", "feature", head),
		makeRun(3, "queued", "push", "main", head),
		makeRun(4, "queued", "pull_request", "feature", strings.Repeat("b", 40)),
		makeRun(5, "queued", "pull_request", "feature", head),
	}
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
		body := any(map[string]any{})
		switch {
		case strings.Contains(path, "actions/runs?"):
			body = map[string]any{"workflow_runs": runs}
		case strings.HasSuffix(path, "/cancel"):
			cancelled = append(cancelled, 1)
		case strings.HasSuffix(path, "/rerun"):
			rerun = append(rerun, 1)
		case strings.HasSuffix(path, "/1"):
			fresh := runs[0]
			if len(cancelled) > 0 {
				fresh.Status, fresh.Conclusion = "completed", "cancelled"
			}
			body = fresh
		case strings.HasSuffix(path, "/5"):
			fresh := runs[4]
			fresh.Status = "in_progress"
			body = fresh
		default:
			return fmt.Errorf("unexpected API call %s", path)
		}
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.Stdout, "HTTP/2.0 200 OK\r\n\r\n%s", raw)
		return err
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
	snap := withoutLocalHostedCancellations(Snapshot{Runs: []Run{cancelledRun}, Checks: []Check{check}}, registry.hosted)
	if ci, _, _, _ := snap.CIStatus(); ci != "pending" {
		t.Fatalf("our cancellation became a failure: %s", ci)
	}
	cancelledRun.Attempt = 2
	cancelledRun.Conclusion = "failure"
	snap = withoutLocalHostedCancellations(Snapshot{Runs: []Run{cancelledRun}}, registry.hosted)
	if ci, _, _, _ := snap.CIStatus(); ci != "failed" {
		t.Fatalf("new hosted attempt failure hidden: %s", ci)
	}
}
