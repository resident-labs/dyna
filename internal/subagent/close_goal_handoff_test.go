package subagent

import (
	"strings"
	"testing"

	"dyna-agent/internal/goals"
)

// A close whose heir is root, or the owner when nothing else is open, refuses
// until each held goal is resolved. A forced close refuses for the same reason
// and never hands the goal to the owner.
func TestCloseRefusesGoalsThatWouldReachRoot(t *testing.T) {
	e := newTestEnv(t)
	holder := completedFixtureSession(t, e)
	rootID := NewID()
	if err := e.m.Store.writeMeta(Meta{ID: rootID, Name: RoleRoot, Role: RoleRoot, Service: true,
		RoomID: e.room.ID, Room: e.room.Name, Depth: 1}); err != nil {
		t.Fatal(err)
	}
	goal, err := e.m.goalStore().Add(holder.RoomID, holder.ID, goals.Input{Text: "Unfinished work", Holders: []string{holder.ID}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.m.Close(e.ctx, holder.ID)
	if err == nil {
		t.Fatal("close moved a goal to root")
	}
	for _, want := range []string{
		"responsibilities: cannot close:",
		"holds goal " + goal.ID,
		"dyna goal done " + goal.ID,
		"dyna goal drop " + goal.ID,
		"dyna goal edit " + goal.ID + " --for @",
		"a forced close keeps the work here",
		"ask the owner first with dyna ask",
		"hand it to an open session",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q misses %q", err, want)
		}
	}
	info, err := e.m.Store.Info(holder.ID)
	if err != nil || !info.Closed.IsZero() {
		t.Fatalf("refused close closed the session: %+v, %v", info, err)
	}
	if got, err := e.m.goalStore().Get(holder.RoomID, goal.ID); err != nil || !got.HeldBy(holder.ID) {
		t.Fatalf("refused close moved the goal: %+v, %v", got, err)
	}
	if _, err := e.m.CloseWithOptions(e.ctx, holder.ID, CloseOptions{Force: true}); err == nil || !strings.Contains(err.Error(), "dyna goal done "+goal.ID) {
		t.Fatalf("forced close = %v; want the goal refusal", err)
	}
	got, err := e.m.goalStore().Get(holder.RoomID, goal.ID)
	if err != nil || !got.Open() || !got.HeldBy(holder.ID) || got.Setter == "owner" || got.NeedsRoot() {
		t.Fatalf("forced close left goal = %+v, %v; want it held by the open session", got, err)
	}
	if info, err := e.m.Store.Info(holder.ID); err != nil || !info.Closed.IsZero() {
		t.Fatalf("forced close closed the session: %+v, %v", info, err)
	}
}

func TestCloseWithoutHeirKeepsHeldGoalOpen(t *testing.T) {
	e := newTestEnv(t)
	holder := completedFixtureSession(t, e)
	goal, err := e.m.goalStore().Add(holder.RoomID, holder.ID, goals.Input{Text: "Unfinished work", Holders: []string{holder.ID}})
	if err != nil {
		t.Fatal(err)
	}
	// No open ancestor and no root session: the heir is the owner.
	if _, err := e.m.Close(e.ctx, holder.ID); err == nil || !strings.Contains(err.Error(), "dyna goal done "+goal.ID) {
		t.Fatalf("close without heir: %v", err)
	}
	info, err := e.m.Store.Info(holder.ID)
	if err != nil || !info.Closed.IsZero() {
		t.Fatalf("session closed: %+v, %v", info, err)
	}
	got, err := e.m.goalStore().Get(holder.RoomID, goal.ID)
	if err != nil || !got.Open() || !got.HeldBy(holder.ID) {
		t.Fatalf("goal changed: %+v, %v", got, err)
	}
}

func TestCloseGoalHandoffPreservesConcurrentGoalChange(t *testing.T) {
	for _, change := range []string{"finish", "reassign"} {
		t.Run(change, func(t *testing.T) {
			e := newTestEnv(t)
			heir, holder, other := completedFixtureSession(t, e), completedFixtureSession(t, e), completedFixtureSession(t, e)
			if _, err := e.m.Store.UpdateMeta(holder.ID, func(m *Meta) { m.ParentSession = heir.ID; m.Depth = 2 }); err != nil {
				t.Fatal(err)
			}
			goal, err := e.m.goalStore().Add(holder.RoomID, "owner", goals.Input{Text: "Work", Holders: []string{holder.ID}})
			if err != nil {
				t.Fatal(err)
			}
			collected, release := make(chan struct{}), make(chan struct{})
			e.m.checkpoint = func(stage string) {
				if stage == "close-before-goal-handoff" {
					close(collected)
					<-release
				}
			}
			done := make(chan error, 1)
			go func() { _, err := e.m.Close(e.ctx, holder.ID); done <- err }()
			select {
			case <-collected:
			case err := <-done:
				t.Fatalf("close ended before handoff: %v", err)
			}
			if change == "finish" {
				_, err = e.m.goalStore().Finish(holder.RoomID, goal.ID, "owner", "Accepted", false)
			} else {
				next := []string{other.ID}
				_, err = e.m.goalStore().Edit(holder.RoomID, goal.ID, "owner", "", &next, false)
			}
			close(release)
			closeErr := <-done
			if err != nil || closeErr != nil {
				t.Fatalf("change=%v close=%v", err, closeErr)
			}
			got, err := e.m.goalStore().Get(holder.RoomID, goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if change == "finish" {
				if got.State != "done" || got.Done == nil || got.Done.Summary != "Accepted" {
					t.Fatalf("completion overwritten: %+v", got)
				}
			} else if len(got.Holders) != 1 || !got.HeldBy(other.ID) {
				t.Fatalf("reassignment overwritten: %+v", got)
			}
		})
	}
}
