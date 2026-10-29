package watch

import (
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

// A pull request into an upstream repository stays out of the merge queue, even
// when GitHub reports it ready, because maintainers merge it. The room's own
// pull request in the same room stays queued.
func TestUpstreamPRStaysOutOfTheMergeQueue(t *testing.T) {
	mergeable := true
	head := "0123456789abcdef0123456789abcdef01234567"
	own := Record{Kind: "github", Key: "own", RoomID: "room_fork", Host: "github.com", Repo: "fork-owner/repo", HeadRepo: "fork-owner/repo", Number: 7,
		Head: head, Base: "main", BaseRef: "main", DefaultBranch: "main", Branch: "feature",
		Watching: true, State: "open", CI: "absent", Verdict: "approve", RoundHead: head,
		Mergeable: &mergeable, Created: time.Unix(1000, 0)}
	upstream := Record{Kind: "github", Key: "upstream", RoomID: "room_fork", Host: "github.com", Repo: "horizon-rust/horizon", HeadRepo: "contributor/horizon", Number: 3846,
		Head: head, Base: "main", BaseRef: "main", DefaultBranch: "main", Branch: "feature",
		Watching: true, State: "open", CI: "absent", Verdict: "approve", RoundHead: head,
		Mergeable: &mergeable, Created: time.Unix(1000, 0)}
	st := State{
		Records: map[string]*Record{own.Key: &own, upstream.Key: &upstream},
		RepositoryFacts: map[string]state.RepositoryFacts{
			defaultRepo(own):      {CheckedAt: time.Unix(2000, 0), MergeTrialCI: true},
			defaultRepo(upstream): {CheckedAt: time.Unix(2000, 0), MergeTrialCI: true},
		},
	}
	upstreams := func(roomID string) []string {
		if roomID != "room_fork" {
			return nil
		}
		return []string{"github.com/horizon-rust/horizon"}
	}
	rows := mergeQueue(st, nil, upstreams)
	if len(rows) != 1 || rows[0].ChangeID != changeID(own) {
		t.Fatalf("queue = %+v, want only the room's own pull request", rows)
	}
}
