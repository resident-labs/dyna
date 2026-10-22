package instructions

import (
	"strings"
	"testing"
)

func TestSnapshotIsStableBytes(t *testing.T) {
	a := Build(Section{Name: "Session rules", Text: "be clear"}, Section{Name: "Empty"}, Section{Name: "Room rules", Items: []string{"r1 one", "r2 two"}})
	b := Build(Section{Name: "Session rules", Text: "be clear"}, Section{Name: "Room rules", Items: []string{"r1 one", "r2 two"}})
	if a.Text() != b.Text() || a.Hash != b.Hash || a.Hash == "" || strings.Contains(a.Text(), "Empty") {
		t.Fatalf("same parts gave different snapshots:\n%q\n%q", a.Text(), b.Text())
	}
	if !strings.Contains(a.Text(), "be clear\n\nr1 one\nr2 two") {
		t.Fatalf("text = %q", a.Text())
	}
}

// The note names changes by section: rules added or removed by text, other
// sections with their new text. Equal snapshots need no note.
func TestNoteListsSectionChanges(t *testing.T) {
	frozen := Build(Section{Name: "Session rules", Text: "v1"}, Section{Name: "Room rules", Items: []string{"r1 one", "r2 two"}}, Section{Name: "Service contract", Text: "serve"})
	desired := Build(Section{Name: "Session rules", Text: "v1"}, Section{Name: "Room rules", Items: []string{"r2 two", "r3 three"}}, Section{Name: "Goal", Text: "g2 ship it"})
	if Note(frozen, frozen) != "" {
		t.Fatal("note for an unchanged snapshot")
	}
	note := Note(frozen, desired)
	for _, want := range []string{"[DYNA INSTRUCTIONS CHANGED]", `Room rules: added "r3 three"; removed "r1 one"`, "Service contract: removed", "Goal: now:\ng2 ship it"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "Session rules") {
		t.Errorf("note names an unchanged section:\n%s", note)
	}
}

// A section that appears or disappears still names its rules, and a changed
// text section names its changed paragraphs, so a person can read what
// changed without the whole section.
func TestNoteNamesItemsAndParagraphs(t *testing.T) {
	room := func(items ...string) Section {
		return Section{Name: "Room rules", Text: "[DYNA RULES] heading", Items: items}
	}
	session := func(text string) Section { return Section{Name: "Session rules", Text: text} }
	cases := []struct {
		name            string
		frozen, desired Snapshot
		want            []string
		not             string
	}{
		{"first rule", Build(session("a")), Build(session("a"), room("r1 Keep PRs small.")), []string{`- Room rules: added "r1 Keep PRs small."`}, "Room rules: now:"},
		{"last rule removed", Build(session("a"), room("r1 one", "r2 two")), Build(session("a")), []string{`- Room rules: removed "r1 one", "r2 two".`}, "Room rules: removed."},
		{"paragraph changed", Build(session("Keep it short.\n\nUse sol workers.\nThey are cheap.")), Build(session("Keep it short.\n\nUse luna workers.")),
			[]string{`- Session rules: added "Use luna workers."; removed "Use sol workers.…".`, "New text:\nUse luna workers."}, "Session rules: now:"},
	}
	for _, c := range cases {
		note := Note(c.frozen, c.desired)
		for _, want := range c.want {
			if !strings.Contains(note, want) {
				t.Errorf("%s: note lacks %q:\n%s", c.name, want, note)
			}
		}
		if strings.Contains(note, c.not) {
			t.Errorf("%s: note has %q:\n%s", c.name, c.not, note)
		}
	}
}
