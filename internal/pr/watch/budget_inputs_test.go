package watch

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCompletedBudgetInputInvalidation(t *testing.T) {
	for _, change := range []string{"root", "head", "base", "author", "room", "attempt", "policy", "response", "page", "running", "workflow", "unrelated"} {
		t.Run(change, func(t *testing.T) {
			root := "/data/prs"
			r := Record{Key: "pr", Host: "github.com", Repo: "owner/repo", RoomID: "room", Author: "author"}
			snap := Snapshot{}
			snap.Pull.Head.SHA, snap.Pull.Base.Ref = strings.Repeat("a", 40), "main"
			run := Run{ID: 81, Attempt: 1, Status: "completed", HeadSHA: snap.Pull.Head.SHA, Path: ".github/workflows/ci.yml"}
			policy := ChangePolicy{CITimeBudget: "enforce"}
			path := strings.ToLower(r.Host) + "/" + runJobsPath(refRecord(r), run)
			gh := GitHub{requests: &requestCache{entries: map[string]cachedResponse{path: {Body: json.RawMessage("{}")}}, used: map[string]cachedResponse{}}}
			before := budgetInputVersion(root, gh, r, snap, run, policy)
			if before == "" {
				t.Fatal("completed input has no version")
			}
			gh.requests.used = map[string]cachedResponse{}
			switch change {
			case "root":
				root += "-other"
			case "head":
				snap.Pull.Head.SHA = strings.Repeat("b", 40)
			case "base":
				snap.Pull.Base.Ref = "release"
			case "author":
				r.Author = "other"
			case "room":
				r.RoomID = "other"
			case "attempt":
				run.Attempt++
			case "policy":
				policy.CIFailAfter = "10m"
			case "response":
				gh.requests.entries[path] = cachedResponse{Body: json.RawMessage("{\"jobs\":[]}")}
			case "page":
				gh.requests.entries[path] = cachedResponse{Body: json.RawMessage("{}"), Next: "missing-page"}
			case "running":
				run.Status = "in_progress"
			case "workflow":
				key := strings.ToLower(r.Host) + "/" + workflowPath(refRecord(r), run)
				gh.requests.entries[key] = cachedResponse{Body: json.RawMessage("{\"content\":\"changed\"}")}
			case "unrelated":
				r.Title, r.Checked = "new title", time.Unix(1, 0)
			}
			after := budgetInputVersion(root, gh, r, snap, run, policy)
			if (after == before) != (change == "unrelated") {
				t.Fatalf("%s input version: before=%s, after=%s", change, before, after)
			}
		})
	}
}

func TestCompletedBudgetSafetyRefreshReadsJobs(t *testing.T) {
	e, snap := completedBudgetFixture(t)
	runBudgetFixture(t, e, snap)
	r := e.record()
	if r.CIBudgetInputs["81/1"] == "" {
		t.Fatal("completed measurement was not saved")
	}
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	gh := e.s.github(r.RoomID)
	gh.requests = &requestCache{entries: st.Responses[r.Key], used: map[string]cachedResponse{}, rates: cloneRates(st.RateLimits), now: e.s.now, forceBudgets: true}
	if err := os.WriteFile(e.calls, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := e.s.enforceCIBudget(t.Context(), gh, r, snap, EffectivePolicy(st, r)); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil || !strings.Contains(string(calls), "/attempts/1/jobs?") {
		t.Fatalf("full safety poll reused timing without a request: %s, %v", calls, err)
	}
}

func TestRunningBudgetAdvancesPastCompletedEvidence(t *testing.T) {
	e, snap := completedBudgetFixture(t)
	runBudgetFixture(t, e, snap)
	snap.Runs[0].Status, snap.Runs[0].Conclusion = "in_progress", ""
	start := e.clock.Add(-20 * time.Second).Format(time.RFC3339)
	path := "repos/owner/repo/actions/runs/81/attempts/1/jobs?per_page=100"
	e.f.Raw[path] = fmt.Sprintf("HTTP/2.0 200 OK\r\n\r\n{\"jobs\":[{\"name\":\"Go tests\",\"status\":\"in_progress\",\"started_at\":%q,\"steps\":[{\"name\":\"Test\",\"status\":\"in_progress\",\"started_at\":%q}]}]}", start, start)
	e.clock = e.clock.Add(4 * time.Minute)
	e.write()
	runBudgetFixture(t, e, snap)
	r := e.record()
	if r.CI != "failed" || !strings.HasPrefix(r.CIBudgetRuns["81/1"], "over CI time budget") || r.CIBudgetInputs["81/1"] != "" {
		t.Fatalf("completed input hid an advancing run: CI=%s, verdict=%s, input=%s", r.CI, r.CIBudgetRuns["81/1"], r.CIBudgetInputs["81/1"])
	}
	calls, err := os.ReadFile(e.calls)
	if err != nil || strings.Count(string(calls), "/runs/81/cancel") != 1 {
		t.Fatalf("advancing PR run was not cancelled once: %s, %v", calls, err)
	}
}

func TestIncompleteBudgetJobsCannotBeReused(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, change := range []string{"running", "missing end", "bad time", "future", "no steps"} {
		t.Run(change, func(t *testing.T) {
			job := budgetJob{Status: "completed", Steps: []budgetStep{{Name: "Test", Status: "completed", Started: now.Add(-time.Second).Format(time.RFC3339), Completed: now.Format(time.RFC3339)}}}
			switch change {
			case "running":
				job.Status = "in_progress"
			case "missing end":
				job.Steps[0].Completed = ""
			case "bad time":
				job.Steps[0].Started = "unknown"
			case "future":
				job.Steps[0].Completed = now.Add(time.Second).Format(time.RFC3339)
			case "no steps":
				job.Steps = nil
			}
			if completedBudgetJobs([]budgetJob{job}, now) {
				t.Fatal("incomplete timing input can be reused")
			}
		})
	}
}
