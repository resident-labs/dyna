package view

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/watch"
)

// localCIChecks projects stored local results into the existing check list.
func localCIChecks(facts watch.ReviewFacts, record watch.Record) watch.Record {
	if record.Kind != "local" || record.RepoPath == "" || record.Head == "" {
		return record
	}
	result, err := (localci.Runner{}).Show(record.RepoPath, record.Head)
	if err != nil {
		return record
	}
	record.Checks = make([]watch.CheckSummary, 0, len(result.Suites))
	max, _, budgetErr := watch.EffectivePolicy(facts.State, record).TestBudget()
	var slow []localci.TestTiming
	for _, suite := range result.Suites {
		name := suite.CheckName
		if name == "" {
			name = suite.Suite
		}
		check := watch.CheckSummary{Name: name, Node: suite.Node, Status: "completed", DurationMs: suite.Duration.Milliseconds()}
		switch suite.Status {
		case "passed":
			check.State = "success"
			check.Conclusion = "success"
		case "failed", "error":
			check.State = "failure"
			check.Conclusion = "failure"
		case "skipped":
			check.State = "skipped"
			check.Conclusion = "skipped"
		default:
			check.State = "pending"
			check.Status = "in_progress"
		}
		if check.State == "failure" {
			failures := strings.Join(suite.Failures, "\n")
			if failures == "" {
				failures = suite.Error
			}
			check.FailureExcerpt = strings.TrimSpace(failures + "\n\n" + suite.LogTail)
			check.FailureExcerptChecked = true
		}
		record.Checks = append(record.Checks, check)
		if budgetErr == nil {
			for _, test := range suite.SlowTests {
				if test.Duration > max {
					slow = append(slow, test)
				}
			}
		}
	}
	if result.Error != "" {
		record.Checks = append(record.Checks, watch.CheckSummary{
			Name: "local CI", State: "failure", Status: "completed", Conclusion: "failure",
			DurationMs: result.Duration.Milliseconds(), FailureExcerpt: result.Error, FailureExcerptChecked: true,
		})
	}
	record.CITestReportNote = ""
	if len(slow) > 0 {
		sort.SliceStable(slow, func(i, j int) bool {
			if slow[i].Duration == slow[j].Duration {
				return slow[i].Name < slow[j].Name
			}
			return slow[i].Duration > slow[j].Duration
		})
		names := make([]string, 0, len(slow))
		for _, test := range slow {
			names = append(names, fmt.Sprintf("%s (%s)", test.Name, test.Duration.Round(time.Millisecond)))
		}
		record.CITestReportNote = fmt.Sprintf("Slow tests over the %s budget: %s", max, strings.Join(names, ", "))
	}
	return record
}
