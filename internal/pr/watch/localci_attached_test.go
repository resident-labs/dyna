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
// even when the scheduler counts that machine full, and waits when the machine is
// only low on disk. Any other machine would start the checks again.
func TestRunIsPinnedToTheMachineThatHoldsItsChecks(t *testing.T) {
	nodes := []localci.Node{
		{Name: "hermeslab", Slots: 8, Enabled: true},
		{Name: "pve", Slots: 2, Enabled: true},
	}
	pin := func(registry localCIRegistry, skip string, nodes []localci.Node) (string, bool) {
		node, wait := pinnedCINode(t.Context(), registry, "/repo", "abc", nodes, "room", skip)
		if node == nil {
			return "", wait
		}
		return node.Name, wait
	}
	if got, wait := pin(attachedRegistry{node: "pve"}, "", nodes); got != "pve" || wait {
		t.Fatalf("pinned %q wait %v, want pve", got, wait)
	}
	if got, wait := pin(attachedRegistry{}, "", nodes); got != "" || wait {
		t.Fatalf("without a record pinned %q wait %v", got, wait)
	}
	if got, wait := pin(attachedRegistry{node: "pve"}, "pve", nodes); got != "" || wait {
		t.Fatalf("after a failure there pinned %q wait %v", got, wait)
	}
	low := []localci.Node{nodes[0], {Name: "pve", Slots: 2, Enabled: true, LowDisk: true}}
	if got, wait := pin(attachedRegistry{node: "pve"}, "", low); got != "" || !wait {
		t.Fatalf("on a low disk pinned %q wait %v, want to wait", got, wait)
	}
	gone := []localci.Node{nodes[0]}
	if got, wait := pin(attachedRegistry{node: "pve"}, "", gone); got != "" || wait {
		t.Fatalf("with the machine removed pinned %q wait %v, want the normal choice", got, wait)
	}
}
