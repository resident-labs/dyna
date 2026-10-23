package subagent

import (
	"dyna-agent/internal/goals"

	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/room"
)

func TestStartParentsSessionActorsAndInheritsService(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		actorRole  authority.Role
		service    bool
		wantSvc    bool
	}{
		{name: "session", actorRole: authority.Session},
		{name: "root", role: RoleRoot, actorRole: authority.Root, service: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			parent := Meta{ID: NewID(), Name: tc.name, RoomID: e.room.ID, Room: e.room.Name, Depth: 2, Role: tc.role, Service: tc.service}
			if err := e.m.Store.writeMeta(parent); err != nil {
				t.Fatal(err)
			}
			if tc.actorRole == authority.Session {
				// An ordinary session parent delegates from a goal it holds.
				if _, err := e.m.goalStore().Add(e.room.ID, "owner", goals.Input{Text: "Parent work", Holders: []string{parent.ID}}); err != nil {
					t.Fatal(err)
				}
			}
			ctx := authority.WithActor(e.ctx, authority.Actor{Role: tc.actorRole, Session: parent.ID, Room: e.room.ID})
			info, _, err := e.m.Start(ctx, StartOptions{Goal: "g", Task: "RESPOND: done", Profile: "mock", Dir: t.TempDir(), Room: e.room.ID})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if info.ParentSession != parent.ID || info.Depth != parent.Depth+1 || info.Service != tc.wantSvc {
				t.Fatalf("child = %+v; want parent %s, depth %d, service %v", info.Meta, parent.ID, parent.Depth+1, tc.wantSvc)
			}
			e.wait(info.ID, 1)
		})
	}
}

func TestStartSessionActorsCannotStartInAnotherRoom(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		actorRole  authority.Role
	}{
		{name: "session", actorRole: authority.Session},
		{name: "root", role: RoleRoot, actorRole: authority.Root},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			other, err := e.m.rooms().Create(room.CreateInput{Name: "other"})
			if err != nil {
				t.Fatal(err)
			}
			parent := Meta{ID: NewID(), Name: tc.name, RoomID: e.room.ID, Room: e.room.Name, Depth: 1, Role: tc.role}
			if err := e.m.Store.writeMeta(parent); err != nil {
				t.Fatal(err)
			}
			if tc.actorRole == authority.Session {
				// An ordinary session parent delegates from a goal it holds.
				if _, err := e.m.goalStore().Add(e.room.ID, "owner", goals.Input{Text: "Parent work", Holders: []string{parent.ID}}); err != nil {
					t.Fatal(err)
				}
			}
			ctx := authority.WithActor(e.ctx, authority.Actor{Role: tc.actorRole, Session: parent.ID, Room: e.room.ID})
			_, _, err = e.m.Start(ctx, StartOptions{Goal: "g", Task: "RESPOND: done", Profile: "mock", Dir: t.TempDir(), Room: other.ID})
			if err == nil || !strings.Contains(err.Error(), "parent session is in another room") {
				t.Fatalf("cross-room Start = %v", err)
			}
		})
	}
}

func TestStartOwnerRemainsTopLevelAndOnlyOwnerOrRootStartsAService(t *testing.T) {
	e := newTestEnv(t)
	ctx := authority.WithActor(e.ctx, authority.Actor{Role: authority.Owner, Room: e.room.ID})
	info, _, err := e.m.Start(ctx, StartOptions{Goal: "g", Task: "RESPOND: done", Profile: "mock", Dir: t.TempDir(), Room: e.room.ID, Owner: "codex:test"})
	if err != nil {
		t.Fatalf("owner Start: %v", err)
	}
	if info.ParentSession != "" || info.Depth != 1 || info.Owner != "codex:test" {
		t.Fatalf("owner child = %+v; want a top-level session owned by codex:test", info.Meta)
	}
	e.wait(info.ID, 1)

	root := authority.WithActor(e.ctx, authority.Actor{Role: authority.Root, Room: e.room.ID})
	info, _, err = e.m.Start(root, StartOptions{Goal: "g", Task: "RESPOND: done", Profile: "mock", Dir: t.TempDir(), Room: e.room.ID})
	if err != nil {
		t.Fatalf("session-less root Start: %v", err)
	}
	if info.ParentSession != "" || info.Depth != 1 {
		t.Fatalf("session-less root child = %+v; want a top-level session", info.Meta)
	}
	e.wait(info.ID, 1)

}
