package watch

import "testing"

// Run 39625369989 and all five of its jobs skipped every step.
func TestSkippedJobsCannotEstablishPassingCI(t *testing.T) {
	checks := []Check{}
	for i, name := range []string{"changes", "frontend", "go-test", "relay", "computer-smoke"} {
		check := actionsCheck(Check{ID: int64(i + 1), Name: name, Status: "completed", Conclusion: "skipped"})
		check.CheckSuite.ID = 1
		checks = append(checks, check)
	}
	for _, tc := range []struct {
		name string
		snap Snapshot
		want string
	}{
		{"checks only", Snapshot{Checks: checks}, "pending"},
		{"successful run with skipped jobs", Snapshot{Checks: checks, Runs: []Run{{ID: 39625369989, CheckSuiteID: 1, WorkflowID: 1, Status: "completed", Conclusion: "success"}}}, "pending"},
		{"skipped run alone", Snapshot{Runs: []Run{{ID: 1, WorkflowID: 1, Status: "completed", Conclusion: "skipped"}}}, "pending"},
		{"successful run alone", Snapshot{Runs: []Run{{ID: 1, WorkflowID: 1, Status: "completed", Conclusion: "success"}}}, "passed"},
		{"real passing check with optional skips", Snapshot{Checks: append(append([]Check{}, checks...), actionsCheck(Check{ID: 10, Name: "unit", Status: "completed", Conclusion: "success"}))}, "passed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, _ := tc.snap.CIStatus()
			if got != tc.want {
				t.Fatalf("CI=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestSkippedSuiteDoesNotBorrowOrBlockOtherWorkflowResults(t *testing.T) {
	skipped := actionsCheck(Check{ID: 1, Name: "optional", Status: "completed", Conclusion: "skipped"})
	skipped.CheckSuite.ID = 1
	passing := actionsCheck(Check{ID: 3, Status: "completed", Conclusion: "success"})
	passing.CheckSuite.ID = 10
	failed := skipped
	failed.ID, failed.Name, failed.Conclusion = 2, "unit", "failure"
	for _, tc := range []struct {
		name string
		snap Snapshot
		want string
	}{
		{"other workflow really passed", Snapshot{Checks: []Check{skipped}, Runs: []Run{{ID: 1, WorkflowID: 1, CheckSuiteID: 1, Status: "completed", Conclusion: "success"}, {ID: 2, WorkflowID: 2, CheckSuiteID: 2, Status: "completed", Conclusion: "success"}}}, "passed"},
		{"failed work remains failed", Snapshot{Checks: []Check{skipped, failed}}, "failed"},
		{"new skipped attempt replaces old passing suite", Snapshot{Checks: []Check{passing, skipped}, Runs: []Run{{ID: 1, WorkflowID: 1, CheckSuiteID: 10, Status: "completed", Conclusion: "success"}, {ID: 2, WorkflowID: 1, CheckSuiteID: 1, Status: "completed", Conclusion: "skipped"}}}, "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, _ := tc.snap.CIStatus()
			if got != tc.want {
				t.Fatalf("CI=%s, want %s", got, tc.want)
			}
		})
	}
}

func TestSuiteExecutionSurvivesDuplicateJobNames(t *testing.T) {
	passed := actionsCheck(Check{ID: 1, Name: "test", Status: "completed", Conclusion: "success"})
	passed.CheckSuite.ID = 1
	optional := actionsCheck(Check{ID: 2, Name: "optional", Status: "completed", Conclusion: "skipped"})
	optional.CheckSuite.ID = 1
	skipped := actionsCheck(Check{ID: 3, Name: "test", Status: "completed", Conclusion: "skipped"})
	skipped.CheckSuite.ID = 2
	snap := Snapshot{
		Checks: []Check{passed, optional, skipped},
		Runs: []Run{
			{ID: 1, WorkflowID: 1, CheckSuiteID: 1, Status: "completed", Conclusion: "success"},
			{ID: 2, WorkflowID: 2, CheckSuiteID: 2, Status: "completed", Conclusion: "success"},
		},
	}
	got, passedKey, _, _ := snap.CIStatus()
	if got != "passed" {
		t.Fatalf("CI=%s, want passed: the first workflow executed its test", got)
	}
	// This job is hidden by name deduplication, but its result affects the gate.
	snap.Checks[0].Conclusion = "skipped"
	got, skippedKey, _, _ := snap.CIStatus()
	if got != "pending" {
		t.Fatalf("CI=%s, want pending: both current suites skipped their jobs", got)
	}
	if passedKey == skippedKey {
		t.Fatal("CI key did not change when suite execution evidence changed")
	}
	snap.Checks[0] = passed
	// The same suite's newer job must replace its earlier passing attempt.
	replaced := passed
	replaced.ID, replaced.Conclusion = 4, "skipped"
	snap.Checks = append(snap.Checks, replaced)
	got, _, _, _ = snap.CIStatus()
	if got != "pending" {
		t.Fatalf("CI=%s, want pending: a newer job replaced the passing attempt", got)
	}
}
