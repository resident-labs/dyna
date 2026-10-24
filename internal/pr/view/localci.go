package view

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/pr/watch"
)

// RunningCI lists the local CI runs in flight on each machine. The runs live in
// the engine's worker process, so this reads the set that scheduler owns.
func (s *Service) RunningCI() []watch.RunningCI { return s.Watcher().RunningCI() }

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
		name := string(suite.Name())
		check := watch.CheckSummary{Name: name, Node: suite.Node, Status: "completed", DurationMs: suite.Duration.Milliseconds()}
		switch suite.Status {
		case localci.OutcomePassed:
			check.State = "success"
			check.Conclusion = "success"
		case localci.OutcomeFailed, localci.OutcomeError:
			check.State = "failure"
			check.Conclusion = "failure"
		default:
			check.State = "pending"
			check.Status = "in_progress"
		}
		if check.State == "failure" {
			failures := strings.Join(suite.Failures, "\n")
			if failures == "" {
				failures = suite.Error
			}
			excerpt := failures + "\n\n" + suite.LogTail
			// The check name stays the name the provider and the merge gate
			// know; the cause belongs to the failure text, where a person
			// reads why the check did not test the change.
			if label := suite.Cause.Label(); label != "" {
				excerpt = "Cause: " + label + "\n\n" + excerpt
			}
			check.FailureExcerpt = strings.TrimSpace(excerpt)
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
		excerpt := result.Error
		if label := result.Cause.Label(); label != "" {
			excerpt = "Cause: " + label + "\n\n" + excerpt
		}
		record.Checks = append(record.Checks, watch.CheckSummary{
			Name: "local CI", State: "failure", Status: "completed", Conclusion: "failure",
			DurationMs: result.Duration.Milliseconds(), FailureExcerpt: excerpt, FailureExcerptChecked: true,
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
