package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/localci"
)

// expectedChecks is this test's own statement of the suite graph: the check names
// each suite must report, with the go suite's eight shards spelled out. It is a
// literal list and never a read of the description, so a check or a suite the
// description loses fails here instead of agreeing with itself.
var expectedChecks = map[localci.Suite][]localci.CheckName{
	localci.SuiteRelay:         {"relay"},
	localci.SuiteFrontend:      {"frontend"},
	localci.SuiteFrontendCheck: {"frontend-check"},
	localci.SuiteGoStatic:      {"go-static"},
	localci.SuiteCrossBuild:    {"cross-build"},
	localci.SuiteComputerSmoke: {"computer-smoke"},
	localci.SuiteGo: {
		"frontend", "frontend-check", "go-static", "go-test",
		"go-test-shard (0)", "go-test-shard (1)", "go-test-shard (2)", "go-test-shard (3)",
		"go-test-shard (4)", "go-test-shard (5)", "go-test-shard (6)", "go-test-shard (7)",
	},
}

// expectedTrials is the suite set a merge trial must run.
var expectedTrials = []localci.Suite{
	localci.SuiteRelay, localci.SuiteGo, localci.SuiteCrossBuild, localci.SuiteComputerSmoke,
}

// reportedChecks is what a run of one suite leaves in a saved result: the name of
// every check the suite expands into, plus the aggregate each one feeds.
func reportedChecks(t *testing.T, suite localci.Suite) []localci.CheckName {
	t.Helper()
	checks, ok := localci.Checks(suite)
	if !ok || len(checks) == 0 {
		t.Fatalf("suite %q has no executable checks", suite)
	}
	var names []localci.CheckName
	add := func(name localci.CheckName) {
		for _, have := range names {
			if have == name {
				return
			}
		}
		names = append(names, name)
	}
	for _, check := range checks {
		add(check.Name)
		if check.Group != "" {
			add(check.Group.Name())
		}
	}
	return names
}

func passedResult(names []localci.CheckName) localci.Result {
	result := localci.Result{Node: "node-a", Status: localci.OutcomePassed}
	for _, name := range names {
		result.Checks = append(result.Checks, localci.SuiteResult{CheckName: name, Status: localci.OutcomePassed})
	}
	return result
}

// withoutNames copies names without one check, so a caller can prove the gate
// rejects an incomplete result.
func withoutNames(names []localci.CheckName, drop localci.CheckName) []localci.CheckName {
	out := make([]localci.CheckName, 0, len(names))
	for _, name := range names {
		if name != drop {
			out = append(out, name)
		}
	}
	return out
}

// sameSet compares two lists as multisets: order does not matter.
func sameSet[T comparable](got, want []T) bool {
	if len(got) != len(want) {
		return false
	}
	left := make(map[T]int, len(got))
	for _, value := range got {
		left[value]++
	}
	for _, value := range want {
		if left[value] == 0 {
			return false
		}
		left[value]--
	}
	return true
}

// TestOneSuiteDescriptionDrivesExecutionGateAndTrial holds the suite graph to the
// literal list above. The description must name those suites and no others, the
// runner must report exactly those check names, the merge gate must demand every
// one of them, and trial selection must run those suites. A check that leaves the
// description therefore fails this test in the runner comparison and in the gate
// comparison, not only in the code that dropped it.
func TestOneSuiteDescriptionDrivesExecutionGateAndTrial(t *testing.T) {
	described := localci.Suites()
	for _, suite := range described {
		if _, ok := expectedChecks[suite]; !ok {
			t.Fatalf("the description names suite %q, which this test does not expect", suite)
		}
	}
	for suite := range expectedChecks {
		if !containsSuite(described, suite) {
			t.Fatalf("this test expects suite %q, which the description does not name", suite)
		}
	}
	for suite, want := range expectedChecks {
		if got := reportedChecks(t, suite); !sameSet(got, want) {
			t.Fatalf("suite %q reports %v, want %v", suite, got, want)
		}
		suites := []localci.Suite{suite}
		if !localci.CoversSuites(passedResult(want), suites) {
			t.Fatalf("suite %q: the gate rejected a result holding every expected check", suite)
		}
		for _, name := range want {
			if localci.CoversSuites(passedResult(withoutNames(want, name)), suites) {
				t.Fatalf("suite %q: the gate accepted a result without %q", suite, name)
			}
		}
	}
	if got := localci.WorkflowSuites(); !sameSet(got, expectedTrials) {
		t.Fatalf("trial selection runs %v, want %v", got, expectedTrials)
	}
	// The merge gate reaches the same graph through its own entry point: a
	// complete trial passes, and the same trial without one check waits.
	service := &Service{}
	job := registeredCIJob{trial: true, head: "0000000000000000000000000000000000000000"}
	var trial []localci.CheckName
	for _, suite := range expectedTrials {
		trial = append(trial, expectedChecks[suite]...)
	}
	if !service.registeredJobResultComplete(context.Background(), job, passedResult(trial)) {
		t.Fatal("a complete trial result was rejected")
	}
	for _, name := range trial {
		if service.registeredJobResultComplete(context.Background(), job, passedResult(withoutNames(trial, name))) {
			t.Fatalf("a trial result without %q passed the gate", name)
		}
	}
}

func containsSuite(suites []localci.Suite, want localci.Suite) bool {
	for _, suite := range suites {
		if suite == want {
			return true
		}
	}
	return false
}
