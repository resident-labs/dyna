package watch

import (
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

// A held pull request stays out of the merge queue even after GitHub reports it
// ready for review, because a ready-for-review call lifts only the GitHub draft.
func TestHeldPRStaysOutOfTheMergeQueueWhenGitHubIsReady(t *testing.T) {
	mergeable := true
	head := "0123456789abcdef0123456789abcdef01234567"
	r := Record{Kind: "github", Key: "pr", Host: "github.com", Repo: "owner/repo", HeadRepo: "owner/repo", Number: 7,
		Head: head, Base: "main", BaseRef: "main", DefaultBranch: "main", Branch: "feature",
		Watching: true, State: "open", CI: "absent", Verdict: "approve", RoundHead: head,
		Mergeable: &mergeable, Created: time.Unix(1000, 0)}
	st := State{
		Records:         map[string]*Record{r.Key: &r},
		RepositoryFacts: map[string]state.RepositoryFacts{defaultRepo(r): {CheckedAt: time.Unix(2000, 0), MergeTrialCI: true}},
	}
	r.Hold = &state.Hold{Reason: "wait for the owner", At: time.Unix(1500, 0)}
	rows := mergeQueue(st, nil, nil)
	if len(rows) != 1 || rows[0].State != "held" {
		t.Fatalf("queue = %+v, want the held pull request to stay held", rows)
	}
}
