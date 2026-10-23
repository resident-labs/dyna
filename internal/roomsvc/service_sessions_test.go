package roomsvc

import (
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/subagent"
)

// A service and its helper are outside the hierarchy, so they are no member and
// no @team recipient, but the composer still offers their names, because a post
// may address a service directly.
func TestServiceSessionsStayOutOfMembersAndInMentions(t *testing.T) {
	e := newEnv(t)
	rec := e.room("services")
	worker := e.start(rec, "worker", nil)
	var serviceNames []string
	for _, role := range []string{"reviewer", subagent.RoleLibrarian} {
		// Persist the service flag with its role; helpers read the canonical parent.
		info := e.start(rec, role+"-service", nil)
		if _, err := e.mgr.Store.UpdateMeta(info.ID, func(m *subagent.Meta) { m.Role, m.Service = role, true }); err != nil {
			t.Fatal(err)
		}
		e.start(rec, role+"-helper", &info)
		serviceNames = append(serviceNames, "@"+role+"-service")
	}
	draft, err := e.svc.Preview(rec.Name, "@team General room post")
	if err != nil || len(draft.Wakes) != 1 || !strings.Contains(draft.Wakes[0], worker.Name) {
		t.Fatalf("@team room post: %+v %v", draft, err)
	}
	members, err := e.svc.Members(rec.Name, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range members {
		if member.Service {
			t.Fatalf("service session appears as a member: %+v", member)
		}
	}
	choices, err := e.svc.MentionChoices(rec.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range serviceNames {
		if !slices.Contains(choices, name) {
			t.Fatalf("mention choices %v lack the service %s", choices, name)
		}
	}
}
