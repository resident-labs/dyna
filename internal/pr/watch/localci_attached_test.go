package watch

import (
	"context"
	"testing"

	"dyna-agent/internal/localci"
)

// attachedRegistry answers which machine holds a commit's checks.
type attachedRegistry struct {
	localCIRegistry
	node string
}

func (r attachedRegistry) AttachedNode(context.Context, string, string) (string, error) {
	return r.node, nil
}

// After an engine restart a run goes to the machine that still runs its checks,
// not to the machine with the most free slots, which would start them again.
func TestRunGoesToTheMachineThatHoldsItsChecks(t *testing.T) {
	nodes := []localci.Node{
		{Name: "hermeslab", Slots: 8, Enabled: true},
		{Name: "pve", Slots: 2, Enabled: true},
	}
	active := map[string]int{}
	roomy := availableCINodeExcept(nodes, active, "room", "")
	if roomy.Name != "hermeslab" {
		t.Fatalf("fixture: the roomy machine is %s", roomy.Name)
	}
	pick := func(registry localCIRegistry, skip string, active map[string]int) string {
		chosen := preferAttachedCINode(t.Context(), registry, "/repo", "abc", nodes, active, "room", skip, roomy)
		return chosen.Name
	}
	if got := pick(attachedRegistry{node: "pve"}, "", active); got != "pve" {
		t.Fatalf("placed on %s, want the machine that holds the checks", got)
	}
	// No record, a machine with no free slot, or a machine the run just failed on
	// keeps the normal choice.
	if got := pick(attachedRegistry{}, "", active); got != "hermeslab" {
		t.Fatalf("without a record placed on %s", got)
	}
	if got := pick(attachedRegistry{node: "pve"}, "", map[string]int{"pve": 2}); got != "hermeslab" {
		t.Fatalf("with a full machine placed on %s", got)
	}
	if got := pick(attachedRegistry{node: "pve"}, "pve", active); got != "hermeslab" {
		t.Fatalf("after a failure there, placed on %s", got)
	}
}
