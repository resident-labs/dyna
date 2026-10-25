package roomsvc

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// TestIssueTextKeepsAnEmptyTypedNote keeps the filing summary out of the note.
// A typed issue event carries the note, even when the filer gave none; only a
// post stored before the typed events reads its text as the note.
func TestIssueTextKeepsAnEmptyTypedNote(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	i, err := e.svc.rooms().FileIssue(r.ID, room.IssueInput{Title: "Fix parser", Filer: r.Name})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := e.svc.rooms().Message(r.ID, i.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := issueText(msg); got != "" {
		t.Fatalf("an issue filed without a note reports %q", got)
	}
	if detail := issueDetail(i, issueText(msg)); strings.Contains(detail, "filed") || !strings.HasPrefix(detail, "Local issue "+i.Label()) {
		t.Fatalf("detail = %q", detail)
	}
	// TODO: legacy/shim remove later — posts stored before the typed events.
	if got := issueText(room.Message{Text: "the old note"}); got != "the old note" {
		t.Fatalf("a legacy post reads %q", got)
	}
}
