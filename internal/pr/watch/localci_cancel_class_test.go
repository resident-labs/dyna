package watch

import (
	"testing"

	"dyna-agent/internal/localci"
)

// TestHostedCancellationClassification drives both cancellation filters through
// the shared suite classification: a real failure, a cancelled-only aggregate,
// and an aggregate that a saved cancellation leaves active or failed.
func TestHostedCancellationClassification(t *testing.T) {
	for _, tc := range []struct {
		name            string
		status          string
		conclusion      string
		dependency      string
		wantRealFailure bool
		wantAggregate   bool
		// wantSaved and wantGeneral say whether each filter keeps the run. They
		// differ only where a saved cancellation proves the run is ours.
		wantSaved   bool
		wantGeneral bool
	}{
		{
			name:            "real failure under a cancelled run",
			status:          "completed",
			conclusion:      "cancelled",
			dependency:      "failure",
			wantRealFailure: true,
			wantSaved:       true,
			wantGeneral:     true,
		},
		{
			name:          "cancelled-only aggregate",
			status:        "completed",
			conclusion:    "cancelled",
			dependency:    "cancelled",
			wantAggregate: true,
		},
		{
			name:          "active aggregate with saved cancellation",
			status:        "in_progress",
			dependency:    "cancelled",
			wantAggregate: true,
			wantGeneral:   true,
		},
		{
			name:          "failed aggregate with saved cancellation",
			status:        "completed",
			conclusion:    "failure",
			dependency:    "cancelled",
			wantAggregate: true,
			wantGeneral:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := Run{ID: 10, Attempt: 1, CheckSuiteID: 20, Path: ".github/workflows/ci.yml", Status: tc.status, Conclusion: tc.conclusion}
			snap := Snapshot{Runs: []Run{run}, localCIDef: watchDynaDefinition(t)}
			for i, name := range []string{"go-test-shard (0)", "go-static", "frontend", "frontend-check", "go-test"} {
				check := actionsCheck(Check{ID: int64(i + 1), Name: name, Status: "completed", Conclusion: "success"})
				check.CheckSuite.ID = 20
				if i == 0 {
					check.Conclusion = tc.dependency
				}
				if name == "go-test" {
					check.Conclusion = "failure"
				}
				snap.Checks = append(snap.Checks, check)
			}
			class := classifyHostedSuite(watchDynaDefinition(t), snap.currentSuiteChecks(), run.CheckSuiteID, replaceableHostedWorkflow(run))
			if class.realFailure != tc.wantRealFailure || (class.cancelledOnlyAggregate != "") != tc.wantAggregate {
				t.Fatalf("classification = %+v, want real failure %t and aggregate %t", class, tc.wantRealFailure, tc.wantAggregate)
			}
			saved := withoutLocalHostedCancellations(snap, localci.HostedRuns{Attempts: map[int64]int{10: 1}}, watchDynaDefinition(t))
			if kept := len(saved.Runs) == 1; kept != tc.wantSaved {
				t.Fatalf("saved cancellation filter kept the run = %t, want %t", kept, tc.wantSaved)
			}
			if ignored := len(saved.Checks) == 0; ignored != !tc.wantSaved {
				t.Fatalf("saved cancellation filter dropped the suite checks = %t, want %t", ignored, !tc.wantSaved)
			}
			general := withoutCancelledHostedWorkflows(snap)
			if kept := len(general.Runs) == 1; kept != tc.wantGeneral {
				t.Fatalf("general filter kept the run = %t, want %t", kept, tc.wantGeneral)
			}
			if ignored := len(general.Checks) == 0; ignored != !tc.wantGeneral {
				t.Fatalf("general filter dropped the suite checks = %t, want %t", ignored, !tc.wantGeneral)
			}
			if len(snap.Runs) != 1 || len(snap.Checks) != 5 {
				t.Fatal("filter changed input snapshot")
			}
		})
	}
}
