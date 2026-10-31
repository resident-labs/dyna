package main

import (
	"database/sql"
	"errors"
	"slices"

	"github.com/resident-labs/dyna/contract"
	"testing"
)

func TestRuleUsesStoredAuthorNameWhenSetterIsUnknown(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE goal_rooms(room TEXT, body TEXT);
INSERT INTO goal_rooms VALUES('r','{"rules":{"r1":{"id":"r1","text":"Rule","setter":"old-id","setterName":"room:author"}}}');`)
	if err != nil {
		t.Fatal(err)
	}
	author := func(value string) (contract.ImportedAuthor, error) {
		if value == "room:author" {
			return contract.ImportedAuthor{Kind: contract.PrincipalSession, Label: "room:author"}, nil
		}
		return contract.ImportedAuthor{}, errors.New("unknown")
	}
	got, err := readRules(db, "r", author)
	if err != nil || len(got) != 1 || got[0].By.Label != "room:author" {
		t.Fatalf("rules = %+v, %v", got, err)
	}
}

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
		for _, field := range decisions[item.Type] {
			if !slices.Contains(item.Dropped, field) {
				t.Errorf("%s.%s is not dropped", item.Type, field)
			}
		}
		if item.Type == "TrackedPR" && !slices.Contains(item.Carried, "Hold") {
			t.Error("TrackedPR.Hold is not carried")
		}
		if item.Type == "Room" && !slices.Contains(item.Gap, "AttributionPlacement") {
			t.Error("attribution placement gap was hidden")
		}
		if item.Type == "Hook" && !slices.Contains(item.Gap, "Command") {
			t.Error("hook command gap was hidden")
		}
	}
}
