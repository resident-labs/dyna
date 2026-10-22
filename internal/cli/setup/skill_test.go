package setup

import (
	"strings"
	"testing"
)

func TestSkillIsAPointerToTheGuide(t *testing.T) {
	if !strings.Contains(skillBody, "Run `dyna guide` first and follow it.") {
		t.Fatal("skill must point at dyna guide")
	}
	if words := len(strings.Fields(skillBody)); words > 40 {
		t.Fatalf("skill is %d words; guidance belongs in dyna guide", words)
	}
}

func TestSkillFrontmatterDrivesExplicitDiscovery(t *testing.T) {
	for _, required := range []string{
		"name: load-dyna-orchestrator",
		"the user asks for Dyna, a workflow, subagents, or multi-model orchestration",
		"dyna guide",
	} {
		if !strings.Contains(skillFrontmatter, required) {
			t.Errorf("frontmatter is missing %q", required)
		}
	}
}

func TestSkillShowPrintsFrontmatterAndBody(t *testing.T) {
	var out strings.Builder
	cmd := NewSkillCommand()
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if out.String() != skillFrontmatter+skillBody {
		t.Fatal("show did not print the shipped skill")
	}
}
