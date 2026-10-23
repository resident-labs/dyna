package subagent

import (
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
)

func TestCloseWithoutHeirKeepsHeldGoalOpen(t *testing.T) {
	e := newTestEnv(t)
	holder := completedFixtureSession(t, e)
	goal, err := e.m.goalStore().Add(holder.RoomID, holder.ID, goals.Input{Text: "Unfinished work", Holders: []string{holder.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Close(e.ctx, holder.ID); err == nil || !strings.Contains(err.Error(), "no open ancestor or root") {
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

// Close hands a goal to the nearest open ancestor, never to root: a goal that
// would reach root stays with the owner instead.
func TestCloseKeepsGoalsOffRoot(t *testing.T) {
	e := newTestEnv(t)
	rootID := NewID()
	if err := e.m.Store.writeMeta(Meta{ID: rootID, Name: RoleRoot, Role: RoleRoot, Service: true,
		RoomID: e.room.ID, Room: e.room.Name, Depth: 1}); err != nil {
		t.Fatal(err)
	}
	holder := completedFixtureSession(t, e)
	goal, err := e.m.goalStore().Add(holder.RoomID, "owner", goals.Input{Text: "Unfinished work", Holders: []string{holder.ID}})
	if err != nil {
		t.Fatal(err)
	}
	closed, err := e.m.Close(e.ctx, holder.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := e.m.goalStore().Get(holder.RoomID, goal.ID)
	if err != nil || !got.Open() || got.HeldBy(rootID) || len(got.Holders) != 0 || got.Setter != "owner" {
		t.Fatalf("goal after close = %+v, %v; want it open and unheld for the owner", got, err)
	}
	if !slices.Contains(closed.CloseSummary, "Goal "+goal.ID+" → the owner") {
		t.Fatalf("close summary = %+v; want it to report the owner", closed.CloseSummary)
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
