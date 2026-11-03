package lifecycle

import (
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/store"
)

func TestQuarantinedDedicatedNodeReleasesDetachedReservation(t *testing.T) {
	node := box.Host{Name: "node", Box: "box", State: box.HostReady, Problem: contract.HostProblemCreateFailed}
	row := store.LifecycleBox{Box: box.Box{ID: "box", State: contract.BoxPending}}
	if holdsNode(node, []store.LifecycleBox{row}) {
		t.Fatal("detached waiter must not hold quarantined node")
	}
	row.Host = node.Name
	if !holdsNode(node, []store.LifecycleBox{row}) {
		t.Fatal("placed box must still hold node")
	}
	row.Host = ""
	node.Problem = ""
	if !holdsNode(node, []store.LifecycleBox{row}) {
		t.Fatal("healthy reservation must still hold node")
	}
}
