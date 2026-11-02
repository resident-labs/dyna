package engine

import (
	"os"
	"testing"

	"github.com/resident-labs/dyna/box"
	"github.com/resident-labs/dyna/contract"
)

// A room's root reads and edits the global file and any room's, as the owner does, and each save names who made it.
func TestARootEditsEveryInstructionsFile(t *testing.T) {
	s, room, rootID, ctx := setupRoot(t)
	s.SetLayout(box.Layout{Data: t.TempDir(), HostData: t.TempDir()})
	root := contract.Principal{Kind: contract.PrincipalSession, Session: rootID}
	other, problem := s.RoomCreate(ctx, ownerCaller, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, target := range []contract.RoomID{"", room, other.Room} {
		before, problem := s.InstructionsGet(ctx, root, contract.InstructionsGetRequest{Room: target})
		if problem != nil {
			t.Fatalf("%q get: %v", target, problem)
		}
		saved, problem := s.InstructionsSet(ctx, root, contract.InstructionsSetRequest{Room: target, Text: "rules for " + string(target), Base: before.Hash})
		if problem != nil {
			t.Fatalf("%q set: %v", target, problem)
		}
		after, problem := s.InstructionsGet(ctx, root, contract.InstructionsGetRequest{Room: target})
		if problem != nil || after.Text != "rules for "+string(target) || after.Hash != saved.Hash {
			t.Fatalf("%q after: %+v %v", target, after, problem)
		}
		want := contract.InstructionsSaver{Kind: contract.PrincipalSession, Session: rootID, Name: "root", RoomName: "room"}
		if after.SavedBy == nil || *after.SavedBy != want {
			t.Fatalf("%q saver: %+v", target, after.SavedBy)
		}
		// The owner sees the same file, and who saved it.
		seen, problem := s.InstructionsGet(ctx, ownerCaller, contract.InstructionsGetRequest{Room: target})
		if problem != nil || seen.Hash != saved.Hash || seen.SavedBy == nil || *seen.SavedBy != want {
			t.Fatalf("%q owner view: %+v %v", target, seen, problem)
		}
	}
}

// Only a root: a child session can neither read nor edit any instructions file.
func TestAChildCannotTouchInstructions(t *testing.T) {
	s, room, worker, ctx := setup(t)
	s.SetLayout(box.Layout{Data: t.TempDir(), HostData: t.TempDir()})
	child := contract.Principal{Kind: contract.PrincipalSession, Session: worker}
	owned, problem := s.InstructionsGet(ctx, ownerCaller, contract.InstructionsGetRequest{})
	if problem != nil {
		t.Fatal(problem)
	}
	for _, target := range []contract.RoomID{"", room} {
		if _, problem := s.InstructionsGet(ctx, child, contract.InstructionsGetRequest{Room: target}); problem == nil || problem.Code != contract.ErrForbidden || problem.Message != "only the owner or a room's root edits instructions" {
			t.Fatalf("%q get: %+v", target, problem)
		}
		if _, problem := s.InstructionsSet(ctx, child, contract.InstructionsSetRequest{Room: target, Text: "mine", Base: owned.Hash}); problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("%q set: %+v", target, problem)
		}
	}
	if after, _ := s.InstructionsGet(ctx, ownerCaller, contract.InstructionsGetRequest{}); after.Text != "" || after.SavedBy != nil {
		t.Fatalf("a refused save changed the file: %+v", after)
	}
}

// A root's save over an edit it has not read is stale, as the owner's would be, and leaves the other edit in place.
func TestARootSaveOverAnotherEditIsStale(t *testing.T) {
	s, room, rootID, ctx := setupRoot(t)
	s.SetLayout(box.Layout{Data: t.TempDir(), HostData: t.TempDir()})
	root := contract.Principal{Kind: contract.PrincipalSession, Session: rootID}
	read, problem := s.InstructionsGet(ctx, root, contract.InstructionsGetRequest{Room: room})
	if problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.InstructionsSet(ctx, ownerCaller, contract.InstructionsSetRequest{Room: room, Text: "the owner's", Base: read.Hash}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.InstructionsSet(ctx, root, contract.InstructionsSetRequest{Room: room, Text: "the root's", Base: read.Hash}); problem == nil || problem.Code != contract.ErrStale {
		t.Fatalf("stale save: %+v", problem)
	}
	after, problem := s.InstructionsGet(ctx, root, contract.InstructionsGetRequest{Room: room})
	if problem != nil || after.Text != "the owner's" || after.SavedBy == nil || after.SavedBy.Kind != contract.PrincipalOwner || after.SavedBy.Session != "" {
		t.Fatalf("after: %+v %v", after, problem)
	}
}

// The record names only the save that left the file as it is: a file changed some other way names nobody.
func TestTheSaverIsForgottenWhenTheFileChangesElsewhere(t *testing.T) {
	s, _, rootID, ctx := setupRoot(t)
	layout := box.Layout{Data: t.TempDir(), HostData: t.TempDir()}
	s.SetLayout(layout)
	root := contract.Principal{Kind: contract.PrincipalSession, Session: rootID}
	read, _ := s.InstructionsGet(ctx, root, contract.InstructionsGetRequest{})
	if _, problem := s.InstructionsSet(ctx, root, contract.InstructionsSetRequest{Text: "saved", Base: read.Hash}); problem != nil {
		t.Fatal(problem)
	}
	if err := os.WriteFile(layout.Instructions(""), []byte("by hand"), 0o600); err != nil {
		t.Fatal(err)
	}
	after, problem := s.InstructionsGet(ctx, ownerCaller, contract.InstructionsGetRequest{})
	if problem != nil || after.Text != "by hand" || after.SavedBy != nil {
		t.Fatalf("after: %+v %v", after, problem)
	}
}
