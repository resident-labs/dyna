package roomsvc

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/authority"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/subagent"
)

func TestGoalMutationsUseAuthenticatedCaller(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	other := e.room("other")
	id := "ses_20261020-000001-aaaaaaaa"
	recordtest.Write(t, e.mgr.Store.Root, id, subagent.Meta{ID: id, Name: "helper", RoomID: r.ID, Room: r.Name, Depth: 1, Resumable: true, HarnessSession: "mock-fixture"})
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: id})
	g, err := e.svc.AddGoal(ctx, "", "Ship the engine calls", nil)
	if err != nil || g.Setter != id || !g.HeldBy(id) {
		t.Fatalf("add = %+v, %v", g, err)
	}
	g, err = e.svc.EditGoalSelectors(ctx, r.ID, g.ID, "Ship all engine calls", nil)
	if err != nil || g.Edits[len(g.Edits)-1].By != id {
		t.Fatalf("edit = %+v, %v", g, err)
	}
	g, err = e.svc.WaitGoal(ctx, r.ID, g.ID, time.Now().Add(time.Hour))
	if err != nil || g.Edits[len(g.Edits)-1].By != id {
		t.Fatalf("wait = %+v, %v", g, err)
	}
	g, err = e.svc.FinishGoal(ctx, r.ID, g.ID, "Shipped", false)
	if err != nil || g.Done == nil || g.Done.By != id {
		t.Fatalf("finish = %+v, %v", g, err)
	}
	g, err = e.svc.EditGoal(ctx, r.ID, g.ID, "Reopen the goal", []string{id}, false)
	if err != nil || g.Edits[len(g.Edits)-1].By != id {
		t.Fatalf("web edit = %+v, %v", g, err)
	}
	if _, err := e.svc.CheckGoal(ctx, r.ID, g.ID); err != nil {
		t.Fatal(err)
	}
	it, err := e.svc.AddGoalItem(ctx, r.ID, g.ID, "Promote this work", "")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := e.svc.MoveGoalItem(ctx, r.ID, g.ID, it.ID, "new")
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := e.svc.Goal(r.ID, moved.To.Goal)
	if err != nil || promoted.Setter != id || !promoted.HeldBy(id) {
		t.Fatalf("promotion ignored the authenticated caller: %+v, %v", promoted, err)
	}
	actions := []func(context.Context, string) error{
		func(ctx context.Context, room string) error {
			_, err := e.svc.AddGoal(ctx, room, "Wrong caller", nil)
			return err
		},
		func(ctx context.Context, room string) error {
			_, err := e.svc.EditGoal(ctx, room, g.ID, "Wrong caller", nil, true)
			return err
		},
		func(ctx context.Context, room string) error {
			_, err := e.svc.EditGoalSelectors(ctx, room, g.ID, "Wrong caller", nil)
			return err
		},
		func(ctx context.Context, room string) error {
			_, err := e.svc.WaitGoal(ctx, room, g.ID, time.Now().Add(time.Hour))
			return err
		},
		func(ctx context.Context, room string) error {
			_, err := e.svc.FinishGoal(ctx, room, g.ID, "Wrong caller", true)
			return err
		},
		func(ctx context.Context, room string) error { _, err := e.svc.CheckGoal(ctx, room, g.ID); return err },
		func(ctx context.Context, room string) error {
			_, err := e.svc.MoveGoalItem(ctx, room, g.ID, it.ID, "new")
			return err
		},
	}
	for i, action := range actions {
		if err := action(ctx, other.ID); err == nil {
			t.Fatalf("action %d accepted another room", i)
		}
		for _, actor := range []authority.Actor{{}, {Role: authority.Worker, Run: "wf_1"}, {Role: authority.Session}} {
			if err := action(authority.WithActor(t.Context(), actor), r.ID); err == nil {
				t.Fatalf("action %d accepted actor %+v", i, actor)
			}
		}
	}
}

