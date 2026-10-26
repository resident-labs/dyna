package subagent

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/mode"
)

func TestAuthorityActorUsesStoredRoleAndRoom(t *testing.T) {
	for _, tc := range []struct {
		role string
		want authority.Role
	}{
		{"", authority.Session}, {RoleRoot, authority.Root}, {RoleLibrarian, authority.Session},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			m, _, work, metas := actorFixture(t)
			meta := metas["lead"]
			meta.Role = tc.role
			if err := m.Store.writeMeta(meta); err != nil {
				t.Fatal(err)
			}
			stampAs(t, meta)
			t.Setenv(mode.EnvRoomID, "")
			t.Setenv(mode.EnvRoom, work.Name)
			got, err := m.AuthorityActor(context.Background())
			if err != nil || got.Role != tc.want || got.Room != work.ID || got.Session != meta.ID {
				t.Fatalf("actor = %+v, %v", got, err)
			}
		})
	}
}

func TestAuthorityActorRejectsWrongRoomStamp(t *testing.T) {
	for _, key := range []string{mode.EnvRoomID, mode.EnvRoom} {
		t.Run(key, func(t *testing.T) {
			m, _, _, metas := actorFixture(t)
			stampAs(t, metas["lead"])
			t.Setenv(key, "another-room")
			if _, err := m.AuthorityActor(context.Background()); err == nil {
				t.Fatal("accepted wrong room stamp")
			}
		})
	}
}

func TestAuthorityActorRejectsMissingSession(t *testing.T) {
	m, _, _, metas := actorFixture(t)
	stampAs(t, metas["lead"])
	t.Setenv(mode.EnvSession, "ses_20261018-000099-ffffffff")
	if _, err := m.AuthorityActor(context.Background()); err == nil {
		t.Fatal("accepted missing session")
	}
}

func TestAuthorityActorSkipsLookupForProcessRoles(t *testing.T) {
	for _, tc := range []struct {
		mode string
		want authority.Role
	}{
		{"", authority.Owner}, {mode.Root, authority.Root},
	} {
		t.Run(string(tc.want), func(t *testing.T) {
			clearStamp(t)
			t.Setenv(mode.EnvMode, tc.mode)
			var m *Manager
			got, err := m.AuthorityActor(context.Background())
			if err != nil || got.Role != tc.want {
				t.Fatalf("actor = %+v, %v", got, err)
			}
		})
	}
}

func TestAuthorityActorRejectsClosedSession(t *testing.T) {
	m, _, _, metas := actorFixture(t)
	meta := metas["lead"]
	meta.Role, meta.Closed = RoleLibrarian, time.Now()
	if err := m.Store.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
	stampAs(t, meta)
	if _, err := m.AuthorityActor(context.Background()); err == nil {
		t.Fatal("accepted closed service session")
	}
}

func TestSessionAuthorityAllowsRelatedCallers(t *testing.T) {
	for _, tc := range []struct{ name, caller, action, target, role, mode string }{
		{"parent promotes", "lead", "subagent promote", "child", "", mode.Subagent},
		{"parent adopts", "lead", "subagent adopt", "child", "", mode.Subagent},
		{"parent renames", "lead", "subagent rename", "child", "", mode.Subagent},
		{"self renames", "child", "subagent rename", "child", "", mode.Subagent},
		{"session closes other", "other", "subagent close", "child", "", mode.Subagent},
		{"owner promotes", "", "subagent promote", "child", "", ""},
		{"root promotes", "", "subagent promote", "child", "", mode.Root},
		{"stored root promotes", "other", "subagent promote", "child", RoleRoot, mode.Subagent},
		{"stored root renames", "other", "subagent rename", "child", RoleRoot, mode.Subagent},
		{"stored root closes self", "other", "subagent close", "other", RoleRoot, mode.Subagent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _, _, metas := actorFixture(t)
			if tc.caller != "" {
				meta := metas[tc.caller]
				meta.Role = tc.role
				if err := m.Store.writeMeta(meta); err != nil {
					t.Fatal(err)
				}
				stampAs(t, meta)
			}
			t.Setenv(mode.EnvMode, tc.mode)
			got, err := m.authorityTarget(context.Background(), tc.action, metas[tc.target].ID)
			if err != nil || got.ID != metas[tc.target].ID {
				t.Fatalf("target = %s, %v", got.ID, err)
			}
		})
	}
}
