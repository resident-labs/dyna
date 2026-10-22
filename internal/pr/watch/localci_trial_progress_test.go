package watch

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/localci"
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
	if err := registry.PutNode(ctx, localci.Node{Name: "worker", Slots: 1, Enabled: true}); err != nil {
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
	ci, _, reason, err := s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || ci != "pending" || reason != "running local CI on worker" {
		t.Fatalf("local execution: ci=%s reason=%q err=%v", ci, reason, err)
	}
	s.registeredRuns.runs = map[string]*registeredCIRun{}
	ci, _, reason, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || ci != "pending" || reason != "waiting for local CI" {
		t.Fatalf("local queue: ci=%s reason=%q err=%v", ci, reason, err)
	}
	result := localci.Result{Repo: repo, SHA: head, RoomID: r.RoomID, Node: "worker", Status: "passed"}
	for _, name := range requiredRegisteredChecks(localci.WorkflowSuites()) {
		result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: "passed"})
	}
	if err := registry.SaveResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	ci, _, reason, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || ci != "passed" {
		t.Fatalf("completed local pass: ci=%s reason=%q err=%v", ci, reason, err)
	}
	result.Status = "failed"
	result.Checks[0].Status = "failed"
	if err := registry.SaveResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	ci, _, reason, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || ci != "failed" {
		t.Fatalf("completed local failure: ci=%s reason=%q err=%v", ci, reason, err)
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
	ci, _, reason, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || ci != "pending" || reason != "waiting for hosted CI fallback" {
		t.Fatalf("queued fallback: ci=%s reason=%q err=%v", ci, reason, err)
	}
	run.Status = "in_progress"
	ci, _, reason, err = s.mergeTrialCI(ctx, r, branch, head)
	if err != nil || ci != "pending" || reason != "running hosted CI fallback" {
		t.Fatalf("running fallback: ci=%s reason=%q err=%v", ci, reason, err)
	}
	if jobReads != 0 {
		t.Fatalf("read cancelled hosted job counts %d times", jobReads)
	}
}
