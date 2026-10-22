package worktrees

import "testing"

// decide marks only a clean, merged tree kept for an open owner as OwnerOpen.
func TestDecideMarksCleanTreeWithOpenOwner(t *testing.T) {
	open := func(string) (bool, error) { return false, nil }
	if got := decide(Tree{Merged: true, sessionID: "ses_a"}, open); !got.OwnerOpen || got.Reason != "owning session is open" {
		t.Fatalf("clean tree with open owner: %+v", got)
	}
	if got := decide(Tree{Merged: true, Dirty: true, sessionID: "ses_a"}, open); got.OwnerOpen {
		t.Fatalf("dirty tree marked expected: %+v", got)
	}
	if got := decide(Tree{sessionID: "ses_a"}, open); got.OwnerOpen {
		t.Fatalf("unmerged tree marked expected: %+v", got)
	}
}
