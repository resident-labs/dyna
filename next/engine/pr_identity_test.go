package engine

import (
	"testing"

	"github.com/resident-labs/dyna/contract"
	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

func TestPrTrackCaseVariantCannotClaimExistingPR(t *testing.T) {
	r := newPRRig(t)
	// PrOpen records the room/session spelling, not GitHub's canonical base repo spelling.
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 7, Title: "existing child PR"})
	// A caller's room can legitimately list GitHub's canonical mixed-case spelling.
	if err := r.s.store.AddRoomRepo(r.ctx, r.room, "Owner/Repo", false); err != nil {
		t.Fatal(err)
	}
	r.github.Add(fakegithub.Pull{Repo: "Owner/Repo", Number: 7, Title: "existing child PR"})
	// The root is a different session; it is not the recorded PR owner.
	_, e := r.s.PrTrack(r.ctx, sessionCaller(r.root), contract.PrTrackRequest{Repo: "Owner/Repo", Number: 7})

	if e == nil {
		t.Fatal("case-variant tracking claimed another session's open PR")
	}
	for _, id := range []contract.SessionID{r.child, r.root} {
		owns, err := r.s.store.OwnsPR(r.ctx, "owner/repo", id, 7)
		if err != nil || owns != (id == r.child) {
			t.Fatalf("%s owns=%v err=%v", id, owns, err)
		}
	}
}
