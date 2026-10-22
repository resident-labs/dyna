package goalkeeper

import (
	"context"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

func stalePosts(e *checkInEnv) []room.Message {
	var out []room.Message
	for _, m := range e.posts(room.GoalKeeperSender) {
		if strings.Contains(m.Text, "is stale") {
			out = append(out, m)
		}
	}
	return out
}

// nudged runs one nudge pass with fresh state and the live session list.
func nudged(t *testing.T, e *checkInEnv) {
	t.Helper()
	st, err := e.s.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := e.s.sessionState()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.nudgeStaleHolders(context.Background(), st, sessions, e.now); err != nil {
		t.Fatal(err)
	}
}

// A fresh goal with an open item goes stale after the window, nudges its
// holder once, stays quiet inside the window, and nudges again after the
// next one.
func TestNudgeStaleHolderOncePerWindow(t *testing.T) {
	e := newCheckInEnv(t)
	e.meta(subagent.Meta{ID: holderID, Name: "holder"}, subagent.StatusIdle)
	if err := e.s.Store.Update(func(st *goals.State) error {
		for _, g := range st.Room(e.rec.ID).Goals {
			g.State = "done"
			g.Done = &goals.Outcome{At: e.now}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g, err := e.s.Store.Add(e.rec.ID, setterID, goals.Input{Text: "Fresh work", Holders: []string{holderID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Store.AddItem(e.rec.ID, g.ID, setterID, "work", holderID); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(goals.StaleAfter + time.Hour)
	nudged(t, e)
	if got := stalePosts(e); len(got) != 1 {
		t.Fatalf("stale nudges = %d, want 1", len(got))
	}
	fresh, err := e.s.Store.Get(e.rec.ID, g.ID)
	if err != nil || fresh.NudgedAt.IsZero() {
		t.Fatalf("NudgedAt = %+v err=%v", fresh.NudgedAt, err)
	}
	// Reloaded state inside the window nudges nobody new.
	nudged(t, e)
	if got := stalePosts(e); len(got) != 1 {
		t.Fatalf("second pass posted %d nudges, want 1", len(got))
	}
	// After the next window the holder hears again.
	e.now = e.now.Add(goals.StaleAfter + time.Hour)
	nudged(t, e)
	if got := stalePosts(e); len(got) != 2 {
		t.Fatalf("post-window nudges = %d, want 2", len(got))
	}
}

// A declared wait beats the stale window: no nudge before the deadline.
func TestNudgeHonorsGoalWait(t *testing.T) {
	e := newCheckInEnv(t)
	e.meta(subagent.Meta{ID: holderID, Name: "holder"}, subagent.StatusIdle)
	if err := e.s.Store.Update(func(st *goals.State) error {
		for _, g := range st.Room(e.rec.ID).Goals {
			g.State = "done"
			g.Done = &goals.Outcome{At: e.now}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g, err := e.s.Store.Add(e.rec.ID, setterID, goals.Input{Text: "Waiting work", Holders: []string{holderID}})
	if err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(2 * time.Hour)
	if _, err := e.s.Store.Wait(e.rec.ID, g.ID, holderID, e.now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// While the deadline is still ahead, no nudge and no mark.
	nudged(t, e)
	if got := stalePosts(e); len(got) != 0 {
		t.Fatalf("waiting goal nudged before its deadline: %+v", got)
	}
	fresh, _ := e.s.Store.Get(e.rec.ID, g.ID)
	if !fresh.NudgedAt.IsZero() {
		t.Fatalf("waiting goal marked nudged: %+v", fresh.NudgedAt)
	}
	// Past the deadline, the stale window counts from it.
	e.now = e.now.Add(2*time.Hour + goals.StaleAfter + time.Hour)
	nudged(t, e)
	if got := stalePosts(e); len(got) != 1 {
		t.Fatalf("nudges after the wait ended = %d, want 1", len(got))
	}
}

// An undelivered nudge keeps its cooldown: a dormant holder hears nothing,
// the goal stays unmarked, and a later wake retries the nudge.
func TestNudgeMarksOnlyDelivered(t *testing.T) {
	e := newCheckInEnv(t)
	if err := e.s.Store.Update(func(st *goals.State) error {
		for _, g := range st.Room(e.rec.ID).Goals {
			g.State = "done"
			g.Done = &goals.Outcome{At: e.now}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	g, err := e.s.Store.Add(e.rec.ID, setterID, goals.Input{Text: "Dormant work", Holders: []string{holderID}})
	if err != nil {
		t.Fatal(err)
	}
	// Adding the goal activates its holder, so dormancy is set after it.
	e.meta(subagent.Meta{ID: holderID, Name: "holder"}, subagent.StatusDormant)
	e.now = e.now.Add(goals.StaleAfter + time.Hour)
	nudged(t, e)
	if got := stalePosts(e); len(got) != 0 {
		t.Fatalf("dormant holder nudged: %+v", got)
	}
	fresh, _ := e.s.Store.Get(e.rec.ID, g.ID)
	if !fresh.NudgedAt.IsZero() {
		t.Fatalf("undelivered nudge consumed the cooldown: %+v", fresh.NudgedAt)
	}
	e.meta(subagent.Meta{ID: holderID, Name: "holder"}, subagent.StatusIdle)
	nudged(t, e)
	if got := stalePosts(e); len(got) != 1 {
		t.Fatalf("retry after wake = %d nudges, want 1", len(got))
	}
}

// The fold leaves delegation items on sessions that hold no goal; the
// keeper still treats them as working: the stale nudge is the only keeper
// post they can get, and nothing calls them goalless.
func TestItemAssigneesGetNoKeeperNagging(t *testing.T) {
	e := newCheckInEnv(t)
	host, err := e.s.Store.Add(e.rec.ID, setterID, goals.Input{Text: "Parent work", Holders: []string{setterID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Store.AddItem(e.rec.ID, host.ID, setterID, "migrated work", holderID); err != nil {
		t.Fatal(err)
	}
	e.now = e.now.Add(time.Hour)
	if _, err := e.s.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, m := range e.posts(room.GoalKeeperSender) {
		for _, to := range m.To {
			if strings.Contains(to, "cccccccc") {
				t.Fatalf("item assignee got a keeper post: %+v", m)
			}
		}
	}
}
