package pr

import (
	"strings"
	"testing"

	prview "dyna-agent/internal/pr/view"
)

func TestFormatHealthReadsAsAList(t *testing.T) {
	report := prview.CIHealth{
		Room: "github-fixture", LimitMs: 1000, WarnMs: 180000, FailMs: 240000,
		Runs: []prview.CIHealthRun{
			{Token: "1241/1", PR: 1902, Workflow: "CI", Status: "completed", Conclusion: "success", BusyMs: 96000, QueueMs: 12000, WarnMs: 180000, FailMs: 240000},
			{Token: "1250/1", PR: 1903, Workflow: "CI", Status: "completed", Conclusion: "failure", BusyMs: 250000, WarnMs: 180000, FailMs: 240000},
		},
		Tests: []prview.CIHealthTest{
			{Test: "internal/pr/watch/TestSlow", Name: "TestSlow", Package: "internal/pr/watch", Features: []string{"prs"},
				LimitMs: 1000, LastMs: 34200, TypicalMs: 31000, Breaches: 3, State: "recurring"},
		},
	}
	text := formatHealth(report)
	for _, want := range []string{
		"CI health · github-fixture · test limit 1s · warn 3m0s · fail 4m0s",
		"runs 2 · tests tracked 1 · over the limit 1",
		"#1902 success · 1m36s busy",
		"1m36s busy, 12s waiting, budget within",
		"#1903 failure · 4m10s busy, budget over fail",
		"over fail",
		"TestSlow · internal/pr/watch · prs · last 34.2s, typical 31s · 3 breaches · recurring",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report lacks %q:\n%s", want, text)
		}
	}
	empty := formatHealth(prview.CIHealth{Room: "r", LimitMs: 1000})
	if !strings.Contains(empty, "No CI runs recorded yet") {
		t.Fatalf("empty report: %q", empty)
	}
}
