package watch

import (
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestStoredStewardResumesOnlyItsRoom(t *testing.T) {
	const stewardID = "ses_20261020-120000-aaaaaaaa"
	const ordinaryID = "ses_20261020-120000-bbbbbbbb"
	root := t.TempDir()
	rooms := &room.Store{Root: filepath.Join(root, "rooms")}
	own, err := rooms.Create(room.CreateInput{Name: "own"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := rooms.Create(room.CreateInput{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range []room.Record{own, other} {
		if err := ghclient.SetAccess(rooms, rec.ID, ghclient.ReadWrite); err != nil {
			t.Fatal(err)
		}
	}
	manager := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}, Rooms: rooms}
	for _, meta := range []subagent.Meta{
		{ID: stewardID, Name: "steward", Role: "merge-steward", RoomID: own.ID, Room: own.Name},
		{ID: ordinaryID, Name: "ordinary", RoomID: own.ID, Room: own.Name},
	} {
		recordtest.Write(t, manager.Store.Root, meta.ID, meta)
	}
	service := &Service{Store: &Store{Root: filepath.Join(root, "prs")}, Notify: notify.New(rooms, manager)}
	if err := service.Store.update(func(st *State) error {
		st.MergePauses = map[string]string{own.ID: "paused", other.ID: "paused"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_MODE", "subagent")
	t.Setenv("DYNA_SESSION", stewardID)
	t.Setenv("DYNA_ROOM_ID", own.ID)
	t.Setenv("DYNA_ROOM", own.Name)
	if err := service.MergeControl(t.Context(), own.ID, "resume", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.MergeControl(t.Context(), other.ID, "resume", nil, nil); err == nil || !strings.HasPrefix(err.Error(), "git merge resume is ") {
		t.Fatalf("other room: %v", err)
	}
	t.Setenv("DYNA_SESSION", ordinaryID)
	if err := service.MergeControl(t.Context(), own.ID, "resume", nil, nil); err == nil || !strings.HasPrefix(err.Error(), "git merge resume is ") {
		t.Fatalf("ordinary session: %v", err)
	}
	state, err := service.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if state.MergePauses[own.ID] != "" || state.MergePauses[other.ID] != "paused" {
		t.Fatalf("pauses = %+v", state.MergePauses)
	}
}
