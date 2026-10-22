package watch

import (
	"context"
	"testing"
)

// A reviewer that asked for changes waits for the author's next push. It must
// not hold one of the three review slots while other PRs queue.
func TestReviewerWaitingForAuthorReleasesReviewSlot(t *testing.T) {
	e, keys := readyReviewPool(t, 4)
	st := dispatchReviewPool(t, e)
	if len(e.launches) != 3 || st.Records[keys[3]].Reviewer != "" {
		t.Fatalf("first dispatch: launches=%d fourth=%+v", len(e.launches), st.Records[keys[3]])
	}
	r := st.Records[keys[0]]
	if err := e.s.Verdict(context.Background(), r.Key, r.Reviewer, r.Head, "changes", "Please fix the parser.", 0); err != nil {
		t.Fatal(err)
	}
	st = dispatchReviewPool(t, e)
	facts, err := e.s.ReviewFacts(st)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := facts.Busy[keys[0]]; ok {
		t.Fatal("verdict holder retained capacity")
	}

	if st.Records[keys[0]].Verdict != "changes" {
		t.Fatal("releasing reviewer lost its verdict")
	}
	if got := len(facts.Busy); got != 3 {
		t.Fatalf("active reviews = %d, want 3", got)
	}
	if st.Records[keys[3]].Reviewer == "" || len(e.launches) != 4 {
		t.Fatalf("queued PR did not get the freed slot: launches=%d fourth=%+v", len(e.launches), st.Records[keys[3]])
	}
}
