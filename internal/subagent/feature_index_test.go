package subagent

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/instructions"
	"dyna-agent/internal/library"
	"dyna-agent/internal/repoid"
	"dyna-agent/internal/room"
)

func TestFeatureIndexListsFeaturesAndDriftsAsANote(t *testing.T) {
	e := newTestEnv(t)
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "c"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	rec, err := e.m.rooms().Create(room.CreateInput{Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	meta := Meta{ID: "ses_20261020-000001-aaaaaaaa", Name: "reader", Room: rec.Name, RoomID: rec.ID, Depth: 1}
	if text := e.m.sessionInstructions(meta, "owner", 3, "").Text(); strings.Contains(text, "[DYNA FEATURES]") {
		t.Fatal("a repository without a map got a feature index")
	}

	r, err := repoid.Resolve(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	lib := library.Store{DataDir: filepath.Dir(e.m.store().root()), Actor: authority.Current()}
	for _, entry := range []library.Entry{
		{ID: "goals", Name: "Goals", Kind: "feature", Summary: "Long-lasting room tasks.", Owns: []string{"goals/**"}},
		{ID: "keeper", Name: "Keeper", Kind: "service", Parent: "goals", Summary: "Checks on goals.", Owns: []string{"keeper/**"}},
		{ID: "journal", Name: "Journal", Kind: "feature", Parent: "keeper", Summary: "Goal journal passes.", Owns: []string{"journal/**"}},
		{ID: "rooms", Name: "Rooms", Kind: "feature", Summary: "Durable feeds.", Owns: []string{"rooms/**"}},
	} {
		if _, err := lib.Add(r.ID, entry); err != nil {
			t.Fatal(err)
		}
	}
	frozen := e.m.sessionInstructions(meta, "owner", 3, "")
	text := frozen.Text()
	for _, want := range []string{"[DYNA FEATURES]", "dyna library show <id>", "dyna library list --tree"} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions lack %q", want)
		}
	}
	var items []string
	for _, sec := range frozen.Sections {
		if sec.Name == "Feature index" {
			items = sec.Items
		}
	}
	// Children ride on their top-level ancestor's line, flattened and sorted
	// by id; a childless feature keeps its line short.
	if want := []string{
		"- goals: Long-lasting room tasks. Children: journal, keeper.",
		"- rooms: Durable feeds.",
	}; !slices.Equal(items, want) {
		t.Errorf("feature index items:\n%s\nwant:\n%s", strings.Join(items, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(text, "Checks on goals.") || strings.Contains(text, "goals/**") {
		t.Fatal("the index holds a child summary or full entry detail")
	}

	// A changed summary reaches a running session as a note naming the line.
	if _, err := lib.Update(r.ID, "rooms", func(e *library.Entry) error { e.Summary = "Durable feeds with mentions."; return nil }); err != nil {
		t.Fatal(err)
	}
	note := instructions.Note(frozen, e.m.sessionInstructions(meta, "owner", 3, ""))
	if !strings.Contains(note, `Feature index: added "- rooms: Durable feeds with mentions."; removed "- rooms: Durable feeds."`) {
		t.Fatalf("note:\n%s", note)
	}

	// A child summary edit stays quiet: parent lines carry ids, not summaries.
	frozen = e.m.sessionInstructions(meta, "owner", 3, "")
	if _, err := lib.Update(r.ID, "keeper", func(e *library.Entry) error { e.Summary = "Checks on goals daily."; return nil }); err != nil {
		t.Fatal(err)
	}
	if note := instructions.Note(frozen, e.m.sessionInstructions(meta, "owner", 3, "")); note != "" {
		t.Fatalf("a child summary edit drifted:\n%s", note)
	}

	// Moving a child changes exactly the two parent lines, nested children
	// following their ancestor.
	frozen = e.m.sessionInstructions(meta, "owner", 3, "")
	if _, err := lib.Update(r.ID, "keeper", func(e *library.Entry) error { e.Parent = "rooms"; return nil }); err != nil {
		t.Fatal(err)
	}
	note = instructions.Note(frozen, e.m.sessionInstructions(meta, "owner", 3, ""))
	if want := `Feature index: added "- goals: Long-lasting room tasks.", "- rooms: Durable feeds with mentions. Children: journal, keeper."; removed "- goals: Long-lasting room tasks. Children: journal, keeper.", "- rooms: Durable feeds with mentions.".`; !strings.Contains(note, want) {
		t.Fatalf("note:\n%s", note)
	}
}
