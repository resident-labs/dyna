package dynamcp

import (
	"strings"
	"testing"
)

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
		{"post", "room", "Room ID; omitted means your room. A root can name another room to post plain text to its root in the feed."},
		{"post", "home", "A session may name only its own DM, {kind: dm}. With no home, a post that names no session goes to the owner's conversation with you: where this turn's reply to the owner goes, else where the owner last wrote to you, else the feed. One that names sessions goes to the feed."},
		{"room", "home", "home narrows the log to one owner DM: {kind: dm, session: ses_…}. For log."},
		{"visual", "file", "Absolute, or relative to your working directory."},
	} {
		if got := described[tc.tool][tc.field].Description; got != tc.want {
			t.Errorf("%s.%s: %q", tc.tool, tc.field, got)
		}
	}
}

// A field several actions take is described once in the toolkit's table and reads the same for each, still naming the
// actions that take it; what every action shares opens the tool's description once.
func TestSharedFieldsAndTheToolDescriptionComeFromTheToolkit(t *testing.T) {
	list, _, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range list {
		if item.Name != "computer" {
			continue
		}
		props := item.InputSchema.Properties
		for field, want := range map[string]string{
			"status": "A few words on what this is for, shown to the owner watching the computer. For click, drag, key, launch, move, open, scroll, stop and type.",
			"x":      "Desktop pixels from the top left of the 1280x800 desktop. For click, move, screenshot and scroll.",
			"width":  "A region's width, with x, y and height. For screenshot.",
			"button": "left (default), middle or right. For click.",
		} {
			if got := props[field].Description; got != want {
				t.Errorf("%s: %q", field, got)
			}
		}
		if !strings.HasPrefix(props["keys"].Description, "Key names: ") || !strings.HasSuffix(props["keys"].Description, "For click, drag, key and scroll.") {
			t.Errorf("keys: %q", props["keys"].Description)
		}
		const opening = "The box desktop is 1280x800, with Chromium in a window and a taskbar; any action but stop starts it.\n"
		if !strings.HasPrefix(item.Description, opening) || strings.Count(item.Description, "The box desktop is") != 1 {
			t.Fatalf("description %q", item.Description)
		}
		return
	}
	t.Fatal("no computer tool")
}

// A property only some of a tool's actions take names them, since the actions share one schema and each decodes strictly:
// pr open takes no repo.
func TestAPropertySomeActionsTakeNamesThem(t *testing.T) {
	tools, _, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		if tool.Name != "pr" {
			continue
		}
		repo := tool.InputSchema.Properties["repo"].Description
		if !strings.Contains(repo, "For ") || strings.Contains(repo, "open") || !strings.Contains(repo, "status") {
			t.Fatalf("repo says %q", repo)
		}
		if title := tool.InputSchema.Properties["title"].Description; !strings.HasSuffix(title, "For open.") {
			t.Fatalf("title says %q", title)
		}
		return
	}
	t.Fatal("no pr tool")
}

func TestPrStackActionAndGuidance(t *testing.T) {
	list, actions, err := registry()
	if err != nil {
		t.Fatal(err)
	}
	if actions["pr"]["stack"].op != "pr.stack" {
		t.Fatal("stack is not an operation")
	}
	want := "stack: make a GitHub stack from open PRs, bottom first; each PR's base is the PR below it, and you own each or are an ancestor of its owner. A stack can't change after creation. Merging it is root's."
	for _, tool := range list {
		if tool.Name == "pr" {
			for _, line := range strings.Split(tool.Description, "\n") {
				if line == want {
					return
				}
			}
			t.Fatalf("stack guidance missing: %s", tool.Description)
		}
	}
	t.Fatal("pr tool missing")
}
