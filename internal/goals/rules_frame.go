package goals

import (
	"dyna-agent/internal/instructions"
	"strings"
)

// RuleLines renders one line per rule: ID, text and author. Every accepted
// rule stays in the list, even when the owner lowers the cap below its size.
func RuleLines(rules []Rule) []string {
	var lines []string
	for _, rule := range rules {
		text := strings.Join(strings.Fields(rule.Text), " ")
		author := "owner"
		if rule.Setter != "owner" {
			author = "agent " + strings.Join(strings.Fields(rule.SetterName), " ")
		}
		lines = append(lines, strings.Join(strings.Fields(rule.ID), " ")+" "+text+" · "+author)
	}
	return lines
}

// GlobalRulesMarker marks the global rules file older Dyna versions wrote into
// account homes; a generation removes files that carry it.
const GlobalRulesMarker = "<!-- Managed by Dyna: global rules. -->"

// RoomOfScope is the room a rule scope belongs to, or "" for global rules.
func RoomOfScope(scope string) string {
	if scope == Global {
		return ""
	}
	return scope
}

// InstructionSections are the global and room rules as instructions
// sections, one item per rule. Sessions and workflow workers both use them.
func (s *Store) InstructionSections(roomID string) []instructions.Section {
	section := func(name, scope, heading string) instructions.Section {
		rules, err := s.Rules(scope)
		if scope == "" || err != nil || len(rules) == 0 {
			return instructions.Section{Name: name}
		}
		return instructions.Section{Name: name, Text: heading, Items: RuleLines(rules)}
	}
	return []instructions.Section{
		section("Global rules", Global, "[DYNA GLOBAL RULES] Standing rules for every Dyna room, set by the owner and agents. Follow them. Owner rules win if two conflict. Room rules win over these. Change them with `dyna rules --global`."),
		section("Room rules", roomID, "[DYNA RULES] Standing rules for this room, set by the owner and agents. Follow them. Owner rules win if two conflict. Room rules win over global rules. Change them with `dyna rules`."),
	}
}
