package view

import (
	"encoding/json"
	"testing"

	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/watch"
)

func TestProjectionRetainsOldRecordsAndOmitsHTTPBodies(t *testing.T) {
	root := state.Root(t.TempDir())
	initial := watch.State{Version: 1, Records: map[string]*watch.Record{
		"changed":   {Key: "changed", ChangeID: "unchanged", State: "open", Findings: []state.Finding{{ID: "f", Replies: []state.FindingReply{{Message: "before"}}}}},
		"unchanged": {Key: "unchanged", State: "open", Checks: []watch.CheckSummary{{Name: "original"}}},
	}, Responses: map[string]map[string]state.CachedResponse{"changed": {"/pulls/1": {Body: json.RawMessage(`{"body":"poller only"}`)}}}}
	if err := state.Write(root, initial); err != nil {
		t.Fatal(err)
	}
	before, err := state.ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	old := &projection{snapshot: before, state: copyProjectionState(before, nil)}
	// A public copy cannot change projection records or the shared snapshot.
	caller, _ := before.Record("unchanged")
	caller.Checks[0].Name = "caller mutation"
	if err := state.Update(t.Context(), root, func(st *state.State) error {
		st.Records["changed"].Findings[0].Replies[0].Message = "after"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after, err := state.ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	current := copyProjectionState(after, old)
	if old.state.Records["changed"].Findings[0].Replies[0].Message != "before" || current.Records["changed"].Findings[0].Replies[0].Message != "after" {
		t.Fatal("new projection changed a retained projection")
	}
	if current.Records["unchanged"].Checks[0].Name != "original" {
		t.Fatal("caller changed an unchanged projection record")
	}
	if current.Responses != nil || old.state.Responses != nil {
		t.Fatal("projection copied poller HTTP bodies")
	}
	if err := state.Update(t.Context(), root, func(st *state.State) error {
		delete(st.Records, "unchanged")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	removed, err := state.ReadSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	latest := copyProjectionState(removed, &projection{snapshot: after, state: current})
	if latest.Records["unchanged"] != nil || current.Records["unchanged"] == nil || old.state.Records["unchanged"] == nil {
		t.Fatal("deletion changed a retained projection")
	}
}
