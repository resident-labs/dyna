package view_test

import (
	"testing"

	"dyna-agent/internal/pr/state"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

// The issue card names the open PR that fixes it, and forgets a merged one.
func TestIssueFixesNamesOnlyOpenChanges(t *testing.T) {
	e := newEnv(t)
	rm := e.room("fixes-room")
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, watch.State{Version: 1, Records: map[string]*watch.Record{
		"open": {Key: "open", Kind: "github", RoomID: rm.ID, Number: 42, State: "open",
			Fixes: []state.IssueRef{{Kind: state.IssueKindDyna, Number: 5}, {Kind: state.IssueKindGitHub, Number: 7}}},
		"merged": {Key: "merged", Kind: "github", RoomID: rm.ID, Number: 43, State: "merged",
			Fixes: []state.IssueRef{{Kind: state.IssueKindDyna, Number: 6}}},
		"other-room": {Key: "other-room", Kind: "github", RoomID: "elsewhere", Number: 44, State: "open",
			Fixes: []state.IssueRef{{Kind: state.IssueKindDyna, Number: 8}}},
	}})
	fixes, err := e.svc.PRs.IssueFixes(rm.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(fixes) != 1 || fixes[5] != 42 {
		t.Fatalf("issue fixes = %v, want only D-5 in #42", fixes)
	}
}
