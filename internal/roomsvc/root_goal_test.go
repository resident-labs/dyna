package roomsvc

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/goals"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

// TestRootCannotHoldGoals keeps the room's root seat out of holders, whether
// root sets the goal, edits its holders, or promotes an item.
func TestRootCannotHoldGoals(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	rootID := "ses_20261022-000000-rrrrrrrr"
	recordtest.Write(t, e.mgr.Store.Root, rootID, subagent.Meta{ID: rootID, Name: subagent.RoleRoot, Role: subagent.RoleRoot,
		Service: true, RoomID: r.ID, Room: r.Name, Depth: 1, Resumable: true, HarnessSession: "mock-fixture"})
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Root, Session: rootID})
	rootCannot := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), goals.RootHoldsNothing) {
			t.Fatalf("err = %v, want the root holder refusal", err)
		}
	}
	_, err := e.svc.AddGoal(ctx, "", "Ship the room work", nil)
	rootCannot(t, err)
	_, err = e.svc.AddGoal(ctx, r.ID, "Ship the room work", []string{"@root"})
	rootCannot(t, err)

	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	g, err := e.svc.AddGoal(owner, r.ID, "Owner goal", []string{"@room"})
	if err != nil {
		t.Fatal(err)
	}
	rootSel := []string{"@root"}
	_, err = e.svc.EditGoalSelectors(ctx, r.ID, g.ID, "", &rootSel)
	rootCannot(t, err)
	rootHolder := []string{rootID}
	_, err = e.svc.editGoal(ctx, r.ID, g.ID, "", &rootHolder, false)
	rootCannot(t, err)

	it, err := e.svc.AddGoalItem(owner, r.ID, g.ID, "Promote this work", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.svc.MoveGoalItem(ctx, r.ID, g.ID, it.ID, "new"); err == nil || !strings.Contains(err.Error(), goals.RootHoldsNothing) {
		t.Fatalf("root promoted an item into a goal of its own: %v", err)
	}
	// An agent still holds goals, so the refusal is about the root seat alone.
	agentID := "ses_20261022-000000-aaaaaaaa"
	recordtest.Write(t, e.mgr.Store.Root, agentID, subagent.Meta{ID: agentID, Name: "lead", RoomID: r.ID, Room: r.Name, Depth: 1, Resumable: true, HarnessSession: "mock-fixture"})
	asAgent := authority.WithActor(context.Background(), authority.Actor{Role: authority.Session, Session: agentID})
	if _, err := e.svc.AddGoal(asAgent, r.ID, "Agent goal", nil); err != nil {
		t.Fatalf("an agent was refused its own goal: %v", err)
	}
}
