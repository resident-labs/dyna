package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
)

func TestCatalogDropsRetiredMetadata(t *testing.T) {
	m, _, _ := editManager(t)
	raw := `{"mcp":[{"name":"tool","command":"node","enabledHarnesses":["claude-code"],"imported":true,"references":{"claude-code":"old-config"},"importedHashes":{"claude-code":"old-hash"},"variants":{"claude-code":{"command":"retired"}}}],"skills":[{"name":"review","source":"/source","enabledHarnesses":["claude-code"],"imported":true,"variants":{"claude-code":"/retired"},"importedHashes":{"claude-code":"old-hash"}}]}`
	mustEdit(t, os.WriteFile(m.Path, []byte(raw), 0600))
	mustEdit(t, m.SetEnabled(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "mcp", "tool", "claude-code", false))
	saved := string(fileBytes(t, m.Path))
	for _, key := range []string{"imported", "references", "importedHashes", "variants"} {
		if strings.Contains(saved, `"`+key+`"`) {
			t.Fatalf("retired metadata %q survived catalog update", key)
		}
	}
	c, err := m.Read()
	mustEdit(t, err)
	if len(c.MCP) != 1 || c.MCP[0].Command != "node" || len(c.MCP[0].EnabledHarnesses) != 0 || len(c.Skills) != 1 || c.Skills[0].Source != "/source" {
		t.Fatalf("current catalog fields changed: %+v", c)
	}
}

func TestReadMigratesLegacySkillHarnessScope(t *testing.T) {
	m := &ConfigManager{Path: filepath.Join(t.TempDir(), "catalog.json")}
	raw := `{"skills":[{"name":"on","source":"/on","enabledHarnesses":["grok"]},{"name":"off","source":"/off","enabledHarnesses":[]},{"name":"absent","source":"/absent"},{"name":"explicit","source":"/explicit","enabled":true,"enabledHarnesses":[]}]}`
	mustEdit(t, os.WriteFile(m.Path, []byte(raw), 0600))
	c, err := m.Read()
	mustEdit(t, err)
	want := map[string]bool{"on": true, "off": false, "absent": false, "explicit": true}
	if len(c.Skills) != len(want) {
		t.Fatalf("got %d skills: %+v", len(c.Skills), c.Skills)
	}
	for _, skill := range c.Skills {
		if skill.Enabled != want[skill.Name] {
			t.Fatalf("skill %q enabled = %v", skill.Name, skill.Enabled)
		}
	}
	// One update writes the current shape and keeps the migrated flag.
	mustEdit(t, m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "new", Source: skillSource(t), Enabled: true}))
	if saved := string(fileBytes(t, m.Path)); strings.Contains(saved, "enabledHarnesses") || strings.Contains(saved, "defaultSkillsSeeded") {
		t.Fatalf("legacy field survived an update: %s", saved)
	}
	c, err = m.Read()
	mustEdit(t, err)
	for _, skill := range c.Skills {
		if skill.Enabled != (skill.Name == "new" || want[skill.Name]) {
			t.Fatalf("skill %q lost its enabled state: %+v", skill.Name, c.Skills)
		}
	}
}

func TestCatalogIsGlobalForSkills(t *testing.T) {
	m := &ConfigManager{Path: filepath.Join(t.TempDir(), "catalog.json")}
	mustEdit(t, m.AddSkill(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), ManagedSkill{Name: "review", Source: skillSource(t), Enabled: true}))
	for _, harness := range []string{"", "pi", "claude-code"} {
		snap, err := m.Catalog(harness)
		mustEdit(t, err)
		if len(snap.Skills) != 1 || snap.Skills[0].Name != "review" {
			t.Fatalf("harness %q hid the global skill: %+v", harness, snap.Skills)
		}
	}
}

func TestRemoveSeededSkillMatchesNameAndSource(t *testing.T) {
	m := &ConfigManager{Path: filepath.Join(t.TempDir(), "catalog.json")}
	seeded := "/dyna/config/skills/load-dyna-orchestrator"
	raw := `{"skills":[{"name":"load-dyna-orchestrator","source":"/dyna/config/skills/load-dyna-orchestrator","enabled":true},{"name":"load-dyna-orchestrator","source":"/owner/skill","enabled":true},{"name":"other","source":"/other","enabled":true}]}`
	mustEdit(t, os.WriteFile(m.Path, []byte(raw), 0600))
	mustEdit(t, m.RemoveSeededSkill("load-dyna-orchestrator", seeded))
	c, err := m.Read()
	mustEdit(t, err)
	if len(c.Skills) != 2 || c.Skills[0].Source != "/owner/skill" || c.Skills[1].Name != "other" {
		t.Fatalf("seeded removal changed other skills: %+v", c.Skills)
	}
	// A second run finds no match and leaves the file alone.
	before := fileBytes(t, m.Path)
	mustEdit(t, m.RemoveSeededSkill("load-dyna-orchestrator", seeded))
	if after := fileBytes(t, m.Path); string(after) != string(before) {
		t.Fatal("a missing seeded entry rewrote the catalog")
	}
}

func skillSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("skill"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}
