// Package slashcmd describes the commands available in a room.
package slashcmd

import (
	"os"
	"path/filepath"

	harnessconfig "dyna-agent/internal/harness/config"
	"dyna-agent/internal/skills"
)

type Source string

const (
	Workflow  Source = "workflow"
	Skill     Source = "skill"
	MCPPrompt Source = "mcp-prompt"
)

type Arg struct {
	Name        string `json:"name"`
	Required    bool   `json:"required"`
	Description string `json:"description"`
}

type Command struct {
	Name        string `json:"name"`
	Source      Source `json:"source"`
	Description string `json:"description"`
	Args        []Arg  `json:"args"`
}

// Skills lists enabled skills whose instruction files are available.
func Skills(entries []harnessconfig.ManagedSkill) []Command {
	out := []Command{}
	for _, entry := range entries {
		if !entry.Enabled || entry.Name == "" || !filepath.IsAbs(entry.Source) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(entry.Source, "SKILL.md"))
		if err != nil {
			continue
		}
		out = append(out, Command{Name: entry.Name, Source: Skill, Description: skills.Description(string(raw)), Args: []Arg{}})
	}
	return out
}
