package view_test

import (
	"dyna-agent/internal/pr/state"
	prfixture "dyna-agent/internal/pr/view/testfixture"
	"dyna-agent/internal/pr/watch"
	"dyna-agent/internal/store"
	"testing"
	"time"
)

func TestChangesAndPRsCutoffAndCursor(t *testing.T) {
	e := newEnv(t)
	r := e.room("paged")
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	st := watch.State{Version: 1, Records: map[string]*watch.Record{}, Reviewers: map[string]watch.Reviewer{}}
	for _, item := range []struct {
		id, state string
		at        time.Time
	}{{"open", "open", old}, {"recent", "closed", now}, {"old", "merged", old}} {
		st.Records[item.id] = &watch.Record{Key: item.id, RoomID: r.ID, Created: item.at, Checked: item.at, ClosedAt: item.at, State: item.state, Watching: item.state == "open"}
	}
	prfixture.SaveState(t, e.svc.PRs.Watcher().Store.Root, st)
	t.Run("Changes", func(t *testing.T) {
		first, err := e.svc.PRs.Changes(r.ID, "", 1)
		if err != nil || len(first.Items) != 2 || !first.Older {
			t.Fatalf("default: %+v %v", first, err)
		}
		page, err := e.svc.PRs.Changes(r.ID, first.Cursor, 1)
		if err != nil || len(page.Items) != 1 || page.Items[0].Key != "old" || page.Older {
			t.Fatalf("older: %+v %v", page, err)
		}
	})
}

func TestHistoryCursorSurvivesClosedRefresh(t *testing.T) {
	e := newEnv(t)
	r := e.room("stable-history")
	now := time.Now()
	st := watch.State{Version: 1, Records: map[string]*watch.Record{}, Reviewers: map[string]watch.Reviewer{}}
	for i, id := range []string{"recent", "older", "oldest"} {
		at := now.Add(-time.Duration(i) * 48 * time.Hour)
		st.Records[id] = &watch.Record{Key: id, RoomID: r.ID, State: "closed", Created: at, Checked: at, ClosedAt: at}
	}
	root := e.svc.PRs.Watcher().Store.Root
	prfixture.SaveState(t, root, st)
	first, err := e.svc.PRs.Changes(r.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	// Stored closure times keep history stable across later metadata refreshes.
	refreshed, err := e.svc.PRs.Watcher().Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.Records["older"].ClosedAt.Equal(now.Add(-48 * time.Hour)) {
		t.Fatal("old closed record did not retain its best known end time")
	}
	refreshed.Records["older"].Checked = now.Add(time.Hour)
	unlock, err := store.Lock(state.LockPath(root))
	if err != nil {
		t.Fatal(err)
	}
	err = state.Write(root, refreshed)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	next, err := e.svc.PRs.Changes(r.ID, first.Cursor, 1)
	if err != nil || len(next.Items) != 1 || next.Items[0].Key != "older" {
		t.Fatalf("refresh skipped history: %+v %v", next, err)
	}
}
