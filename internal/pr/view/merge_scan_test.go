package view_test

import (
	"testing"
	"time"

	"dyna-agent/internal/listchange"
	"dyna-agent/internal/pr/state"
	"dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
)

func TestScanPublishesRoomMergeProgress(t *testing.T) {
	e := newEnv(t)
	a, b := e.room("merge-a"), e.room("merge-b")
	root := e.svc.PRs.Watcher().Store.Root
	st := watch.State{Version: 1, Records: map[string]*watch.Record{
		"a": {Key: "a", Kind: "github", Host: "github.com", Repo: "org/a", RoomID: a.ID, State: "open", Watching: true, CI: "pending"},
		"b": {Key: "b", Kind: "github", Host: "github.com", Repo: "org/b", RoomID: b.ID, State: "open", Watching: true, CI: "pending"},
	}, MergeTrials: map[string]*state.MergeTrial{"github.com/org/a": {Phase: "testing", Changes: []state.MergeTrialChange{{Key: "a"}}}}, MergePauses: map[string]string{}, RepositoryFacts: map[string]state.RepositoryFacts{}}
	testfixture.SaveState(t, root, st)
	signatures, work, version := map[string]string{}, map[string]string{}, ""
	e.svc.PRs.Scan(signatures, work, &version, true, func(string, string, *listchange.Change, bool) {})
	for _, step := range []struct {
		name   string
		change func()
	}{
		{"trial phase", func() { st.MergeTrials["github.com/org/a"].Phase = "passed" }},
		{"pause", func() { st.MergePauses[a.ID] = "inspect failure" }},
		{"main CI", func() {
			st.RepositoryFacts["github.com/org/a"] = state.RepositoryFacts{Head: "main-head", Verdict: "failed", CheckedAt: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)}
		}},
		{"trial removed", func() { delete(st.MergeTrials, "github.com/org/a") }},
		{"resume", func() { delete(st.MergePauses, a.ID) }},
	} {
		t.Run(step.name, func(t *testing.T) {
			step.change()
			testfixture.SaveState(t, root, st)
			events := 0
			e.svc.PRs.Scan(signatures, work, &version, false, func(kind, roomID string, change *listchange.Change, wake bool) {
				if kind != "prs" || roomID != a.ID || change != nil || wake {
					t.Errorf("unexpected event: %s %s %+v wake=%v", kind, roomID, change, wake)
				}
				events++
			})
			if events != 1 {
				t.Fatalf("got %d events; want one room-scoped merge refresh", events)
			}
			e.svc.PRs.Scan(signatures, work, &version, false, func(string, string, *listchange.Change, bool) { t.Error("idle scan emitted an event") })
		})
	}
}
