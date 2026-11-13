package folder

import "testing"

func TestCategoryRenamePreservesChildren(t *testing.T) {
	for _, adopt := range []Identity{{Was: "old"}, {ID: "200"}} {
		live := Snapshot{Entries: []Entry{{Name: "old", Path: "old", Category: &Category{Identity: Identity{ID: "200"}}}, {Name: "chat", Path: "old/chat", Parent: "old", Channel: &Channel{Identity: Identity{ID: "201"}, Type: "text"}}}}
		desired := Folder{Entries: []Entry{{Name: "new", Path: "new", Category: &Category{Identity: adopt}}, {Name: "chat", Path: "new/chat", Parent: "new", Channel: &Channel{Type: "text"}}}}
		plan, err := BuildPlan(desired, live)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range plan.Changes {
			if change.Field == "create" {
				t.Fatalf("rename recreated: %s", plan.String())
			}
		}
		if len(plan.Changes) != 1 {
			t.Fatalf("want only category rename: %s", plan.String())
		}
	}
}

func TestMessageDiffs(t *testing.T) {
	live := Snapshot{Entries: []Entry{{Name: "chat", Path: "chat", Channel: &Channel{Identity: Identity{ID: "201"}, Type: "text"}, Messages: map[string]Message{"welcome.md": {ID: "300", Body: "hello"}}}}}
	for _, test := range []struct {
		body    string
		changes int
		field   string
	}{{"hello", 0, ""}, {"changed", 1, "edit"}} {
		desired := Folder{Entries: []Entry{{Name: "chat", Path: "chat", Channel: &Channel{Type: "text"}, Messages: map[string]Message{"welcome.md": {Body: test.body}}}}}
		plan, err := BuildPlan(desired, live)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Changes) != test.changes {
			t.Fatal(plan.String())
		}
		if test.changes > 0 && (plan.Changes[0].ID != "300" || plan.Changes[0].Target != "201" || plan.Changes[0].Field != test.field) {
			t.Fatalf("bad edit: %+v", plan.Changes[0])
		}
	}
	desired := Folder{Entries: []Entry{{Name: "new", Path: "new", Channel: &Channel{Type: "text"}, Messages: map[string]Message{"welcome.md": {Body: "hello"}}}}}
	plan, err := BuildPlan(desired, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 2 || plan.Changes[1].Kind != "message" || plan.Changes[1].Field != "create" {
		t.Fatal(plan.String())
	}
}

func TestAutoModIdentityValidation(t *testing.T) {
	for _, rules := range [][]AutoMod{
		{{Name: "one", Identity: Identity{ID: "abc"}}},
		{{Name: "one", Identity: Identity{ID: "100", Was: "old"}}},
		{{Name: "one", Identity: Identity{ID: "100"}}, {Name: "two", Identity: Identity{ID: "100"}}},
	} {
		path := t.TempDir()
		if _, err := Export(path, Snapshot{Server: Server{AutoMod: rules}}, false); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Fatalf("accepted %#v", rules)
		}
	}
}
