package ci

import (
	"context"
	"testing"

	"dyna-agent/internal/localci"
)

// attachedRegistry answers which machine holds a commit's checks.
type attachedRegistry struct {
	Registry
	node string
}

func (r attachedRegistry) AttachedNode(context.Context, string, string) (string, error) {
	return r.node, nil
}

// After an engine restart a run goes to the machine that still runs its checks,
// even when the scheduler counts that machine full or its disk is blocked: the
// checks then wait in its queue. Any other machine would start the checks again.
func TestRunIsPinnedToTheMachineThatHoldsItsChecks(t *testing.T) {
	nodes := []localci.Node{
		{Name: "hermeslab", Enabled: true},
		{Name: "pve", Enabled: true},
	}
	pin := func(registry Registry, skip string, nodes []localci.Node) string {
		node := PinnedCINode(t.Context(), registry, "/repo", "abc", nodes, "room", skip)
		if node == nil {
			return ""
		}
		return node.Name
	}
	if got := pin(attachedRegistry{node: "pve"}, "", nodes); got != "pve" {
		t.Fatalf("pinned %q, want pve", got)
	}
	if got := pin(attachedRegistry{}, "", nodes); got != "" {
		t.Fatalf("without a record pinned %q, want the normal choice", got)
	}
	if got := pin(attachedRegistry{node: "pve"}, "pve", nodes); got != "" {
		t.Fatalf("after a failure there pinned %q, want the normal choice", got)
	}
	gone := []localci.Node{nodes[0]}
	if got := pin(attachedRegistry{node: "pve"}, "", gone); got != "" {
		t.Fatalf("with the machine removed pinned %q, want the normal choice", got)
	}
}
