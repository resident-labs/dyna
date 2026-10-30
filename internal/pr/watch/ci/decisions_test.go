package ci

import (
	"strings"
	"testing"

	"dyna-agent/internal/localci"
	"dyna-agent/internal/machines"
)

func stoppedRun() localci.Result {
	return localci.Result{
		Repo: "repo", SHA: strings.Repeat("a", 40), Node: "pve", Status: localci.OutcomeError,
		Cause: localci.CauseEngineStop, Error: "local CI stopped because the engine is restarting",
		Checks: []localci.SuiteResult{
			{Suite: "fmt", CheckName: "fmt", Node: "pve", Status: localci.OutcomeError, Cause: localci.CauseEngineStop, Error: "check stopped before its reports were read"},
			{Suite: "test-core", CheckName: "test-core", Node: "pve", Status: localci.OutcomeError, Cause: localci.CauseCancelled, Error: "context canceled"},
		},
	}
}

// A machine that could not run a check proves nothing about the head, so the run
// moves to another machine once instead of leaving the head pending for evidence
// that will never come. A test failure is evidence about the head, and a retry
// never bounces back to the machine that failed.
func TestMachineFailureMovesTheRunToAnotherMachineOnce(t *testing.T) {
	machineFailure := localci.Result{Node: "arch", Status: localci.OutcomeError, Checks: []localci.SuiteResult{
		{CheckName: "relay", Status: localci.OutcomeError, Cause: localci.CauseContainerFailure},
	}}
	if got := MachineFailureRetryFrom(machineFailure); got != "arch" {
		t.Fatalf("retry of a machine failure starts from %q, want arch", got)
	}

	testFailure := machineFailure
	testFailure.Checks = []localci.SuiteResult{{CheckName: "relay", Status: localci.OutcomeFailed, Cause: localci.CauseTestFailure}}
	if got := MachineFailureRetryFrom(testFailure); got != "" {
		t.Fatalf("a failing test retried from %q, want no retry", got)
	}

	timeout := machineFailure
	timeout.Cause = localci.CauseTimeout
	timeout.Checks = nil
	if got := MachineFailureRetryFrom(timeout); got != "" {
		t.Fatalf("a timeout retried from %q, want no retry", got)
	}

	// A node whose systemd refuses the scope proves no test result at all, which
	// is the same kind of machine fault as a container that never started.
	unproven := localci.Result{Node: "pve", Status: localci.OutcomeError, Checks: []localci.SuiteResult{
		{CheckName: "relay", Status: localci.OutcomeError, Cause: localci.CauseUnclassified},
	}}
	if got := MachineFailureRetryFrom(unproven); got != "pve" {
		t.Fatalf("a check that proved no test result started from %q, want pve", got)
	}

	passed := localci.Result{Node: "arch", Status: localci.OutcomePassed}
	if got := MachineFailureRetryFrom(passed); got != "" {
		t.Fatalf("a passed result retried from %q, want no retry", got)
	}

	retried := machineFailure
	retried.Node = "hermeslab"
	retried.RetriedFrom = "arch"
	if got := MachineFailureRetryFrom(retried); got != "" {
		t.Fatalf("the retry of a machine failure retried again from %q", got)
	}

	// The retry goes to a different machine, and a fleet with no other machine
	// keeps the result instead of running it again in place.
	nodes := []localci.Node{{Name: "arch", Enabled: true, Labels: []string{machines.LabelCI}}, {Name: "hermeslab", Enabled: true, Labels: []string{machines.LabelCI}}}
	free := func(node localci.Node) int {
		if node.Name == "arch" {
			return 12
		}
		return 16
	}
	chosen := AvailableCINode(nodes, "", MachineFailureRetryFrom(machineFailure), 0, free)
	if chosen == nil || chosen.Name != "hermeslab" {
		t.Fatalf("retry chose %+v, want hermeslab", chosen)
	}
	if chosen := AvailableCINode(nodes[:1], "", "arch", 0, free); chosen != nil {
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
	if got := MachineFailureRetryFrom(grouped); got != "arch" {
		t.Fatalf("a grouped machine failure started from %q, want arch", got)
	}

	failedGroup := grouped
	failedGroup.Checks = []localci.SuiteResult{
		{CheckName: "relay", Status: localci.OutcomeFailed, Cause: localci.CauseTestFailure, Group: "go-test"},
		{CheckName: "go-test", Status: localci.OutcomeFailed, Aggregate: true, Group: "go-test"},
	}
	if got := MachineFailureRetryFrom(failedGroup); got != "" {
		t.Fatalf("a grouped test failure retried from %q, want no retry", got)
	}
}

func TestRegisteredCIPrefersFreeCapacity(t *testing.T) {
	nodes := []localci.Node{
		{Name: "local", Enabled: true},
		{Name: "hermeslab", Enabled: true},
		{Name: "disabled"},
	}
	for _, tc := range []struct {
		free map[string]int
		want string
	}{
		{map[string]int{"local": 1, "hermeslab": 8}, "hermeslab"},
		{map[string]int{"local": 1, "hermeslab": 7}, "hermeslab"},
		{map[string]int{"local": 1, "hermeslab": 1}, "local"},
		{map[string]int{"local": 0, "hermeslab": 0}, ""},
	} {
		node := AvailableCINode(nodes, "", "", 0, func(n localci.Node) int { return tc.free[n.Name] })
		got := ""
		if node != nil {
			got = node.Name
		}
		if got != tc.want {
			t.Fatalf("free=%v: chose %q, want %q", tc.free, got, tc.want)
		}
	}
}

// A disk-blocked node admits no work, so it never wins the choice against a
// node that can admit work, however free its CPUs read: work must not queue
// behind a disk rule while a healthy node has room.
func TestRegisteredCISkipsADiskBlockedNodeForANodeThatCanAdmit(t *testing.T) {
	blocked := localci.Node{Name: "blocked", Enabled: true, CPUs: 8, Disk: machines.Disk{Total: 100 << 30, Free: 1 << 30}, MaxDiskPercent: 10}
	healthy := localci.Node{Name: "healthy", Enabled: true, CPUs: 2}
	free := func(n localci.Node) int {
		if n.Name == "healthy" {
			return 1
		}
		return 8
	}
	// A pull request head needs a node with room, and the blocked node reads
	// as the roomiest: the healthy node still wins.
	if node := AvailableCINode([]localci.Node{blocked, healthy}, "", "", 0, free); node == nil || node.Name != "healthy" {
		t.Fatalf("pull request head chose %+v, want the healthy node", node)
	}
	// A trial joins a queue even on a full node, but not behind a disk rule
	// while a healthy node can take the work.
	if node := AvailableCINode([]localci.Node{blocked, healthy}, "", "", -1, free); node == nil || node.Name != "healthy" {
		t.Fatalf("trial chose %+v, want the healthy node", node)
	}
	// A fleet with no healthy node still takes the blocked node, so the trial
	// waits in its queue with the disk reason instead of waiting for a tick.
	if node := AvailableCINode([]localci.Node{blocked}, "", "", -1, free); node == nil || node.Name != "blocked" {
		t.Fatalf("trial chose %+v, want the blocked node as the only choice", node)
	}
	// A blocked node and a healthy but full node both have no room; the tie
	// must not depend on node order, or a trial would wait behind a disk rule
	// while the healthy node frees instead of joining its queue.
	for _, order := range [][]localci.Node{{blocked, healthy}, {healthy, blocked}} {
		if node := AvailableCINode(order, "", "", -1, func(localci.Node) int { return 0 }); node == nil || node.Name != "healthy" {
			t.Fatalf("trial over a blocked and a full node chose %+v, want the healthy node", node)
		}
	}
}

// A machine that names rooms takes only those rooms' work, so the scheduler and
// the merge trials that share it leave it to the room that owns it.
func TestRegisteredCISkipsMachinesLimitedToOtherRooms(t *testing.T) {
	nodes := []localci.Node{
		{Name: "hermeslab", Enabled: true, Rooms: []string{"room_dyna"}},
		{Name: "pve", Enabled: true, Rooms: []string{"room_fl-rea"}},
		{Name: "arch", Enabled: true},
	}
	free := func(localci.Node) int { return 4 }
	for _, tc := range []struct {
		room string
		full string
		want string
	}{
		{"room_dyna", "", "hermeslab"},
		{"room_fl-rea", "", "pve"},
		// A full machine of the room hands work to a machine with no limit
		// before another room's machine.
		{"room_dyna", "hermeslab", "arch"},
		// A machine with no room list takes every room's work.
		{"room_other", "", "arch"},
	} {
		node := AvailableCINode(nodes, tc.room, "", 0, func(n localci.Node) int {
			if n.Name == tc.full {
				return 0
			}
			return free(n)
		})
		got := ""
		if node != nil {
			got = node.Name
		}
		if got != tc.want {
			t.Fatalf("room=%s full=%q: chose %q, want %q", tc.room, tc.full, got, tc.want)
		}
	}
	// A room that no machine allows waits for one, and takes no other room's
	// machine.
	if node := AvailableCINode(nodes[:2], "room_other", "", 0, free); node != nil {
		t.Fatalf("room with no machine chose %q", node.Name)
	}
}

// A local run that Dyna itself stopped says nothing about the commit, so it runs
// again on the same machine, and the marker bounds that to one retry.
func TestStoppedLocalRunIsRetriedOnce(t *testing.T) {
	stopped := stoppedRun()
	if act := SavedResultActionFor(stopped); act != RetryStoppedRun {
		t.Fatalf("action = %v, want RetryStoppedRun", act)
	}
	stopped.Resumed = true
	if act := SavedResultActionFor(stopped); act != SettleSavedResult {
		t.Fatalf("second interruption action = %v, want SettleSavedResult", act)
	}
	// A machine failure keeps its own rule: it moves to another machine.
	machine := stoppedRun()
	machine.Cause = localci.CauseContainerFailure
	for i := range machine.Checks {
		machine.Checks[i].Cause = localci.CauseContainerFailure
	}
	if act := SavedResultActionFor(machine); act != RetryOnAnotherMachine {
		t.Fatalf("machine failure action = %v, want RetryOnAnotherMachine", act)
	}
	// A test failure is evidence about the commit and never runs again.
	failed := stoppedRun()
	failed.Status = localci.OutcomeFailed
	failed.Cause = localci.CauseTestFailure
	failed.Checks = []localci.SuiteResult{{CheckName: "fmt", Status: localci.OutcomeFailed, Cause: localci.CauseTestFailure}}
	if act := SavedResultActionFor(failed); act != SettleSavedResult {
		t.Fatalf("test failure action = %v, want SettleSavedResult", act)
	}
}
