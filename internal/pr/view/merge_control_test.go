package view

import (
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/ghclient"
	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store/storetest"
	"dyna-agent/internal/subagent"
)

func TestMergeControlChecksCallerAndRoom(t *testing.T) {
	storetest.Isolate(t)
	rooms := &room.Store{Root: t.TempDir()}
	own, err := rooms.Create(room.CreateInput{Name: "merge-own"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := rooms.Create(room.CreateInput{Name: "merge-other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ghclient.SetAccess(rooms, own.ID, ghclient.ReadWrite); err != nil {
		t.Fatal(err)
	}
	manager := &subagent.Manager{Store: &subagent.Store{Root: t.TempDir() + "/sessions"}}
	s := &Service{Notify: notify.New(rooms, manager)}
	// Engine environment must not supply a caller's role or room.
	t.Setenv("DYNA_MODE", "root")
	t.Setenv("DYNA_SESSION", "forged-root")
	t.Setenv("DYNA_ROOM_ID", other.ID)
	t.Setenv("DYNA_ROOM", other.Name)
	for _, actor := range []authority.Actor{
		{Role: authority.Owner},
		{Role: authority.Root, Room: own.ID, Session: "ses_20261021-000011-aaaa1111"},
		{Role: authority.Steward, Room: own.ID, Session: "ses_20261021-000012-bbbb2222"},
		{Role: authority.Root, Room: other.ID, Session: "ses_20261021-000013-cccc3333"},
		{Role: authority.Steward, Room: other.ID, Session: "ses_20261021-000014-dddd4444"},
		{Role: authority.Session, Room: own.ID, Session: "ses_20261021-000015-eeee5555"},
		{Role: authority.Worker, Room: own.ID, Run: "run"},
		{},
	} {
		t.Run(string(actor.Role)+"/"+actor.Session, func(t *testing.T) {
			if actor.Session != "" {
				role := ""
				if actor.Role == authority.Root {
					role = subagent.RoleRoot
				} else if actor.Role == authority.Steward {
					role = subagent.RoleSteward
				}
				rm := own
				if actor.Room == other.ID {
					rm = other
				}
				recordtest.Write(t, manager.Store.Root, actor.Session, subagent.Meta{ID: actor.Session, Role: role, RoomID: rm.ID, Room: rm.Name})
			}
			ctx := authority.WithActor(t.Context(), actor)
			allowed := actor.Role == authority.Owner || (actor.Role == authority.Root || actor.Role == authority.Steward) && actor.Room == own.ID
			for _, action := range []string{"pause", "cancel", "return", "retry", "bisect", "publish", "note", "resume"} {
				args := []string(nil)
				if action == "note" {
					args = []string{"Inspect the failed batch."}
				}
				err := s.MergeControl(ctx, own.Name, action, args, nil)
				if allowed {
					switch action {
					case "pause", "resume", "note":
						if err != nil {
							t.Fatalf("%s: %v", action, err)
						}
					default:
						// Existing batch validation must still run after authority.
						if err == nil || strings.Contains(err.Error(), "only") {
							t.Fatalf("%s: expected batch validation, got %v", action, err)
						}
					}
				} else if err == nil {
					t.Fatalf("%s accepted caller %+v", action, actor)
				}
			}
		})
	}
	status, err := s.Watcher().MergeControlStatus(own.ID)
	if err != nil || status.Paused {
		t.Fatalf("denied caller changed pause: %+v %v", status, err)
	}
	history, err := s.Watcher().RoomMergeHistory(own.ID)
	if err != nil || len(history) != 3 {
		t.Fatalf("decision history = %+v, %v", history, err)
	}
}
