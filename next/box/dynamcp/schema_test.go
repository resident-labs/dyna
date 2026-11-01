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
