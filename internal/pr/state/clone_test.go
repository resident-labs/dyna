package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

func cloneFixture(entries int) State {
	r := &Record{Key: "pr", State: "closed", Mergeable: new(bool), CITestRuns: map[string]string{"run": "1"},
		Findings:          []Finding{{ID: "f", Replies: []FindingReply{{Message: "fixed"}}}},
		RequiredReviewers: []string{"required"},
		ReviewAssignments: map[string][]string{"head": {"reviewer"}},
		ReviewHistory:     []map[string]json.RawMessage{{"reviewHead": json.RawMessage(`"a"`)}}}
	for i := range entries {
		r.Notices = append(r.Notices, Notice{ID: fmt.Sprint(i), Text: "CI failed"})
		r.Checks = append(r.Checks, CheckSummary{Name: fmt.Sprint(i), Conclusion: "success"})
	}
	return State{Version: 1, Records: map[string]*Record{"pr": r},
		Responses:   map[string]map[string]CachedResponse{"pr": {"/pulls/1": {ETag: "e", Body: json.RawMessage(`{}`)}}},
		Reviewers:   map[string]Reviewer{"room": {FailedProfiles: map[string]time.Time{"p": {}}}},
		MergeTrials: map[string]*MergeTrial{"t": {Changes: []MergeTrialChange{{Key: "pr"}}}}}
}

// Every PR store update clones about 1,300 records, most of them closed and
// holding hundreds of notices and checks. Values without maps, slices or
// pointers must be copied in bulk, so their count adds no allocations.
func TestCloneStateCopiesFlatValuesInBulk(t *testing.T) {
	allocs := func(entries int) float64 {
		st := cloneFixture(entries)
		return testing.AllocsPerRun(5, func() { cloneState(st) })
	}
	one, many := allocs(1), allocs(500)
	if many > one {
		t.Fatalf("cloning 500 notices and checks took %.0f allocations, and 1 of each took %.0f; flat entries must add none", many, one)
	}
}

func TestCloneStateSharesNoMutableMemory(t *testing.T) {
	st := cloneFixture(2)
	// A notice that carries a merge event deep-copies the event, so a clone
	// cannot change the trial the original names.
	st.Records["pr"].Notices[0].Merge = &room.MergeEvent{
		Type: room.MergeQueued, TrialID: "dyna/merge-trial/1", Repo: "a/b", Base: "main",
		PRs: []room.MergePR{{Number: 1, Title: "Fix", URL: "https://github.com/a/b/pull/1"}},
	}
	before, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	c := cloneState(st)
	r := c.Records["pr"]
	r.State = "open"
	r.RequiredReviewers[0] = "mutated"
	*r.Mergeable = true
	r.CITestRuns["run"] = "2"
	r.Findings[0].Replies[0].Message = "changed"
	r.ReviewAssignments["head"][0] = "other"
	r.ReviewHistory[0]["reviewHead"][1] = 'b'
	r.Notices[0].Text = "changed"
	r.Notices[0].Merge.PRs[0].Title = "other"
	r.Checks[0].Conclusion = "failure"
	c.Responses["pr"]["/pulls/1"].Body[0] = '['
	c.Reviewers["room"].FailedProfiles["p"] = time.Unix(1, 0)
	c.MergeTrials["t"].Changes[0].Key = "other"
	after, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("changing the clone changed the original state")
	}
}

func TestUpdateCommitsChangesAndDropsFailedOnes(t *testing.T) {
	root := newRoot(t)
	if err := Update(context.Background(), root, func(st *State) error {
		st.Records["pr"] = &Record{Key: "pr", State: "open", Title: "first"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	failed := errors.New("stop")
	if err := Update(context.Background(), root, func(st *State) error {
		st.Records["pr"].Title = "lost"
		return failed
	}); err != failed {
		t.Fatalf("Update returned %v, want the callback error", err)
	}
	st, err := Read(root)
	if err != nil || st.Records["pr"].Title != "first" {
		t.Fatalf("after a failed update the store holds %+v (%v), want the first title", st.Records["pr"], err)
	}
	forgetSnapshot()
	if st, err = Read(root); err != nil || st.Records["pr"].Title != "first" {
		t.Fatalf("the database holds %+v (%v), want the first title", st.Records["pr"], err)
	}
}
