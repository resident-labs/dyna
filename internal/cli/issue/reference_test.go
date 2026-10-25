package issue

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

// TestIssueReferenceShowsTheIssue stops the D-61 mistake: a lone issue
// reference reads that issue instead of filing an empty one titled after it,
// and a whole title that is only an issue number is refused.
func TestIssueReferenceShowsTheIssue(t *testing.T) {
	f := newIssueFixture(t)
	filed := f.file(t)
	for _, args := range [][]string{
		{filed.Label()},
		{strings.ToLower(filed.Label())},
		{strings.Replace(filed.Label(), "D-", "d", 1)},
		{filed.ID},
		{"show", filed.Label()},
	} {
		out, err := f.run(args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		for _, want := range []string{filed.Label(), "Fix parser", "details"} {
			if !strings.Contains(out, want) {
				t.Fatalf("%v printed %q, want %q", args, out, want)
			}
		}
	}
	if rows, err := f.rooms.Issues(f.rec.ID, true); err != nil || len(rows) != 1 {
		t.Fatalf("a reference filed an issue: %+v, %v", rows, err)
	}
	if _, err := f.rooms.FileIssue(f.rec.ID, room.IssueInput{Title: "D-9", Filer: f.rec.Name}); err == nil || !strings.Contains(err.Error(), "dyna issue show D-9") {
		t.Fatalf("a bare issue number became a title: %v", err)
	}
}
