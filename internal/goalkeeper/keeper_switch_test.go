package goalkeeper

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
	"dyna-agent/internal/settings"
)

func TestKeeperSwitchStopsAndResumesDelivery(t *testing.T) {
	e := newCheckInEnv(t)
	if err := e.s.Store.Update(func(st *goals.State) error {
		st.Room(e.rec.ID).Notices = append(st.Room(e.rec.ID).Notices, goals.Notice{
			ID: "queued", Kind: "item_moved", To: leadID, Goal: "g1", Text: "queued notice",
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	setKeeper := func(on bool) {
		t.Helper()
		v, err := settings.Load(settings.DefaultPath())
		if err != nil {
			t.Fatal(err)
		}
		v.GoalKeeperEnabled = on
		if err := v.Save(); err != nil {
			t.Fatal(err)
		}
	}
	setKeeper(false)
	e.now = e.now.Add(goals.StaleAfter + time.Hour)
	next, err := e.s.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if posts := e.posts(room.GoalKeeperSender); len(posts) != 0 {
		t.Fatalf("disabled keeper posted messages: %+v", posts)
	}
	if !next.Equal(e.now.Add(5 * time.Minute)) {
		t.Fatalf("next tick = %v; want %v", next, e.now.Add(5*time.Minute))
	}
	st, err := e.s.Store.Read()
	if err != nil || len(st.Room(e.rec.ID).Notices) != 1 {
		t.Fatalf("disabled keeper changed queued notices: %+v, %v", st, err)
	}
	setKeeper(true)
	e.now = next
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	var stale, queued bool
	for _, m := range e.posts(room.GoalKeeperSender) {
		stale = stale || strings.Contains(m.Text, "is stale")
		queued = queued || strings.Contains(m.Text, "queued notice")
	}
	if !stale || !queued {
		t.Fatalf("resumed keeper delivery: stale=%v queued=%v", stale, queued)
	}
}
