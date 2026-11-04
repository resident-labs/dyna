package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// dyna_room is the root's alone, shows the root-approves kinds that are on in a fixed order, and is absent when none is.
func TestContextRoomSection(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	section := func(who contract.Principal) (string, bool) {
		res, problem := r.s.ContextSnapshot(ctx, who, contract.ContextSnapshotRequest{})
		if problem != nil {
			t.Fatal(problem)
		}
		for _, sec := range res.Sections {
			if sec.Key == contract.ContextRoom {
				return sec.Text, true
			}
		}
		return "", false
	}
	set := func(changes map[contract.RootApproval]bool) {
		if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, RootApproves: changes}); problem != nil {
			t.Fatal(problem)
		}
	}
	if text, ok := section(r.root); ok {
		t.Fatalf("all off, root has %q", text)
	}
	set(map[contract.RootApproval]bool{contract.RootApprovesAddons: true})
	if text, ok := section(r.root); !ok || text != "Approvals you decide: global add-ons" {
		t.Fatalf("one on: %q %v", text, ok)
	}
	if text, ok := section(r.worker); ok {
		t.Fatalf("a non-root session has %q", text)
	}
	set(map[contract.RootApproval]bool{contract.RootApprovesPools: true, contract.RootApprovesHostexec: true})
	if text, _ := section(r.root); text != "Approvals you decide: host exec, global add-ons, node pools" {
		t.Fatalf("three on: %q", text)
	}
	set(map[contract.RootApproval]bool{contract.RootApprovesHosts: true})
	if text, _ := section(r.root); text != "Approvals you decide: host exec, global add-ons, room host rules, node pools" {
		t.Fatalf("four on: %q", text)
	}
	if _, ok := section(r.worker); ok {
		t.Fatal("a non-root session has the section")
	}
	one, err := r.s.ContextSnapshot(ctx, r.root, contract.ContextSnapshotRequest{Section: contract.ContextRoom})
	if err != nil || len(one.Sections) != 1 {
		t.Fatalf("one section: %+v %v", one, err)
	}
	set(map[contract.RootApproval]bool{contract.RootApprovesHostexec: false, contract.RootApprovesAddons: false, contract.RootApprovesHosts: false, contract.RootApprovesPools: false})
	if text, ok := section(r.root); ok {
		t.Fatalf("all off again, root has %q", text)
	}
}
