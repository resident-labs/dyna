package engine

import (
	"github.com/resident-labs/dyna/box"
	"strings"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/approve"
)

// Each root-approves kind is one room setting on the approval path: off, only the owner decides; on, the room's root decides
// too, and the owner still can. The request of a session in another kind, or in another room, stays the owner's.
func TestRootApprovesKinds(t *testing.T) {
	kinds := []struct {
		kind    contract.RootApproval
		request func(s *Service, who contract.Principal, room contract.RoomID) (contract.OpID, *contract.Error)
	}{
		{contract.RootApprovesAddons, func(s *Service, who contract.Principal, room contract.RoomID) (contract.OpID, *contract.Error) {
			res, problem := s.OpSubmit(t.Context(), who, contract.OpSubmitRequest{Operation: contract.Operation{Room: room, Kind: "addon.global", Args: contract.AddonGlobalArgs{Kind: contract.AddonMCP, Name: "extra", Command: "node"}}})
			return res.Operation.ID, problem
		}},
		{contract.RootApprovesHosts, func(s *Service, who contract.Principal, room contract.RoomID) (contract.OpID, *contract.Error) {
			res, problem := s.RoomHosts(t.Context(), who, contract.RoomHostsRequest{Room: room})
			return res.Operation, problem
		}},
		{contract.RootApprovesPools, func(s *Service, who contract.Principal, room contract.RoomID) (contract.OpID, *contract.Error) {
			res, problem := s.PoolAdd(t.Context(), who, contract.PoolAddRequest{Name: "boats", Script: "#!/bin/sh\n", Max: 1})
			return res.Operation, problem
		}},
	}
	for _, k := range kinds {
		t.Run(string(k.kind), func(t *testing.T) {
			r := newGrantRig(t)
			ctx := t.Context()
			r.s.SetLayout(box.Layout{Data: t.TempDir(), HostData: t.TempDir()})
			entries := map[contract.OpKind]approve.Entry{"hostexec.grant": r.s.HostexecGrant(), "addon.global": r.s.AddonGlobal(), "room.hosts": r.s.RoomHostsEntry(), "pool.add": r.s.PoolAddEntry()}
			if err := r.s.ConfigureOperations(ctx, entries, time.Hour); err != nil {
				t.Fatal(err)
			}
			decide := func(who contract.Principal, id contract.OpID) *contract.Error {
				_, problem := r.s.OpDecide(ctx, who, contract.OpDecideRequest{ID: id, Approve: true})
				return problem
			}
			if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, RootApproves: map[contract.RootApproval]bool{contract.RootApprovesAddons: false, contract.RootApprovesHosts: false, contract.RootApprovesPools: false}}); problem != nil {
				t.Fatal(problem)
			}
			// Off, as today: the root is refused, and the card names no root.
			off, problem := k.request(r.s, r.worker, r.room)
			if problem != nil {
				t.Fatal(problem)
			}
			if got, err := r.s.store.Operation(ctx, off); err != nil || strings.Contains(got.Operation.Card.Body, "root") {
				t.Fatalf("card with the setting off: %q %v", got.Operation.Card.Body, err)
			}
			if problem := decide(r.root, off); problem == nil || problem.Code != contract.ErrForbidden {
				t.Fatalf("root with the setting off: %v", problem)
			}
			// On for this kind only: the root decides it, and no other kind.
			set := func(kind contract.RootApproval, on bool) {
				if _, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, RootApproves: map[contract.RootApproval]bool{kind: on}}); problem != nil {
					t.Fatal(problem)
				}
			}
			for _, other := range contract.AllRootApprovals {
				if other != k.kind {
					set(other, true)
				}
			}
			if problem := decide(r.root, off); problem == nil || problem.Code != contract.ErrForbidden {
				t.Fatalf("root with only the other kinds on: %v", problem)
			}
			set(k.kind, true)
			if problem := decide(r.worker, off); problem == nil || problem.Code != contract.ErrForbidden {
				t.Fatalf("the asking session decided its own request: %v", problem)
			}
			if problem := decide(r.root, off); problem != nil {
				t.Fatalf("root with the setting on: %v", problem)
			}
			if got, err := r.s.store.Operation(ctx, off); err != nil || got.Operation.Decision == nil || got.Operation.Decision.By != r.root {
				t.Fatalf("decision: %+v %v", got.Operation.Decision, err)
			}
			// The card now says who can approve, and the owner still decides.
			on, problem := k.request(r.s, r.worker, r.room)
			if problem != nil {
				t.Fatal(problem)
			}
			if got, err := r.s.store.Operation(ctx, on); err != nil || strings.Contains(got.Operation.Card.Body, "Waiting for") {
				t.Fatalf("card with the setting on: %q %v", got.Operation.Card.Body, err)
			}
			if problem := decide(r.owner, on); problem != nil {
				t.Fatalf("owner with the setting on: %v", problem)
			}
			if got, err := r.s.store.Operation(ctx, on); err != nil || got.Operation.Decision == nil || got.Operation.Decision.By != r.owner {
				t.Fatalf("owner decision: %+v %v", got.Operation.Decision, err)
			}
			// Off again: back to the owner alone.
			set(k.kind, false)
			again, problem := k.request(r.s, r.worker, r.room)
			if problem != nil {
				t.Fatal(problem)
			}
			if problem := decide(r.root, again); problem == nil || problem.Code != contract.ErrForbidden {
				t.Fatalf("root after the setting went off: %v", problem)
			}
		})
	}
}

func TestRoomSetRootApprovesKeepsOmittedKinds(t *testing.T) {
	r := newGrantRig(t)
	ctx := t.Context()
	set := func(changes map[contract.RootApproval]bool) *contract.Error {
		_, problem := r.s.RoomSet(ctx, r.owner, contract.RoomSetRequest{Room: r.room, RootApproves: changes})
		return problem
	}
	if problem := set(map[contract.RootApproval]bool{"nothing": true}); problem == nil || problem.Code != contract.ErrInvalid {
		t.Fatalf("an unknown kind: %v", problem)
	}
	if problem := set(map[contract.RootApproval]bool{contract.RootApprovesPools: true, contract.RootApprovesAddons: true}); problem != nil {
		t.Fatal(problem)
	}
	if problem := set(map[contract.RootApproval]bool{contract.RootApprovesAddons: false, contract.RootApprovesHosts: true}); problem != nil {
		t.Fatal(problem)
	}
	list, problem := r.s.RoomList(ctx, r.owner, contract.RoomListRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, room := range list.Rooms {
		if room.ID == r.room {
			want := []contract.RootApproval{contract.RootApprovesHosts, contract.RootApprovesPools}
			if len(room.RootApproves) != 2 || room.RootApproves[0] != want[0] || room.RootApproves[1] != want[1] {
				t.Fatalf("room list: %v, want %v", room.RootApproves, want)
			}
			return
		}
	}
	t.Fatal("room missing from the list")
}
