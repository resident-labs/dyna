package dynamcp

import "testing"

func TestRegistryBuilds(t *testing.T) {
	list, actions, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 || len(actions) == 0 {
		t.Fatal("empty registry")
	}
	web, ok := actions["web"]["search"]
	if !ok || web.method != "web.search" || !web.readOnly {
		t.Fatal("web search is not a direct read action")
	}
	for _, tool := range list {
		if tool.Name == "web" {
			if !tool.Annotations.ReadOnly || !tool.Annotations.Idempotent || !tool.Annotations.OpenWorld || tool.Annotations.Destructive {
				t.Fatal("wrong web annotations")
			}
			return
		}
	}
	t.Fatal("web toolkit missing from initial tool list")
}

func TestOnlyAToolWithSeveralActionsRequiresOne(t *testing.T) {
	list, _, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		verbs := item.InputSchema.Properties["action"].Enum
		if required := len(item.InputSchema.Required) > 0; required != (len(verbs) > 1) {
			t.Errorf("%s has %d actions and required %v", item.Name, len(verbs), item.InputSchema.Required)
		}
	}
}

func TestFieldDescriptionsComeFromTheContract(t *testing.T) {
	list, _, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	described := map[string]map[string]schema{}
	for _, item := range list {
		described[item.Name] = item.InputSchema.Properties
	}
	for _, tc := range []struct{ tool, field, want string }{
		{"post", "room", "A room_… ID, from your instructions. Omitted, it is your own room."},
		{"post", "home", "A session may name only its own DM, {kind: dm}. With no home, a post that names no session goes to the owner's conversation with you: where this turn's reply to the owner goes, else where the owner last wrote to you, else the feed. One that names sessions goes to the feed."},
		{"room", "home", "home narrows the log to one owner DM: {kind: dm, session: ses_…}."},
		{"visual", "file", "Absolute, or relative to your working directory."},
	} {
		if got := described[tc.tool][tc.field].Description; got != tc.want {
			t.Errorf("%s.%s: %q", tc.tool, tc.field, got)
		}
	}
}
