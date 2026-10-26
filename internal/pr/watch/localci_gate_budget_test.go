package watch

import (
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/changepolicy"
	"dyna-agent/internal/localci"
)

// The room's ciTimeBudget decides whether a test over the per-test limit fails
// the check. On warn it is reported with its typed cause and the check keeps
// passing when every test passed; on enforce it fails as before. The limit
// itself never changes.
func TestPerTestBudgetFollowsTheRoomMode(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want localci.Outcome
	}{
		{"warn", localci.OutcomePassed},
		{"enforce", localci.OutcomeFailed},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			policy := changepolicy.Policy{CITimeBudget: tc.mode, CITestMax: "2s"}
			result := localci.Result{Node: "hermeslab", Status: localci.OutcomePassed, Checks: []localci.SuiteResult{{
				Suite: "go-test-shard-0", CheckName: "go-test-shard (0)", Status: localci.OutcomePassed,
				Cause:     localci.CauseOverBudget,
				SlowTests: []localci.TestTiming{{Name: "dyna-agent/internal/x.TestSlow", Duration: 2500 * time.Millisecond}},
			}}}
			note := applyLocalBudgets(&result, policy)
			if !strings.Contains(note, "the test limit is 2s") {
				t.Fatalf("no notice for the slow test: %q", note)
			}
			check := result.Checks[0]
			if check.Status != tc.want || result.Status != tc.want {
				t.Fatalf("check=%s result=%s, want %s", check.Status, result.Status, tc.want)
			}
			if tc.want == localci.OutcomePassed {
				if check.Error != "" {
					t.Fatalf("a passing check carries an error: %q", check.Error)
				}
				if check.Cause != localci.CauseOverBudget {
					t.Fatalf("the typed cause was lost: %q", check.Cause)
				}
			}
		})
	}
}
