package box_test

import (
	"testing"
	"time"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

func switchHost(name contract.HostName) box.Host {
	return box.Host{Name: name, State: box.HostReady, CPUs: 4, MemBytes: 8 * box.GiB, DiskBytes: 100 * box.GiB, DiskFloor: 20 * box.GiB}
}

// A host with agents off takes no new session box, whatever its room list says, and the box goes to a host with agents on.
func TestPlacementSkipsAnAgentsOffHost(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeAgent)
	request := box.Box{ID: "box", Room: "room", Size: size}
	ci := switchHost("ci")
	ci.NoAgents, ci.CI, ci.MemBytes = true, true, 64*box.GiB // the most free memory, which admission would otherwise prefer
	if got := box.Admit([]box.Host{ci}, nil, request); got.Host != "" || got.Reason != box.PendingRoomHost {
		t.Fatalf("agents off alone: %+v", got)
	}
	ci.Rooms = []contract.RoomID{"room"}
	if got := box.Admit([]box.Host{ci}, nil, request); got.Host != "" || got.Reason != box.PendingRoomHost {
		t.Fatalf("agents off with the room listed: %+v", got)
	}
	if got := box.Admit([]box.Host{ci, switchHost("worker")}, nil, request); got.Host != "worker" {
		t.Fatalf("with a host that takes sessions: %+v", got)
	}
	ci.NoAgents = false
	if got := box.Admit([]box.Host{ci, switchHost("worker")}, nil, request); got.Host != "ci" {
		t.Fatalf("agents back on: %+v", got)
	}
}

// The CI switch does not touch placement: a host with ci on and agents on takes sessions, and one with both off takes none.
func TestCISwitchLeavesPlacementAlone(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeAgent)
	request := box.Box{ID: "box", Room: "room", Size: size}
	h := switchHost("h")
	h.CI = true
	if got := box.Admit([]box.Host{h}, nil, request); got.Host != "h" {
		t.Fatalf("ci on, agents on: %+v", got)
	}
	h.CI, h.NoAgents = false, true
	if got := box.Admit([]box.Host{h}, nil, request); got.Host != "" || got.Reason != box.PendingRoomHost {
		t.Fatalf("both off: %+v", got)
	}
}

// A box already on a host with agents off stays placed there and does not wake there, as for a room its list leaves out.
func TestAgentsOffHoldsAStoppedBox(t *testing.T) {
	size, _ := box.Size(contract.BoxSizeAgent)
	h := switchHost("h")
	h.CheckedAt = time.Unix(10, 0)
	stopped := box.Box{ID: "box", Room: "room", Host: "h", Image: "image", Size: size, State: contract.BoxStopped, PlacedAt: time.Unix(1, 0)}
	if got := box.Admit([]box.Host{h}, []box.Box{stopped}, stopped); got.Host != "h" {
		t.Fatalf("agents on: %+v", got)
	}
	h.NoAgents = true
	if got := box.Admit([]box.Host{h, switchHost("other")}, []box.Box{stopped}, stopped); got.Host != "" || got.Reason != box.PendingRoomHost {
		t.Fatalf("agents off: %+v", got)
	}
}

func TestServes(t *testing.T) {
	h := switchHost("h")
	if !h.Serves("a") {
		t.Fatal("an unlimited host with agents on serves every room")
	}
	h.Rooms = []contract.RoomID{"a"}
	if !h.Serves("a") || h.Serves("b") {
		t.Fatal("the room list decides while agents are on")
	}
	h.NoAgents = true
	if h.Serves("a") {
		t.Fatal("agents off serves no room")
	}
}
