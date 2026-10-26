package subagent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/instructions"
	dynaskills "dyna-agent/internal/skills"
)

func skillSourceFixture(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestSkillsSectionListsEnabledSkills(t *testing.T) {
	e := newTestEnv(t)
	front := skillSourceFixture(t, "---\nname: front\ndescription: \"Reviews a change\"\n---\n\n# Body\nSecret body line\n")
	body := skillSourceFixture(t, "# Title\n\nFirst body line\nsecond line\n")
	block := skillSourceFixture(t, "---\ndescription: >\n  Folded value\n---\n# Heading\nReal first line\n")
	missing := filepath.Join(t.TempDir(), "gone")
	e.m.Skills = func() ([]harnessconfig.ManagedSkill, error) {
		return []harnessconfig.ManagedSkill{
			{Name: "front", Source: front, Enabled: true},
			{Name: "body", Source: body, Enabled: true},
			{Name: "block", Source: block, Enabled: true},
			{Name: "missing", Source: missing, Enabled: true},
			{Name: "off", Source: front, Enabled: false},
		}, nil
	}
	meta := Meta{ID: "ses_20261019-000001-aaaaaaaa", Name: "reader", Room: e.room.Name, RoomID: e.room.ID, Depth: 1}
	snapshot := e.m.sessionInstructions(meta, "owner", 3, "")
	text := snapshot.Text()
	for _, want := range []string{
		"[DYNA SKILLS]",
		"- front: Reviews a change · " + filepath.Join(front, "SKILL.md"),
		"- body: First body line · " + filepath.Join(body, "SKILL.md"),
		"- block: Real first line · " + filepath.Join(block, "SKILL.md"),
	} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions missing %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"Secret body line", "- missing:", "- off:", "block: >", "block: Folded"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("instructions contain %q:\n%s", unwanted, text)
		}
	}
	// The section sits directly after the Feature index and before the jobs.
	index := func(name string) int {
		return slices.IndexFunc(snapshot.Sections, func(s instructions.Section) bool { return s.Name == name })
	}
	skills := index("Skills")
	if skills < 0 {
		t.Fatal("skills section missing")
	}
	for _, earlier := range []string{"Subagent profiles", "Feature index"} {
		if i := index(earlier); i >= 0 && i > skills {
			t.Errorf("skills section comes before %q", earlier)
		}
	}
	for _, later := range []string{"Your running jobs", "Open questions", "Service contract", "Consolidation charter", "Goal and brief"} {
		if i := index(later); i >= 0 && i < skills {
			t.Errorf("skills section comes after %q", later)
		}
	}
}

func TestSkillChangesReachTheNextTurnAsANote(t *testing.T) {
	e := newTestEnv(t)
	info := completedFixtureSession(t, e)
	source := skillSourceFixture(t, "---\ndescription: First\n---\n")
	e.m.Skills = func() ([]harnessconfig.ManagedSkill, error) {
		return []harnessconfig.ManagedSkill{{Name: "review", Source: source, Enabled: true}}, nil
	}
	frozen := e.m.desiredInstructions(info.Meta)
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\ndescription: Second\n---\n"), 0600); err != nil {
		t.Fatal(err)
	}
	desired := e.m.desiredInstructions(info.Meta)
	changes := instructions.Changes(frozen, desired)
	if len(changes) != 1 || changes[0].Name != "Skills" || changes[0].Action != instructions.ChangeItems {
		t.Fatalf("skill change = %+v", changes)
	}
	path := filepath.Join(source, "SKILL.md")
	if len(changes[0].Added) != 1 || changes[0].Added[0] != "- review: Second · "+path {
		t.Fatalf("added = %v", changes[0].Added)
	}
	if len(changes[0].Removed) != 1 || changes[0].Removed[0] != "- review: First · "+path {
		t.Fatalf("removed = %v", changes[0].Removed)
	}
	// Removing the last enabled skill removes the section and names its line.
	e.m.Skills = func() ([]harnessconfig.ManagedSkill, error) {
		return []harnessconfig.ManagedSkill{{Name: "review", Source: source, Enabled: false}}, nil
	}
	changes = instructions.Changes(desired, e.m.desiredInstructions(info.Meta))
	if len(changes) != 1 || changes[0].Name != "Skills" || changes[0].Action != instructions.ChangeRemoved {
		t.Fatalf("skill removal = %+v", changes)
	}
	if len(changes[0].Removed) != 1 || changes[0].Removed[0] != "- review: Second · "+path {
		t.Fatalf("removed lines = %v", changes[0].Removed)
	}
}

func TestSkillDescriptionParsing(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"frontmatter", "---\nname: x\ndescription: Plain words\n---\nBody\n", "Plain words"},
		{"single quotes", "---\ndescription: 'Quoted words'\n---\n", "Quoted words"},
		{"folded marker", "---\ndescription: >\n  Folded\n---\n# Head\nBody line\n", "Body line"},
		{"literal marker", "---\ndescription: |-\n  Block\n---\n# Head\nBody line\n", "Body line"},
		{"body fallback", "# Head\n\nFirst body line\nsecond\n", "First body line"},
		{"headings only", "# Head\n## More\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dynaskills.Description(tc.body); got != tc.want {
				t.Fatalf("description = %q, want %q", got, tc.want)
			}
		})
	}
}
