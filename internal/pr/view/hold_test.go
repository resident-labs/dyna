package view

import (
	"testing"

	"dyna-agent/internal/pr/state"
)

// A branch name matches only the open PR in the named repository, so two PRs
// that share a branch in different repositories do not share a hold.
func TestHoldForBranchStaysInItsRepository(t *testing.T) {
	records := map[string]*state.Record{
		"a": {Key: "a", RoomID: "room-1", Repo: "owner/a", Branch: "fix/held", State: state.PROpen, Hold: &state.Hold{Reason: "in a"}},
		"b": {Key: "b", RoomID: "room-1", Repo: "owner/b", Branch: "fix/held", State: state.PROpen, Hold: &state.Hold{Reason: "in b"}},
	}
	if got := holdFor(records, "room-1", "owner/b", "fix/held"); got == nil || got.Reason != "in b" {
		t.Fatalf("holdFor(owner/b, fix/held) = %+v, want the hold on owner/b", got)
	}
	if got := holdFor(records, "room-1", "owner/c", "fix/held"); got != nil {
		t.Fatalf("holdFor(owner/c, fix/held) = %+v, want no hold", got)
	}
}
