package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/harness/config"
)

func TestDesktopCatalogAddStartsDisabledAndRemoveDeletesOnlyCatalogEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := testPrivateRuntime(t)
	source := t.TempDir()
	skillPath := filepath.Join(source, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("skill fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.AddHarnessMCP(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), config.ManagedMCP{Name: "fixture", MCPDefinition: config.MCPDefinition{Command: "node", Args: []string{"fixture.js"}}, EnabledHarnesses: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.MCP) != 1 || len(snapshot.MCP[0].EnabledHarnesses) != 0 {
		t.Fatal("new MCP was enabled without a toggle", snapshot.MCP)
	}
	snapshot, err = app.AddHarnessSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), config.ManagedSkill{Name: "fixture-skill", Source: source, EnabledHarnesses: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, skill := range snapshot.Skills {
		if skill.Name == "fixture-skill" {
			found = true
			if len(skill.EnabledHarnesses) != 0 {
				t.Fatal("new skill enabled")
			}
		}
	}
	if !found {
		t.Fatal("added skill missing")
	}
	if _, err = app.RemoveHarnessConfig(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "skill", "fixture-skill", ""); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(skillPath)
	if err != nil || string(raw) != "skill fixture" {
		t.Fatal("removal changed the source skill", err)
	}
	snapshot, err = app.RemoveHarnessConfig(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "mcp", "fixture", "")
	if err != nil || len(snapshot.MCP) != 0 {
		t.Fatal(snapshot.MCP, err)
	}
}
