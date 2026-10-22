package view

import (
	"encoding/json"
	"strings"
	"testing"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

func TestChangeViewPassesSavedCheckFailureExcerpt(t *testing.T) {
	r := watch.Record{Checks: []watch.CheckSummary{{Name: "unit", State: "failure", FailureExcerpt: "FAIL broken\nexpected green"}}}
	row := changeView(watch.ReviewFacts{}, r)
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"failureExcerpt":"FAIL broken\nexpected green"`) {
		t.Fatalf("saved excerpt missing: %s", data)
	}
}

func TestChangeViewCarriesSharedOverlaps(t *testing.T) {
	r := watch.Record{Overlaps: []state.Overlap{
		{ChangeID: "a", Number: 7, Files: []string{"x.go"}, Conflicts: []string{"y.go"}},
		{ChangeID: "b", Number: 8},
	}}
	got := changeView(watch.ReviewFacts{}, r).Overlaps
	if len(got) != 1 || got[0].Number != 7 || got[0].Conflicts[0] != "y.go" {
		t.Fatalf("overlaps = %+v", got)
	}
}
