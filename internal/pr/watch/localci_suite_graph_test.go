package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/localci"
)

// executedChecks is what the runner reports for one suite: every check the suite
// expands into plus the aggregate each check feeds, which is what a saved result
// holds after a run.
func executedChecks(t *testing.T, suite localci.Suite) []localci.CheckName {
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

// TestOneSuiteDescriptionDrivesExecutionGateAndTrial holds the suite graph to one
// description. For every suite the description names, the gate demands exactly the
// checks and aggregates the runner reports for that suite, and it accepts nothing
// less. Trial selection only names suites the description knows, so a suite added
// to the description reaches execution, the gate and trial selection together,
// instead of needing an edit in each place.
func TestOneSuiteDescriptionDrivesExecutionGateAndTrial(t *testing.T) {
	described := map[localci.Suite]bool{}
	for _, suite := range localci.Suites() {
		described[suite] = true
		executed := executedChecks(t, suite)
		suites := []localci.Suite{suite}
		if !localci.CoversSuites(passedResult(executed), suites) {
			t.Fatalf("suite %q: the gate rejected the checks the runner reports for it", suite)
		}
		// Every check the runner reports is one the gate insists on.
		for i := range executed {
			partial := append(append([]localci.CheckName{}, executed[:i]...), executed[i+1:]...)
			if localci.CoversSuites(passedResult(partial), suites) {
				t.Fatalf("suite %q: the gate accepted a result without %q", suite, executed[i])
			}
		}
	}
	for _, suite := range localci.WorkflowSuites() {
		if !described[suite] {
			t.Fatalf("trial selection runs suite %q, which the description does not name", suite)
		}
	}
	for _, suite := range localci.DefaultSuites() {
		if !described[suite] {
			t.Fatalf("default selection runs suite %q, which the description does not name", suite)
		}
	}
	// The merge gate reaches the same description through its own entry point: a
	// complete trial passes, and the same trial without one check waits.
	service := &Service{}
	job := registeredCIJob{trial: true, head: "0000000000000000000000000000000000000000"}
	var trial []localci.CheckName
	for _, suite := range localci.WorkflowSuites() {
		trial = append(trial, executedChecks(t, suite)...)
	}
	if !service.registeredJobResultComplete(context.Background(), job, passedResult(trial)) {
		t.Fatal("a complete trial result was rejected")
	}
	for i := range trial {
		partial := append(append([]localci.CheckName{}, trial[:i]...), trial[i+1:]...)
		if service.registeredJobResultComplete(context.Background(), job, passedResult(partial)) {
			t.Fatalf("a trial result without %q passed the gate", trial[i])
		}
	}
}
