package engine

import (
	"testing"

	"github.com/resident-labs/dyna/box/skills"
	"github.com/resident-labs/dyna/contract"
)

func imageArchive(t *testing.T) []byte {
	t.Helper()
	archive := skills.NewArchive()
	if err := archive.Add("Containerfile", 0o600, []byte("ARG DYNA_BASE\nFROM ${DYNA_BASE}\n")); err != nil {
		t.Fatal(err)
	}
	data, err := archive.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestASessionSavesImagesOnlyInItsOwnRoomAndTheOwnerInAny(t *testing.T) {
	s, room, worker, ctx, _ := setupAddons(t)
	other, problem := s.RoomCreate(ctx, ownerCaller, contract.RoomCreateRequest{Name: "other", RootProfile: "test"})
	if problem != nil {
		t.Fatal(problem)
	}
	save := contract.ImageSaveRequest{Name: "rust", Archive: imageArchive(t)}

	forbidden := func(what string, problem *contract.Error) {
		t.Helper()
		if problem == nil || problem.Code != contract.ErrForbidden {
			t.Fatalf("%s: %+v", what, problem)
		}
	}
	other1 := save
	other1.Room = other.Room
	_, problem = s.ImageSave(ctx, worker, other1)
	forbidden("save", problem)
	_, problem = s.ImageList(ctx, worker, contract.ImageListRequest{Room: other.Room})
	forbidden("list", problem)
	_, problem = s.ImageShow(ctx, worker, contract.ImageShowRequest{Name: "rust", Room: other.Room})
	forbidden("show", problem)
	_, problem = s.ImageRemove(ctx, worker, contract.ImageRemoveRequest{Name: "rust", Room: other.Room})
	forbidden("remove", problem)

	// A session's own room, named or not.
	if _, problem := s.ImageSave(ctx, worker, save); problem != nil {
		t.Fatal(problem)
	}
	if list, problem := s.ImageList(ctx, worker, contract.ImageListRequest{Room: room}); problem != nil || len(list.Images) != 1 {
		t.Fatalf("%+v %+v", list, problem)
	}
	if list, _ := s.ImageList(ctx, ownerCaller, contract.ImageListRequest{Room: other.Room}); len(list.Images) != 0 {
		t.Fatalf("the refused save landed: %+v", list)
	}

	// The owner names any room, and with none sees every room's.
	if _, problem := s.ImageSave(ctx, ownerCaller, other1); problem != nil {
		t.Fatal(problem)
	}
	if list, problem := s.ImageList(ctx, ownerCaller, contract.ImageListRequest{}); problem != nil || len(list.Images) != 2 {
		t.Fatalf("%+v %+v", list, problem)
	}
	if _, problem := s.ImageRemove(ctx, ownerCaller, contract.ImageRemoveRequest{Name: "rust", Room: other.Room}); problem != nil {
		t.Fatal(problem)
	}
	if _, problem := s.ImageRemove(ctx, ownerCaller, contract.ImageRemoveRequest{Name: "rust", Room: other.Room}); problem == nil || problem.Code != contract.ErrNotFound {
		t.Fatalf("%+v", problem)
	}
}
