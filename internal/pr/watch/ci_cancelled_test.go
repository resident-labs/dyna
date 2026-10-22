package watch

import "testing"

// A cancelled run finished without testing the head. CI fails, so the PR
// needs a push or rerun, but no failed job or run is named: the author is not
// woken with "CI failed" and the failure pause does not count it.
func TestCancelledRunFailsWithoutFailedJobs(t *testing.T) {
	cancelled := Run{ID: 10, WorkflowID: 1, Name: "CI", Attempt: 1, Status: "completed", Conclusion: "cancelled"}
	snap := Snapshot{Runs: []Run{cancelled}}
	state, _, jobs, failedRuns := snap.CIStatus()
	if state != "failed" || len(jobs) != 0 || len(failedRuns) != 0 {
		t.Fatalf("cancelled run: state %q jobs %v failed runs %v, want failed with no failed jobs", state, jobs, failedRuns)
	}
	if !snap.ciCancelled() || !snap.ciOnlyCancelled() {
		t.Fatal("a cancelled run is not reported as cancelled-only CI")
	}

	check := Check{ID: 5, Name: "go-test", Status: "completed", Conclusion: "cancelled"}
	check.App.Slug = "github-actions"
	if state, _, jobs, _ := (Snapshot{Checks: []Check{check}}).CIStatus(); state != "failed" || len(jobs) != 0 {
		t.Fatalf("cancelled Actions check: state %q jobs %v, want failed with no failed jobs", state, jobs)
	}

	// A newer run of the same workflow replaces the cancelled one.
	rerun := Run{ID: 11, WorkflowID: 1, Name: "CI", Attempt: 1, Status: "completed", Conclusion: "success"}
	snap = Snapshot{Runs: []Run{cancelled, rerun}}
	if state, _, _, _ := snap.CIStatus(); state != "passed" {
		t.Fatalf("rerun after cancel: state %q, want passed", state)
	}
	if snap.ciCancelled() {
		t.Fatal("ciCancelled = true after a newer successful run")
	}

	// A real failure still fails.
	failed := Run{ID: 12, WorkflowID: 1, Name: "CI", Attempt: 1, Status: "completed", Conclusion: "failure"}
	if state, _, _, runs := (Snapshot{Runs: []Run{failed}}).CIStatus(); state != "failed" || len(runs) != 1 {
		t.Fatalf("failed run: state %q runs %d, want failed", state, len(runs))
	}
	if (Snapshot{Runs: []Run{failed, {ID: 13, WorkflowID: 2, Name: "Lint", Attempt: 1, Status: "completed", Conclusion: "cancelled"}}}).ciOnlyCancelled() {
		t.Fatal("a real failure next to a cancel was reported as cancelled-only CI")
	}
}

func TestCancelledDuplicateCISelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		runs []Run
		want string
	}{
		{"duplicate and success", []Run{{ID: 1, Conclusion: "success"}, {ID: 3, Conclusion: "cancelled"}}, "passed"},
		{"duplicate and newer failure", []Run{{ID: 1, Conclusion: "success"}, {ID: 2, Conclusion: "failure"}, {ID: 3, Conclusion: "cancelled"}}, "failed"},
		{"only cancelled", []Run{{ID: 3, Conclusion: "cancelled"}}, "cancelled"},
		{"only cancelled duplicates", []Run{{ID: 2, Conclusion: "cancelled"}, {ID: 3, Conclusion: "cancelled"}}, "cancelled"},
		{"cancelled rerun attempt", []Run{{ID: 3, Attempt: 1, Conclusion: "success"}, {ID: 3, Attempt: 2, Conclusion: "cancelled"}}, "cancelled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var checks []Check
			for i := range tc.runs {
				r := &tc.runs[i]
				r.WorkflowID, r.Name, r.Event, r.HeadSHA, r.Status = 1, "CI", "pull_request", "head", "completed"
				r.CheckSuiteID = r.ID
				c := Check{ID: int64(10 + i), Name: "go-test", Status: "completed", Conclusion: r.Conclusion}
				c.App.Slug = "github-actions"
				c.CheckSuite.ID = r.CheckSuiteID
				checks = append(checks, c)
			}
			snap := Snapshot{Runs: tc.runs, Checks: checks}
			got, _, _, _ := snap.CIStatus()
			if want := map[string]string{"cancelled": "failed"}[tc.want]; got != tc.want && got != want {
				t.Fatalf("CI=%s, want %s", got, tc.want)
			}
			if snap.ciOnlyCancelled() != (tc.want == "cancelled") {
				t.Fatalf("cancelled=%v for %s", snap.ciCancelled(), tc.want)
			}
			if summaries := snap.CheckSummaries(Ref{}); len(summaries) != 1 || summaries[0].Conclusion != currentRuns(tc.runs)[0].Conclusion {
				t.Fatalf("summaries disagree with selected run: %+v", summaries)
			}
		})
	}
}

func TestCancelledRunDoesNotBorrowOtherCIEvidence(t *testing.T) {
	for _, field := range []string{"head", "event", "workflow"} {
		t.Run(field, func(t *testing.T) {
			runs := []Run{
				{ID: 1, WorkflowID: 1, HeadSHA: "head", Event: "pull_request", Status: "completed", Conclusion: "success"},
				{ID: 2, WorkflowID: 1, HeadSHA: "head", Event: "pull_request", Status: "completed", Conclusion: "cancelled"},
			}
			switch field {
			case "head":
				runs[0].HeadSHA = "other"
			case "event":
				runs[0].Event = "push"
			case "workflow":
				runs[0].WorkflowID = 2
			}
			if snap := (Snapshot{Runs: runs}); !snap.ciOnlyCancelled() {
				got, _, _, _ := snap.CIStatus()
				t.Fatalf("CI=%s, want cancelled-only", got)
			}
		})
	}
}

