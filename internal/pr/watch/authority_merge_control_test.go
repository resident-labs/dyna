package watch

import (
	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
	"path/filepath"
	"strings"
	"testing"
)

func TestMergeControlAuthorityBeforeBackend(t *testing.T) {
	t.Setenv("DYNA_MODE", "subagent")
	const childID = "ses_20261020-000001-cccccccc"
	t.Setenv("DYNA_SESSION", childID)
	root := t.TempDir()
	manager := &subagent.Manager{Store: &subagent.Store{Root: filepath.Join(root, "sessions")}}
	recordtest.Write(t, manager.Store.Root, childID, subagent.Meta{ID: childID, RoomID: "work", Room: "work"})
	service := &Service{Notify: notify.New(&room.Store{Root: filepath.Join(root, "rooms")}, manager)}
	for _, action := range []string{"pause", "resume", "cancel", "retry", "bisect", "return", "publish"} {
		err := service.MergeControl(t.Context(), "missing", action, nil, nil, nil)
		if err == nil || !strings.HasPrefix(err.Error(), "git merge "+action+" is ") {
			t.Fatalf("%s: %v", action, err)
		}
	}
}

func TestMergeControlUsesAuthenticatedActor(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: t.TempDir()}
	rm, err := rooms.Create(room.CreateInput{Name: "controls"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ghclient.SetAccess(rooms, rm.ID, ghclient.ReadWrite); err != nil {
		t.Fatal(err)
	}
	s := &Service{Store: &Store{Root: t.TempDir()}, Notify: notify.New(rooms, subagent.NewManager())}
	// The process claims an unrelated root. The context is the authenticated caller.
	t.Setenv("DYNA_MODE", "root")
	t.Setenv("DYNA_SESSION", "forged-root")
	t.Setenv("DYNA_ROOM_ID", "unrelated-room")
	t.Setenv("DYNA_ROOM", "unrelated-room")
	for _, role := range []authority.Role{authority.Root, authority.Owner} {
		actor := authority.Actor{Role: role, Room: rm.ID}
		if role != authority.Owner {
			actor.Session = "ses_20261021-000011-bcdebcde"
			recordtest.Write(t, s.sessionStore().Root, actor.Session, subagent.Meta{ID: actor.Session, Role: subagent.RoleRoot, RoomID: rm.ID, Room: rm.Name})
		}
		ctx := authority.WithActor(t.Context(), actor)
		if err := s.MergeControl(ctx, rm.ID, "pause", nil, nil, nil); err != nil {
			t.Fatalf("%s pause: %v", role, err)
		}
		if err := s.MergeControl(ctx, rm.ID, "note", []string{"Inspect the failed batch."}, nil, nil); err != nil {
			t.Fatalf("%s note: %v", role, err)
		}
		if err := s.MergeControl(ctx, rm.ID, "resume", nil, nil, nil); err != nil {
			t.Fatalf("%s resume: %v", role, err)
		}
	}
	// An owner process must not lend its authority to an unknown caller.
	storetest.ClearStamp(t)
	for _, action := range []string{"pause", "resume", "cancel", "retry", "bisect", "return", "publish", "note"} {
		args := []string(nil)
		if action == "note" {
			args = []string{"Denied decision."}
		}
		err := s.MergeControl(authority.WithActor(t.Context(), authority.Actor{}), rm.ID, action, args, nil, nil)
		if err == nil || !strings.HasPrefix(err.Error(), "git merge "+action+" is ") {
			t.Fatalf("%s: %v", action, err)
		}
	}
}
