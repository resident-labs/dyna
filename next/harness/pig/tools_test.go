package pig

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"

	"github.com/resident-labs/dyna/harness/pig/pigtest"
)

var readOnly = []string{"read", "grep", "find", "ls"}

func toolNames(t *testing.T, r pigtest.Request) []string {
	t.Helper()
	var names []string
	for _, raw := range r.Tools {
		var tool struct{ Name string }
		if err := json.Unmarshal(raw, &tool); err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	return names
}

// An exact tool list is all the model is offered, whatever an extension, a package or an MCP server would add, and whatever the
// conversation it resumes used before.
func TestAnExactToolListHoldsAcrossAResume(t *testing.T) {
	model := pigtest.NewModel([]pigtest.Block{{Text: "one"}}, []pigtest.Block{{Text: "two"}}, []pigtest.Block{{Text: "three"}})
	c := config(t, model, `{"fixture":{"command":"true","exposure":"direct"}}`)
	pigtest.Packages(t, c.AgentDir, pigtest.Package(t, pigtest.ToolExtension("package_tool", "x")))
	c.Extensions = []extension.InlineExtension{testTool("inline_tool", extension.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "x"}}})}
	// The conversation first ran with the box's usual tools, bash among them.
	usual, err := Open(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	start(t, usual, "first")
	_ = usual.Close()
	c.Tools, c.Bare = readOnly, true
	for i, prompt := range []string{"second", "third"} {
		h, err := Open(context.Background(), c)
		if err != nil {
			t.Fatal(err)
		}
		start(t, h, prompt)
		_ = h.Close()
		want := slices.Clone(readOnly)
		slices.Sort(want)
		if got := toolNames(t, model.Requests()[i+1]); !slices.Equal(got, want) {
			t.Fatalf("resume %d offered %v, want %v", i+1, got, want)
		}
	}
	if got := toolNames(t, model.Requests()[0]); !slices.Contains(got, "bash") {
		t.Fatalf("the usual tools lost bash: %v", got)
	}
}