func TestCancelledDuplicatePreservesEnforcedBudgetFailures(t *testing.T) {
	for _, kind := range []string{"CI time", "test time"} {
		t.Run(kind, func(t *testing.T) {
			runs := []Run{
				{ID: 1, Attempt: 1, WorkflowID: 1, Event: "pull_request", HeadSHA: "head", Status: "completed", Conclusion: "success"},
				{ID: 2, Attempt: 1, WorkflowID: 1, Event: "pull_request", HeadSHA: "head", Status: "completed", Conclusion: "cancelled"},
			}
			snap := Snapshot{Runs: runs}
			snap.Pull.Head.SHA = "head"
			r := Record{Head: "head"}
			if kind == "CI time" {
				r.CIBudgetHead = "head"
				r.CIBudgetRuns = map[string]string{"2/1": "over CI time budget (cancelled)"}
			} else {
				r.CITestHead = "head"
				r.CITestRuns = map[string]string{"2/1": "tests over 10s: slow"}
			}
			s := &Service{}
			policy := ChangePolicy{CITimeBudget: "enforce"}
			if state, _, _, _ := s.ciStatus(snap, r, policy); state != "failed" {
				t.Fatalf("enforced cancellation CI=%s", state)
			}
			// A further cancelled duplicate cannot hide the saved failure.
			duplicate := runs[1]
			duplicate.ID = 3
			snap.Runs = append(snap.Runs, duplicate)
			if state, _, _, _ := s.ciStatus(snap, r, policy); state != "failed" {
				t.Fatalf("multiple cancellations CI=%s", state)
			}
			// A genuine replacement or a new attempt can replace the failure.
			replacement := runs[0]
			replacement.ID = 4
			snap.Runs = append(snap.Runs, replacement)
			if state, _, _, _ := s.ciStatus(snap, r, policy); state != "passed" {
				t.Fatalf("replacement CI=%s", state)
			}
			snap.Runs = []Run{runs[0], runs[1]}
			snap.Runs[1].Attempt = 2
			if reason, _ := budgetFailure(r, snap, policy); reason != "" {
				t.Fatalf("old attempt still failed: %s", reason)
			}
			snap.Runs = runs
			snap.Pull.Head.SHA = "new-head"
			if reason, _ := budgetFailure(r, snap, policy); reason != "" {
				t.Fatalf("old head still failed: %s", reason)
			}
		})
	}
}

// PR #1132 sat as "CI running" and got a full poll every minute, although its
// CI had ended: the "changes" job was cancelled and every other job skipped.
// These are its stored checks.
func TestCancelledCIFromPR1132IsFinished(t *testing.T) {
	head := "0a125eb960336a67567b8763bcca9cb9b728fcf2"
	snap := Snapshot{Runs: []Run{{ID: 39365801284, CheckSuiteID: 1, WorkflowID: 1, Name: "CI", Attempt: 1, Event: "pull_request", HeadSHA: head, Status: "completed", Conclusion: "cancelled"}}}
	snap.Pull.Head.SHA = head
	for _, c := range []struct {
		id         int64
		name, done string
	}{
		{118442902599, "changes", "cancelled"},
		{118445089360, "computer-smoke", "skipped"},
		{118445089587, "cross-build", "skipped"},
		{118445089175, "frontend", "skipped"},
		{118445089494, "go-test", "skipped"},
		{118445089268, "go-test-group (${{ matrix.group }})", "skipped"},
		{118445088916, "relay", "skipped"},
	} {
		check := Check{ID: c.id, Name: c.name, Status: "completed", Conclusion: c.done}
		check.App.Slug, check.CheckSuite.ID = "github-actions", 1
		snap.Checks = append(snap.Checks, check)
	}
	review := Check{Name: "CodeRabbit", Status: "completed", Conclusion: "success"}
	review.App.Slug = "coderabbitai"
	snap.Checks = append(snap.Checks, review)

	state, key, jobs, runs := snap.CIStatus()
	if state != "failed" || len(jobs) != 0 || len(runs) != 0 {
		t.Fatalf("CI state %q, jobs %v, runs %d; want failed with no failed job", state, jobs, len(runs))
	}
	r := &Record{Key: "pr", Head: head, State: "open", Watching: true, Author: "author", Checks: snap.CheckSummaries(Ref{})}
	s := &Service{}
	s.applyCI(&State{Records: map[string]*Record{"pr": r}}, r, snap, state, key, jobs, nil, false, newPollNotices(r))
	if r.CI != "failed" || r.Failures != 0 || r.CIKey != "" || r.CIReason != "The last CI run was cancelled. Push or rerun CI to test this commit." {
		t.Fatalf("CI %q, failures %d, key %q, reason %q; want a cancelled CI that counts no failure", r.CI, r.Failures, r.CIKey, r.CIReason)
	}
	r.Polling.Error = ""
	if !settledCI(*r) {
		t.Fatal("cancelled CI is not settled, so the PR is polled every minute")
	}
}
