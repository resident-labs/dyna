package goalkeeper

import (
	"context"
	"time"

	"dyna-agent/internal/subagent"
	"strings"
	"testing"
)

func TestHolderCloseHandbackDeliversOnce(t *testing.T) {
	for _, setterLive := range []bool{true, false} {
		t.Run(map[bool]string{true: "setter", false: "owner"}[setterLive], func(t *testing.T) {
			e := newEscalationEnv(t)
			if !setterLive {
				e.session(setterID, parentID, false)
			}
			if err := e.s.Store.CloseSession(e.rec.ID, holderID, parentID, map[string]bool{setterID: setterLive, parentID: true}, ""); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				if _, err := e.s.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			msgs, err := e.rooms.Messages(e.rec.ID)
			if err != nil || len(msgs) != 1 {
				t.Fatalf("handback posts=%+v err=%v", msgs, err)
			}
			want := e.rec.Name
			if setterLive {
				want = "work:aaaaaaaa"
			}
			if len(msgs[0].To) != 1 || msgs[0].To[0] != want {
				t.Fatalf("handback target=%v, want %s", msgs[0].To, want)
			}
		})
	}
}

// A deferred holder handback (the recipient is mid-turn) stays queued and
// the tick returns a retry time instead of nothing.
func TestDeferredHandbackSchedulesRetry(t *testing.T) {
	e := newEscalationEnv(t)
	// The setter holds a running turn, so DeliverGoal defers the post.
	e.session(setterID, parentID, true)
	if err := e.s.Store.CloseSession(e.rec.ID, holderID, parentID, map[string]bool{setterID: true, parentID: true}, ""); err != nil {
		t.Fatal(err)
	}
	st, _ := e.s.Store.Read()
	if len(st.Room(e.rec.ID).Notices) == 0 {
		t.Fatal("no handback queued")
	}
	// Hold the setter's turn lock, so DeliverGoal defers the post.
	lock, err := e.mgr.Store.TryLockTurnForTest(setterID)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	e.s.Now = func() time.Time { return now }
	next, err := e.s.Tick(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !next.Equal(now.Add(checkInRetry)) {
		t.Fatalf("deferred handback retry = %v, want %v", next, now.Add(checkInRetry))
	}
	fresh, _ := e.s.Store.Read()
	if len(fresh.Room(e.rec.ID).Notices) == 0 {
		t.Fatal("the deferred handback was dropped")
	}
}

func TestCloseGoalHandoffNotifiesIdleAncestorAndRoot(t *testing.T) {
	for _, root := range []bool{false, true} {
		t.Run(map[bool]string{false: "ancestor", true: "root"}[root], func(t *testing.T) {
			e := newEscalationEnv(t)
			if root {
				if _, err := e.mgr.Store.UpdateMeta(setterID, func(m *subagent.Meta) { m.Role = subagent.RoleRoot; m.Name = "root" }); err != nil {
					t.Fatal(err)
				}
				if _, err := e.mgr.Store.UpdateMeta(holderID, func(m *subagent.Meta) { m.ParentSession = "" }); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := e.mgr.CloseForEngine(context.Background(), holderID, "test handoff", subagent.CloseOptions{}); err != nil {
				t.Fatal(err)
			}
			// One tick drains durable notices. Repeating it must not duplicate delivery.
			for range 2 {
				if _, err := e.s.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			heir, err := e.mgr.Store.Info(setterID)
			if err != nil {
				t.Fatal(err)
			}
			posts, err := e.rooms.Messages(e.rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, post := range posts {
				if post.SessionIDs[heir.Address()] == heir.ID && post.Wake && strings.Contains(post.Text, e.goal.ID) && strings.Contains(post.Text, holderID) {
					found++
				}
			}
			if found != 1 {
				t.Fatalf("heir should receive one handoff notice: %+v", posts)
			}
		})
	}
}
