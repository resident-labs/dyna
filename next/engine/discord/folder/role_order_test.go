package folder

import "testing"

func TestLowRoleSwapBelowUnchangedHighRole(t *testing.T) {
	high := Role{Name: "high", Position: 10, Identity: Identity{ID: "100"}}
	first := Role{Name: "a", Position: 1, Identity: Identity{ID: "101"}}
	second := Role{Name: "b", Position: 2, Identity: Identity{ID: "102"}}
	live := Snapshot{BotRolePosition: 5, Server: Server{Roles: []Role{high, second, first}}}
	plan, err := BuildPlan(Folder{Server: Server{Roles: []Role{high, first, second}}}, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Lines) != 0 || len(plan.Changes) != 1 || plan.Changes[0].Name != "role order" {
		t.Fatal(plan.String())
	}
}

func TestNewRoleRetainsDesiredPlacement(t *testing.T) {
	existing := Role{Name: "a", Position: 1, Identity: Identity{ID: "101"}}
	live := Snapshot{BotRolePosition: 5, Server: Server{Roles: []Role{existing}}}
	plan, err := BuildPlan(Folder{Server: Server{Roles: []Role{{Name: "new"}, existing}}}, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 || plan.Changes[1].Name != "role order" {
		t.Fatal(plan.String())
	}
	order := plan.Changes[1].After.([]RoleReference)
	if order[0].Name != "new" || order[1].ID != existing.ID {
		t.Fatalf("wrong order: %+v", order)
	}
}
