package main

import (
	"fmt"
	"os"

	"dyna-agent/internal/skills"
	"github.com/resident-labs/dyna/contract"
)

func readSkills(data string, entries []skillMapping) ([]contract.SkillBundle, error) {
	root, err := os.OpenRoot(data)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var result []contract.SkillBundle
	for _, item := range entries {
		skill := contract.SkillBundle{Name: item.Name, Package: item.Package}
		if item.Path != "" {
			body, err := root.ReadFile(item.Path)
			if err != nil {
				return nil, fmt.Errorf("read captured skill: %w", err)
			}
			skill.Body = string(body)
			skill.Description = skills.Description(skill.Body)
		}
		result = append(result, skill)
	}
	return result, nil
}
