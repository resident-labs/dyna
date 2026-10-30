package authority

import (
	"testing"

	"dyna-agent/internal/store/storetest"
)

func TestMergeRoomAuthority(t *testing.T) {
	for _, role := range []Role{Owner, Root, Session, Worker} {
		for _, sameRoom := range []bool{false, true} {
			err := Check("git merge pause", Actor{Role: role}, func(Relation) bool { return sameRoom })
			want := role == Owner || role == Root && sameRoom
			if (err == nil) != want {
				t.Errorf("role=%s sameRoom=%v: %v", role, sameRoom, err)
			}
		}
	}
}

func TestCurrentRejectsIncompleteStamps(t *testing.T) {
	for _, key := range []string{"DYNA_SESSION", "DYNA_WORKER_RUN"} {
		t.Run(key, func(t *testing.T) {
			storetest.ClearStamp(t)
			t.Setenv(key, "bare-stamp")
			actor := Current()
			if actor.Role != "" {
				t.Fatalf("incomplete stamp became %s", actor.Role)
			}
			for _, rule := range Rules {
				if err := Check(rule.Action, actor, nil); err == nil {
					t.Errorf("%s allowed incomplete stamp", rule.Action)
				}
			}
		})
	}
	t.Setenv("DYNA_MODE", "invalid")
	if Current().Role != "" {
		t.Fatal("unknown mode has authority")
	}
}

func TestPR2RoleLimits(t *testing.T) {
	for _, action := range []string{"workflow run", "git pr merge-fix", "SetRoomChangePolicy", "SetRoomCIBudget", "ResolveFinding", "library enable", "library disable", "room clear-dormant", "subagent dismiss-pending", "DismissSession", "ChangeIssue", "SetGitHubAccess", "post @voice-call"} {
		for _, role := range []Role{Owner, Root, Session, Worker} {
			want := role == Owner || role == Root
			if action == "post @voice-call" {
				want = role == Root
			}
			err := Check(action, Actor{Role: role}, nil)
			if (err == nil) != want {
				t.Errorf("%s as %s: %v", action, role, err)
			}
		}
	}
	for _, command := range []string{"pause", "resume", "cancel", "retry", "bisect", "return", "publish"} {
		for _, role := range []Role{Owner, Root, Session, Worker} {
			for _, sameRoom := range []bool{false, true} {
				err := Check("git merge "+command, Actor{Role: role}, func(Relation) bool { return sameRoom })
				want := role == Owner || role == Root && sameRoom
				if (err == nil) != want {
					t.Errorf("%s as %s sameRoom=%v: %v", command, role, sameRoom, err)
				}
			}
		}
	}
}

func TestCurrentOwnerMaySelectRoom(t *testing.T) {
	for _, tc := range []struct{ id, name, room string }{
		{id: "room-test", room: "room-test"},
		{name: "work", room: "work"},
		{id: "room-test", name: "work", room: "room-test"},
	} {
		t.Run(tc.room+tc.name, func(t *testing.T) {
			storetest.ClearStamp(t)
			t.Setenv("DYNA_ROOM_ID", tc.id)
			t.Setenv("DYNA_ROOM", tc.name)
			actor := Current()
			if actor.Role != Owner || actor.Room != tc.room {
				t.Fatalf("actor = %+v", actor)
			}
			if err := Check("workflow run", actor, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}
