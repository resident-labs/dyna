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
	raw := `{"mcp":[{"name":"tool","command":"node","enabledHarnesses":["codex"],"imported":true,"references":{"codex":"old-config"},"importedHashes":{"codex":"old-hash"},"variants":{"codex":{"command":"retired"}}}],"skills":[{"name":"review","source":"/source","enabledHarnesses":["codex"],"imported":true,"variants":{"codex":"/retired"},"importedHashes":{"codex":"old-hash"}}]}`
	mustEdit(t, os.WriteFile(m.Path, []byte(raw), 0600))
	mustEdit(t, m.SetEnabled(authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner}), "mcp", "tool", "codex", false))
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

func TestRetireManagedSkillKeepsOwnerSkills(t *testing.T) {
	m := &ConfigManager{Path: filepath.Join(t.TempDir(), "catalog.json")}
	if err := m.RetireManagedSkill("orch", "src"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.Path); !os.IsNotExist(err) {
		t.Fatal("retire created owner configuration")
	}
	raw := `{"defaultSkillsSeeded":true,"mcp":[],"skills":[{"name":"orch","source":"src","enabledHarnesses":["grok"]},{"name":"orch","source":"mine","enabledHarnesses":["grok"]}]}`
	if err := os.WriteFile(m.Path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.RetireManagedSkill("orch", "src"); err != nil {
		t.Fatal(err)
	}
	c, err := m.Read()
	if err != nil || len(c.Skills) != 1 || c.Skills[0].Source != "mine" || c.DefaultSkillsSeeded {
		t.Fatalf("wrong catalog: %+v %v", c, err)
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
