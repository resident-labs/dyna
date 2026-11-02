package engine

import (
	"testing"

	"github.com/resident-labs/dyna/engine/prs/fakegithub"
)

func TestARestartMidFeedRepeatsNothingAndLosesNothing(t *testing.T) {
	r := newPRRig(t)
	r.open(t, fakegithub.Pull{Repo: "owner/repo", Number: 1})
	r.settle(t)
	r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) {
		p.Comments = []fakegithub.Comment{{ID: 5000, Author: "alice", Body: "first"}}
	})
	r.pass(t)
	r.connect()
	r.pass(t)
	r.github.EditAs("alice", "owner/repo", 1, func(p *fakegithub.Pull) {
		p.Comments = append(p.Comments, fakegithub.Comment{ID: 5001, Author: "alice", Body: "while down"})
	})
	r.connect()
	r.pass(t)
	got := r.wakes(t, r.child)
	if len(got) != 2 || got[0].Changes[0].Comments[0].Body != "first" || got[1].Changes[0].Comments[0].Body != "while down" {
		t.Fatalf("wakes %+v", got)
	}
}
