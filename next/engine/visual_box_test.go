//go:build linux

package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// A check on real Podman with the real box agent, the Pi the image ships and Chromium: in the same message as the write that
// makes its file, as Pi runs such a message's calls in parallel; by a relative path, which starts where Pi's write does;
// and with no action, as Pi's own validation sees it.
func TestVisualCheckInARealBox(t *testing.T) {
	c := newRealCrew(t, map[string]string{"README.md": "repository\n"})
	c.git(c.cache, "fetch", c.origin, "+refs/heads/*:refs/heads/*")
	world := newAddonWorld(c)
	write := func(path string) crewTool {
		input, _ := json.Marshal(map[string]string{"path": path, "content": `<!doctype html><html lang="en"><title>Plan</title><body><h1>Plan</h1></body></html>`})
		return crewTool{"write", string(input)}
	}
	check := func(file string) crewTool {
		input, _ := json.Marshal(map[string]string{"action": "check", "file": file})
		return crewTool{"mcp__dyna__visual", string(input)}
	}
	c.upstream.mu.Lock()
	c.upstream.batch["task-visual"] = 2
	c.upstream.tools["task-visual"] = []crewTool{
		{"tool_search", toolInput("query", "visual")}, {"job", toolInput("action", "list")},
		write("/box/work/same-message/plan.html"), check("/box/work/same-message/plan.html"),
		write("relative/plan.html"), check("relative/plan.html"),
		{"mcp__dyna__visual", toolInput("file", "/box/work/same-message/plan.html")},
	}
	c.upstream.mu.Unlock()
	world.boxOf(c.root)
	world.ask(c.root, "task-visual")
	_, messages := c.upstream.lastBody("task-visual")
	if strings.Contains(messages, "no such file") || strings.Contains(messages, "Validation failed") || strings.Count(messages, "renders") != 3 {
		t.Fatalf("the checks did not read their files:\n%s", messages)
	}
}
