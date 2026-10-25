package watch

import (
	"testing"

	"dyna-agent/internal/localci"
)

// A machine that could not run a check proves nothing about the head, so the run
// moves to another machine once instead of leaving the head pending for evidence
// that will never come. A test failure is evidence about the head, and a retry
// never bounces back to the machine that failed.
func TestMachineFailureMovesTheRunToAnotherMachineOnce(t *testing.T) {
	machineFailure := localci.Result{Node: "arch", Status: localci.OutcomeError, Checks: []localci.SuiteResult{
		{CheckName: "relay", Status: localci.OutcomeError, Cause: localci.CauseContainerFailure},
	}}
	if got := machineFailureRetryFrom(machineFailure); got != "arch" {
		t.Fatalf("retry of a machine failure starts from %q, want arch", got)
	}

	testFailure := machineFailure
	testFailure.Checks = []localci.SuiteResult{{CheckName: "relay", Status: localci.OutcomeFailed, Cause: localci.CauseTestFailure}}
	if got := machineFailureRetryFrom(testFailure); got != "" {
		t.Fatalf("a failing test retried from %q, want no retry", got)
	}

	timeout := machineFailure
	timeout.Cause = localci.CauseTimeout
	timeout.Checks = nil
	if got := machineFailureRetryFrom(timeout); got != "" {
		t.Fatalf("a timeout retried from %q, want no retry", got)
	}

	// A node whose systemd refuses the scope proves no test result at all, which
	// is the same kind of machine fault as a container that never started.
	unproven := localci.Result{Node: "pve", Status: localci.OutcomeError, Checks: []localci.SuiteResult{
		{CheckName: "relay", Status: localci.OutcomeError, Cause: localci.CauseUnclassified},
	}}
	if got := machineFailureRetryFrom(unproven); got != "pve" {
		t.Fatalf("a check that proved no test result started from %q, want pve", got)
	}

	passed := localci.Result{Node: "arch", Status: localci.OutcomePassed}
	if got := machineFailureRetryFrom(passed); got != "" {
		t.Fatalf("a passed result retried from %q, want no retry", got)
	}

	retried := machineFailure
	retried.Node = "hermeslab"
	retried.RetriedFrom = "arch"
	if got := machineFailureRetryFrom(retried); got != "" {
		t.Fatalf("the retry of a machine failure retried again from %q", got)
	}

	// The retry goes to a different machine, and a fleet with no other machine
	// keeps the result instead of running it again in place.
	nodes := []localci.Node{{Name: "arch", Slots: 12, Enabled: true}, {Name: "hermeslab", Slots: 16, Enabled: true}}
	chosen := availableCINodeExcept(nodes, map[string]int{}, "", machineFailureRetryFrom(machineFailure))
	if chosen == nil || chosen.Name != "hermeslab" {
		t.Fatalf("retry chose %+v, want hermeslab", chosen)
	}
	if chosen := availableCINodeExcept(nodes[:1], map[string]int{}, "", "arch"); chosen != nil {
		t.Fatalf("a fleet of one machine retried on itself: %+v", chosen)
	}
}

// A suite group adds a derived row with the group's status and no cause of its
// own. Only its members prove why they failed, so a grouped machine failure must
// still retry on another machine.
func TestGroupedMachineFailureRetriesOnAnotherMachine(t *testing.T) {
	grouped := localci.Result{Node: "arch", Status: localci.OutcomeError, Checks: []localci.SuiteResult{
		{CheckName: "relay", Status: localci.OutcomeError, Cause: localci.CauseContainerFailure, Group: "go-test"},
		{CheckName: "go-test", Status: localci.OutcomeError, Aggregate: true, Group: "go-test"},
	}}
	if got := machineFailureRetryFrom(grouped); got != "arch" {
		t.Fatalf("a grouped machine failure started from %q, want arch", got)
	}

	failedGroup := grouped
	failedGroup.Checks = []localci.SuiteResult{
		{CheckName: "relay", Status: localci.OutcomeFailed, Cause: localci.CauseTestFailure, Group: "go-test"},
		{CheckName: "go-test", Status: localci.OutcomeFailed, Aggregate: true, Group: "go-test"},
	}
	if got := machineFailureRetryFrom(failedGroup); got != "" {
		t.Fatalf("a grouped test failure retried from %q, want no retry", got)
	}
}
