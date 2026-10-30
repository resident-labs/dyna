package slashcmd

import (
	"os"
	"path/filepath"
	"testing"

	harnessconfig "dyna-agent/internal/harness/config"
)

func TestSkillsListsEnabledSkills(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\ndescription: Examine code\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := Skills([]harnessconfig.ManagedSkill{
		{Name: "review", Source: dir, Enabled: true},
		{Name: "disabled", Source: dir},
	})
	if len(got) != 1 || got[0].Name != "review" || got[0].Source != Skill || got[0].Description != "Examine code" {
		t.Fatalf("unexpected commands: %+v", got)
	}
}
