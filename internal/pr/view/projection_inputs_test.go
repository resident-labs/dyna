package view

import (
	"testing"
	"time"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

func TestProjectionAccountingComparisonKeepsInputsIsolated(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	remaining := 1000
	before := watch.State{Records: map[string]*watch.Record{"pr": {Key: "pr", State: "open", Checked: now}},
		RateLimits:      map[string]state.RateLimit{"scope": {ObservedAt: now, Remaining: &remaining, Reset: now.Add(time.Hour)}},
		RepositoryFacts: map[string]watch.RepositoryFacts{"repo": {Head: "head", CheckedAt: now}}}
	after := before
	row := *before.Records["pr"]
	row.Checked, row.Polling.Next = now.Add(time.Minute), now.Add(2*time.Minute)
	after.Records = map[string]*watch.Record{"pr": &row}
	if !sameProjectionInputs(before, after) {
		t.Fatal("accounting alone changed the projection inputs")
	}
	if before.Records["pr"].Checked != now || before.RateLimits["scope"].ObservedAt != now ||
		before.RateLimits["scope"].Remaining != &remaining || before.RepositoryFacts["repo"].CheckedAt != now {
		t.Fatal("comparing inputs changed retained state")
	}
	row.HeadStale = true
	if sameProjectionInputs(before, after) {
		t.Fatal("head verification change was ignored")
	}
	row.HeadStale = false
	row.Kind = "local"
	beforeRow := *before.Records["pr"]
	beforeRow.Kind = "local"
	before.Records = map[string]*watch.Record{"pr": &beforeRow}
	if sameProjectionInputs(before, after) {
		t.Fatal("local poll invalidation was ignored")
	}
}
