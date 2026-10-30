package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
)

var slotCommandName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// loadTestSlots validates the hand-edited settings at their input boundary.
func (v *Values) loadTestSlots(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		return fmt.Errorf("testSlots must be an object")
	}
	var section struct {
		Slots    json.RawMessage `json:"slots"`
		Commands json.RawMessage `json:"commands"`
	}
	if err := json.Unmarshal(data, &section); err != nil {
		return fmt.Errorf("testSlots must be an object: %w", err)
	}
	if section.Slots != nil {
		if bytes.Equal(section.Slots, []byte("null")) || json.Unmarshal(section.Slots, &v.TestSlots.Slots) != nil || v.TestSlots.Slots < 0 {
			return fmt.Errorf("testSlots.slots must be a non-negative integer")
		}
	}
	if section.Commands != nil {
		if bytes.Equal(section.Commands, []byte("null")) {
			return fmt.Errorf("testSlots.commands must be an array")
		}
		var commands []struct {
			Command *string `json:"command"`
			Arg     *string `json:"arg"`
		}
		if err := json.Unmarshal(section.Commands, &commands); err != nil {
			return fmt.Errorf("testSlots.commands must be an array of command rules: %w", err)
		}
		rules := make([]TestSlotCommand, 0, len(commands))
		seen := make(map[string]bool)
		for _, rule := range commands {
			if rule.Command == nil || rule.Arg == nil {
				return fmt.Errorf("each testSlots.commands rule needs command and arg strings")
			}
			name := *rule.Command
			if !slotCommandName.MatchString(name) || name == "dyna" || name == "git" || name == "systemd-run" || seen[name] {
				return fmt.Errorf("invalid or duplicate testSlots command %q", name)
			}
			seen[name] = true
			rules = append(rules, TestSlotCommand{Command: name, Arg: *rule.Arg})
		}
		v.TestSlots.Commands = rules
	}
	return nil
}
