package watch

import (
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
)

// A repository that publishes no CI for pull request heads cannot supply one, so
// an approved pull request must go to its merge trial instead of waiting at
// "waiting for CI" for ever.
func TestQueueTakesAPRWithoutHeadCIStraightToItsTrial(t *testing.T) {
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
	rows := mergeQueue(st)
	if len(rows) != 1 || rows[0].State != "ready" {
		t.Fatalf("queue = %+v, want the pull request ready for its trial", rows)
	}
	// A head whose CI is still running keeps waiting.
	r.CI = "pending"
	st.Records[r.Key] = &r
	if rows := mergeQueue(st); len(rows) != 1 || rows[0].State != "ci" {
		t.Fatalf("pending head = %+v, want it to wait for CI", rows)
	}
}

// A room in local mode decides its merge trial from local CI, so a repository
// without hosted workflows still gets a trial. Every other room needs the hosted
// push CI on dyna/merge-trial/**, which is what the fact already recorded.
func TestMergeTrialNeedsHostedPushCIOnlyOutsideLocalMode(t *testing.T) {
	noWorkflows := defaultBranchProbe{Name: "main", Head: "abc"}
	for _, tc := range []struct {
		name     string
		probe    defaultBranchProbe
		previous RepositoryFacts
		local    bool
		want     bool
	}{
		{name: "local mode needs no hosted push CI", probe: noWorkflows, local: true, want: true},
		{name: "hosted mode still needs it", probe: noWorkflows, want: false},
		{name: "hosted push CI confirmed", probe: defaultBranchProbe{MergeTrialCI: true, MergeTrialKnown: true}, want: true},
		{name: "an unknown read keeps the last answer", probe: defaultBranchProbe{MergeTrialKnown: false}, previous: RepositoryFacts{MergeTrialCI: true}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := repositoryMergeTrialCI(tc.probe, tc.previous, tc.local); got != tc.want {
				t.Fatalf("merge trial available = %t, want %t", got, tc.want)
			}
		})
	}
}
