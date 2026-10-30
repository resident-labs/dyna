package main

import (
	"fmt"
	"os"
	"path/filepath"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/room"
)

// qaSeedFixtures writes only to an existing, empty data directory. The caller
// must create that directory for the isolated QA engine and remove it afterward.
func qaSeedFixtures(dataDir string) error {
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return fmt.Errorf("read QA data directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("QA data directory must be empty")
	}
	rooms := &room.Store{Root: filepath.Join(dataDir, "rooms")}
	rec, err := rooms.Create(room.CreateInput{Name: "qa-demo", CreatedBy: "owner"})
	if err != nil {
		return err
	}
	for _, post := range []room.PostInput{
		{From: "owner", Human: true, To: []string{rec.Name}, Text: "Please check the sample change."},
		{From: rec.Name + ":root", To: []string{rec.Name}, Text: "The sample change is ready for review."},
	} {
		if _, err := rooms.Post(rec.ID, post); err != nil {
			return err
		}
	}
	goalStore := &goals.Store{Root: filepath.Join(dataDir, "goals")}
	goal, err := goalStore.Add(rec.ID, "owner", goals.Input{Text: "Review the sample change", Shared: true})
	if err != nil {
		return err
	}
	const key = "github.com/example/widgets#7"
	pr := &state.Record{Key: key, Host: "github.com", Repo: "example/widgets", Number: 7,
		URL: "https://github.com/example/widgets/pull/7", Title: "Improve the sample page",
		RoomID: rec.ID, GoalID: goal.ID, State: state.PROpen, Branch: "sample-change", Base: "main"}
	return state.Write(state.Root(dataDir), state.State{Version: 1, Records: map[string]*state.Record{key: pr}})
}
