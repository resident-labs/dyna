package view

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

func TestChangeRowsAreLean(t *testing.T) {
	r := &watch.Record{Key: "repo#1", Kind: "github", RoomID: "room", State: "open", Watching: true, Head: "head", RoundHead: "head", Verdict: "approve", CI: "passed", Summary: strings.Repeat("Full review body. ", 1000), Notes: state.AuthorNotes{Intent: "Detail notes"}, Findings: []state.Finding{{ID: "finding", Why: "Detail finding"}}, ReviewRounds: []state.ReviewRound{{Round: 1, Summary: "Detail round"}}}
	st := watch.State{Records: map[string]*watch.Record{r.Key: r}}
	data, err := json.Marshal(sortedChanges(watch.ReviewFacts{State: st}, "room", st.Records)[0])
	if err != nil {
		t.Fatal(err)
	}
	{
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"summary", "notes", "findings", "reviewRounds", "polling", "ciBudgetHead"} {
			if _, ok := fields[name]; ok {
				t.Errorf("list row contains detail or store field %s", name)
			}
		}
		if string(fields["findingCount"]) != "1" {
			t.Error("lean row lost its finding count")
		}
		if string(fields["hasReviewSummary"]) != "true" {
			t.Error("row lost its review action")
		}
	}
}

func TestStaleHeadDoesNotShowApprovedStatus(t *testing.T) {
	r := &watch.Record{Key: "repo#1", Kind: "github", State: "open", Watching: true, Head: "head", HeadStale: true, Verdict: "approve", CI: "passed"}
	st := watch.State{Records: map[string]*watch.Record{r.Key: r}}
	row := sortedChanges(watch.ReviewFacts{State: st}, "", st.Records)[0]
	if row.ReviewState != "blocked" || row.Status == "approved" {
		t.Fatalf("stale approval: %+v", row)
	}
}

func TestDetailEditsInvalidateTheSelectedRow(t *testing.T) {
	r := watch.Record{Key: "repo#1", Summary: "First review", PolicyOverride: watch.PolicyOverride{Review: "off", Reason: "Owner reviewed"}}
	st := watch.State{}
	before := changeView(watch.ReviewFacts{State: st}, r)
	r.Summary = "Edited review"
	after := changeView(watch.ReviewFacts{State: st}, r)
	if before.DetailVersion == after.DetailVersion || before.DetailVersion == "" {
		t.Fatal("a detail edit did not invalidate the selected review")
	}
	if !after.HasPolicyOverride {
		t.Fatal("lean row lost its policy override label")
	}
	r.Findings = []watch.Finding{{ID: "finding", Why: "New finding"}}
	if got := changeView(watch.ReviewFacts{State: st}, r); got.DetailVersion == after.DetailVersion {
		t.Fatal("a new finding did not invalidate the selected review")
	}
}

func TestChangeRowCarriesTheLatestReviewTime(t *testing.T) {
	first := time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)
	second := first.Add(time.Hour)
	r := &watch.Record{Key: "repo#1", Kind: "github", RoomID: "room", State: "open", Watching: true, Head: "head", Verdict: "approve", ReviewRounds: []state.ReviewRound{{Round: 1, At: first}, {Round: 2, At: second}}}
	st := watch.State{Records: map[string]*watch.Record{r.Key: r}}
	row := sortedChanges(watch.ReviewFacts{State: st}, "room", st.Records)[0]
	if !row.ReviewAt.Equal(second) {
		t.Errorf("ReviewAt = %v, want the latest round %v", row.ReviewAt, second)
	}
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"reviewAt":"2026-10-20T02:00:00Z"`) {
		t.Errorf("the changes API row lacks reviewAt: %s", data)
	}
}
