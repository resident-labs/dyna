package main

import (
	"slices"
	"testing"
)

func TestApprovedInventoryDecisions(t *testing.T) {
	inventories := inventory()
	decisions := map[string][]string{
		"Session":   {"HarnessSession", "Model", "SpawnBrief"},
		"Settings":  {"TestSlots", "VoiceProvider", "Extra"},
		"TrackedPR": {"Head", "Reviewer", "Title"},
		"Question":  {"Human"},
		"Profile":   {"Output", "ExtraArgs", "Env"},
		"Room":      {"LibraryPin", "CreatedBy", "Pinned"},
	}
	for _, item := range inventories {
		if len(item.Gap) != 0 {
			t.Errorf("%s has unclassified fields: %v", item.Type, item.Gap)
		}
		for _, field := range decisions[item.Type] {
			if !slices.Contains(item.Dropped, field) {
				t.Errorf("%s.%s is not dropped", item.Type, field)
			}
		}
		if item.Type == "TrackedPR" && !slices.Contains(item.Carried, "Hold") {
			t.Error("TrackedPR.Hold is not carried")
		}
		if item.Type == "Room" && !slices.Contains(item.Handoff, "AttributionPlacement") {
			t.Error("attribution placement gap was hidden")
		}
		if item.Type == "Hook" && !slices.Contains(item.Handoff, "Command") {
			t.Error("hook command gap was hidden")
		}
	}
}
