// Package skills turns Dyna's global skill catalog into a small standing
// instruction section. Every session and workflow worker uses the same
// rendering, so a skill reaches both kinds of worker the same way.
package skills

import (
	"os"
	"path/filepath"
	"strings"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/instructions"
)

// Section lists the enabled skills, one item each, with the description from
// SKILL.md and the absolute path of the file. The skill body never enters the
// instructions: the agent reads the file only when a task matches. Each item
// is separately tracked, so adding, removing or changing a skill reaches a
// running session as a normal change note.
func Section(entries []harnessconfig.ManagedSkill) instructions.Section {
	section := instructions.Section{Name: "Skills"}
	for _, skill := range entries {
		if !skill.Enabled || skill.Name == "" || !filepath.IsAbs(skill.Source) {
			continue
		}
		path := filepath.Join(skill.Source, "SKILL.md")
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		description := Description(string(raw))
		if description == "" {
			description = "No description in SKILL.md."
		}
		section.Items = append(section.Items, "- "+skill.Name+": "+description+" · "+path)
	}
	if len(section.Items) > 0 {
		section.Text = "[DYNA SKILLS]\nSkills enabled by Dyna. Read a skill's SKILL.md and its files only when the task matches. Look up or change skills with `dyna skill list`."
	}
	return section
}

// Description returns the one-line description of a SKILL.md: its frontmatter
// description when it has a single-line one, otherwise the first body line
// that is not a heading. It never returns more than one line.
func Description(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := 0
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for i := 1; i < len(lines); i++ {
			if strings.TrimSpace(lines[i]) == "---" {
				start = i + 1
				break
			}
			key, value, ok := strings.Cut(lines[i], ":")
			if !ok || strings.TrimSpace(key) != "description" {
				continue
			}
			value = strings.TrimSpace(value)
			// A YAML block scalar moves its text to later lines, which this
			// one-line reader does not take. Fall back to the body instead.
			if value == "" || strings.HasPrefix(value, ">") || strings.HasPrefix(value, "|") {
				continue
			}
			if description := oneLine(unquote(value)); description != "" {
				return description
			}
		}
	}
	for _, line := range lines[start:] {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || line == "---" {
			continue
		}
		return oneLine(line)
	}
	return ""
}

// oneLine collapses whitespace so a value stays on one instructions line.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// unquote strips the matching quotes of a single-line YAML value.
func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
