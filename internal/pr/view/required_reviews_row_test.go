package view

import (
	"reflect"
	"testing"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

func TestRequiredReviewsReachTheSharedPRRowWithoutSharingMemory(t *testing.T) {
	r := watch.Record{Key: "pr", Head: "current", RequiredReviewers: []string{"architecture", "safety"}, ReviewRounds: []state.ReviewRound{{Reviewer: "architecture", Head: "current", Verdict: "approve"}}}
	row := changeView(watch.ReviewFacts{}, r)
	if !reflect.DeepEqual(row.RequiredReviewers, r.RequiredReviewers) || !reflect.DeepEqual(row.MissingRequiredReviewers, []string{"safety"}) {
		t.Fatalf("required reviews absent from row: %+v", row)
	}
	row.RequiredReviewers[0] = "mutated"
	row.MissingRequiredReviewers[0] = "mutated"
	if r.RequiredReviewers[0] != "architecture" || r.RequiredReviewers[1] != "safety" {
		t.Fatal("PR row mutated source record")
	}
	r.RequiredReviewers = append(r.RequiredReviewers, "third")
	if changeView(watch.ReviewFacts{}, r).DetailVersion == row.DetailVersion {
		t.Fatal("new requirements kept an obsolete detail version")
	}
}
