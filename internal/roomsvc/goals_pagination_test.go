package roomsvc

import (
	"dyna-agent/internal/goals"
	"testing"
	"time"
)

func TestGoalsCutoffAndCursor(t *testing.T) {
	e := newEnv(t)
	r := e.room("paged")
	store := e.svc.goalService().Store
	now := time.Now()
	old := now.Add(-48 * time.Hour)
	for _, at := range []time.Time{old, now} {
		store.Now = func() time.Time { return at }
		g, err := store.Add(r.ID, "owner", goals.Input{Text: "Closed work", Shared: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Finish(r.ID, g.ID, "owner", "Done", false); err != nil {
			t.Fatal(err)
		}
	}
	store.Now = func() time.Time { return old }
	open, err := store.Add(r.ID, "owner", goals.Input{Text: "Open work", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.svc.Goals(r.ID, "", 1)
	if err != nil || len(first.Items) != 2 || first.Items[0].ID != open.ID || !first.Older {
		t.Fatalf("default: %+v %v", first, err)
	}
	page, err := e.svc.Goals(r.ID, first.Cursor, 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].Done == nil || !page.Items[0].Done.At.Equal(old) || page.Older {
		t.Fatalf("older: %+v %v", page, err)
	}
}