func TestExpediteGoalIsOwnerOrRootOnly(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	id := "ses_20261020-000001-aaaaaaaa"
	recordtest.Write(t, e.mgr.Store.Root, id, subagent.Meta{ID: id, Name: "helper", RoomID: r.ID, Room: r.Name, Depth: 1, Resumable: true, HarnessSession: "mock-fixture"})
	session := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: id})
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	first, err := e.svc.AddGoal(owner, r.ID, "First", []string{"@room"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.svc.AddGoal(owner, r.ID, "Second", []string{"@room"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ExpediteGoal(session, r.ID, first.ID, false); err == nil {
		t.Fatal("a session expedited a goal")
	}
	if g, err := e.svc.ExpediteGoal(owner, r.ID, first.ID, false); err != nil || g.ExpediteUntil == nil {
		t.Fatalf("owner expedite = %+v, %v", g, err)
	}
	if _, err := e.svc.ExpediteGoal(owner, r.ID, second.ID, false); err == nil {
		t.Fatal("a second expedited goal needs replace")
	}
	if _, err := e.svc.ExpediteGoal(session, r.ID, "", false); err == nil {
		t.Fatal("a session stopped the priority")
	}
	if g, err := e.svc.ExpediteGoal(owner, r.ID, "", false); err != nil || g.ID != first.ID || g.ExpediteUntil != nil {
		t.Fatalf("stop = %+v, %v", g, err)
	}
}

func TestRuleMutationsUseAuthenticatedCaller(t *testing.T) {
	e := newEnv(t)
	r := e.room("work")
	other := e.room("other")
	owner := authority.WithActor(t.Context(), authority.Actor{Role: authority.Owner})
	rule, err := e.svc.AddRule(owner, r.ID, false, "Owner rule")
	if err != nil {
		t.Fatal(err)
	}
	id := "ses_20261020-000001-aaaaaaaa"
	recordtest.Write(t, e.mgr.Store.Root, id, subagent.Meta{ID: id, Name: "helper", RoomID: r.ID, Room: r.Name, Depth: 1, Resumable: true})
	ctx := authority.WithActor(t.Context(), authority.Actor{Role: authority.Session, Session: id})
	for _, global := range []bool{false, true} {
		added, err := e.svc.AddRule(ctx, "", global, "Agent rule")
		if err != nil || added.Setter != id || added.SetterName != "helper" {
			t.Fatalf("add = %+v, %v", added, err)
		}
		if _, err := e.svc.EditRule(ctx, r.ID, global, added.ID, "Changed"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.svc.RemoveRule(ctx, r.ID, global, added.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.EditRule(ctx, r.ID, false, rule.ID, "Changed"); err == nil {
		t.Fatal("session changed owner rule")
	}
	if _, err := e.svc.RemoveRule(ctx, r.ID, false, rule.ID); err == nil {
		t.Fatal("session removed owner rule")
	}
	for _, global := range []bool{false, true} {
		if _, err := e.svc.AddRule(ctx, other.ID, global, "Wrong room"); err == nil {
			t.Fatal("cross-room rule accepted")
		}
	}
	for _, actor := range []authority.Actor{{}, {Role: authority.Worker, Run: "wf_1"}} {
		denied := authority.WithActor(t.Context(), actor)
		if _, err := e.svc.AddRule(denied, r.ID, false, "Wrong caller"); err == nil {
			t.Fatal("rule accepted wrong caller")
		}
		if _, err := e.svc.EditRule(denied, r.ID, false, rule.ID, "Wrong caller"); err == nil {
			t.Fatal("rule edit accepted wrong caller")
		}
		if _, err := e.svc.RemoveRule(denied, r.ID, false, rule.ID); err == nil {
			t.Fatal("rule removal accepted wrong caller")
		}
	}
	view, err := e.svc.Rules(r.ID, false)
	if err != nil || len(view.Rules) != 1 || view.Rules[0].Setter != "owner" {
		t.Fatalf("rules = %+v, %v", view, err)
	}
}
