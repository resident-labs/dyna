package folder

import "testing"

func TestWasRemainsIdempotentAfterRename(t *testing.T) {
	desired := Folder{
		Server:  Server{Roles: []Role{{Name: "new-role", Identity: Identity{Was: "old-role"}}}, AutoMod: []AutoMod{{Name: "new-rule", Identity: Identity{Was: "old-rule"}}}},
		Entries: []Entry{{Name: "new-category", Path: "new-category", Category: &Category{Identity: Identity{Was: "old-category"}}}, {Name: "new-channel", Path: "new-category/new-channel", Parent: "new-category", Channel: &Channel{Type: "text", Identity: Identity{Was: "old-channel"}}}},
	}
	live := Snapshot{
		Server:  Server{Roles: []Role{{Name: "new-role", Identity: Identity{ID: "100"}}}, AutoMod: []AutoMod{{Name: "new-rule", Identity: Identity{ID: "101"}}}},
		Entries: []Entry{{Name: "new-category", Path: "new-category", Category: &Category{Identity: Identity{ID: "200"}}}, {Name: "new-channel", Path: "new-category/new-channel", Parent: "new-category", Channel: &Channel{Type: "text", Identity: Identity{ID: "201"}}}},
	}
	plan, err := BuildPlan(desired, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatal(plan.String())
	}
	for _, names := range [][2]string{{"old-role", "new-role"}, {"old-rule", "new-rule"}, {"old-category", "new-category"}, {"old-channel", "new-channel"}} {
		for _, name := range names {
			if !matches(Identity{Was: names[0]}, names[1], "100", name) {
				t.Fatalf("did not match %s", name)
			}
		}
	}
	live.Server.Roles = append(live.Server.Roles, Role{Name: "old-role", Identity: Identity{ID: "102"}})
	if _, err := BuildPlan(desired, live); err == nil {
		t.Fatal("old and new role names were not ambiguous")
	}
}

func TestRoleOrderRespectsBotPosition(t *testing.T) {
	desired := []Role{{Name: "low", Identity: Identity{ID: "101"}}, {Name: "high", Identity: Identity{ID: "100"}}}
	live := Snapshot{BotRolePosition: 5, Server: Server{Roles: []Role{{Name: "high", Position: 10, Identity: Identity{ID: "100"}}, {Name: "low", Position: 1, Identity: Identity{ID: "101"}}}}}
	var plan Plan
	if err := planRoles(&plan, desired, live); err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || len(plan.Lines) != 1 {
		t.Fatal(plan.String())
	}
	live.BotRolePosition = 20
	plan = Plan{}
	if err := planRoles(&plan, desired, live); err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || len(plan.Lines) != 0 {
		t.Fatal(plan.String())
	}
}
