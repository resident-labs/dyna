package roomsvc

import (
	"strings"
	"testing"

	"dyna-agent/internal/subagent"
)

func TestServiceSessionsTeamComposerAndMemberFlag(t *testing.T) {
	e := newEnv(t)
	rec := e.room("services")
	worker := e.start(rec, "worker", nil)
	for _, role := range []string{"reviewer", subagent.RoleLibrarian} {
		// Persist the service flag with its role; helpers read the canonical parent.
		info := e.start(rec, role+"-service", nil)
		if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Role, m.Service = role, true }); err != nil {
			t.Fatal(err)
		}
		e.start(rec, role+"-helper", &info)
	}
	draft, err := e.svc.Preview(rec.Name, "@team General room post")
	if err != nil || len(draft.Wakes) != 1 || !strings.Contains(draft.Wakes[0], worker.Name) {
		t.Fatalf("@team room post: %+v %v", draft, err)
	}
	members, err := e.svc.Members(rec.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, member := range members {
		if member.Service {
			count++
		}
	}
	if count != 4 {
		t.Fatalf("service members = %d; want 4", count)
	}
}
