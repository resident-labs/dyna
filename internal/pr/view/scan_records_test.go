package view_test

import (
	"fmt"
	"slices"
	"testing"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/pr/view"
	"dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

// A PR state write rebuilds the rows of the records it wrote only, and a
// removed record is reported deleted. Unchanged records, in the same room or
// another, are not rebuilt, and a scan with no new write builds nothing.
func TestScanRebuildsOnlyChangedRecords(t *testing.T) {
	e := newEnv(t)
	a, b := e.room("alpha"), e.room("beta")
	root := e.svc.PRs.Watcher().Store.Root
	st := watch.State{Version: 1, Reviewers: map[string]watch.Reviewer{}, Records: map[string]*watch.Record{
		"a1": {Key: "a1", RoomID: a.ID, State: "open", Watching: true, CI: "pending"},
		"a2": {Key: "a2", RoomID: a.ID, State: "open", Watching: true, CI: "pending"},
		"a3": {Key: "a3", RoomID: a.ID, State: "open", Watching: true, CI: "pending"},
		"b1": {Key: "b1", RoomID: b.ID, State: "open", Watching: true, CI: "pending"},
	}}
	testfixture.SaveState(t, root, st)
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	var events []string
	emit := func(kind, roomID string, change *listchange.Change, _ bool) {
		if change != nil {
			events = append(events, fmt.Sprintf("%s %s %s %s", kind, roomID, change.Op, change.ID))
		}
	}
	e.svc.PRs.Scan(signatures, work, &version, true, emit)
	built := view.CountRows(t)
	st.Records["a1"].CI = "passed"
	delete(st.Records, "a2")
	testfixture.SaveState(t, root, st)
	e.svc.PRs.Scan(signatures, work, &version, false, emit)
	if *built != 1 {
		t.Errorf("built %d rows; want 1, for the one written record", *built)
	}
	e.svc.PRs.Scan(signatures, work, &version, false, emit)
	if *built != 1 {
		t.Errorf("an idle scan built %d more rows; want none", *built-1)
	}
	slices.Sort(events)
	want := []string{"changes " + a.ID + " delete a2", "changes " + a.ID + " upsert a1", "prs " + a.ID + " delete a2", "prs " + a.ID + " upsert a1"}
	if !slices.Equal(events, want) {
		t.Fatalf("events %q; want %q", events, want)
	}
}
