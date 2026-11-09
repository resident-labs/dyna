package engine

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func (r *goalRig) close(session contract.SessionID) {
	r.t.Helper()
	if err := r.st.CloseSession(r.ctx, session, contract.Event{ID: contract.EventID("close-" + string(session)), Room: r.room, Time: r.now(), From: ownerCaller, Body: contract.SessionClosed{Session: session}}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *goalRig) goals(session contract.SessionID, all bool) []contract.Goal {
	r.t.Helper()
	list, e := r.s.GoalList(r.ctx, ownerCaller, contract.GoalListRequest{Room: r.room, Session: session, All: all})
	if e != nil {
		r.t.Fatal(e)
	}
	return list.Goals
}

func TestASessionHoldsOneGoal(t *testing.T) {
	r := newGoalRig(t)
	lead := asSession(r.lead)
	_, e := r.s.GoalSet(r.ctx, lead, contract.GoalSetRequest{Session: r.child, Text: "more", DoneWhen: "done"})
	if e == nil || e.Code != contract.ErrInvalid || !strings.Contains(e.Message, `session child already holds g1 ("ship the port")`) || !strings.Contains(e.Message, "goal item with goal g1") {
		t.Fatalf("second goal: %v", e)
	}
	own, e := r.s.GoalSet(r.ctx, lead, contract.GoalSetRequest{Text: "lead work", DoneWhen: "led"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := r.s.GoalSet(r.ctx, lead, contract.GoalSetRequest{Goal: own.Goal, Session: r.child}); e == nil || !strings.Contains(e.Message, "already holds g1") {
		t.Fatalf("handoff to a holder: %v", e)
	}
	if _, e := r.s.GoalSet(r.ctx, lead, contract.GoalSetRequest{Goal: own.Goal, Text: "lead work, renamed"}); e != nil {
		t.Fatalf("editing one's own goal: %v", e)
	}
}

func TestSubtasksKeepNoOpenItemUnderASettledOne(t *testing.T) {
	r := newGoalRig(t)
	child := asSession(r.child)
	item := func(req contract.GoalItemRequest) (int, *contract.Error) {
		req.Goal = "g1"
		got, e := r.s.GoalItem(r.ctx, child, req)
		return got.Item, e
	}
	top, _ := item(contract.GoalItemRequest{Text: "port"})
	sub, e := item(contract.GoalItemRequest{Text: "tests", Parent: top})
	if e != nil || sub != 2 {
		t.Fatalf("subtask %d: %v", sub, e)
	}
	if _, e := item(contract.GoalItemRequest{Text: "deeper", Parent: sub}); e == nil || !strings.Contains(e.Message, "item 2 is itself a subtask of item 1") {
		t.Fatalf("third level: %v", e)
	}
	if _, e := item(contract.GoalItemRequest{Item: top, State: contract.GoalItemDone}); e == nil || !strings.Contains(e.Message, "item 1 has open subtasks (2)") {
		t.Fatalf("done over an open subtask: %v", e)
	}
	if _, e := item(contract.GoalItemRequest{Item: sub, Parent: top, Text: "x"}); e == nil || !strings.Contains(e.Message, "parent applies only when adding") {
		t.Fatalf("parent on an edit: %v", e)
	}
	if _, e := item(contract.GoalItemRequest{Item: top, State: contract.GoalItemDropped}); e != nil {
		t.Fatal(e)
	}
	states := func() string {
		var out []string
		for _, it := range r.goals(r.child, false)[0].Items {
			out = append(out, string(it.State))
		}
		return strings.Join(out, " ")
	}
	if got := states(); got != "dropped dropped" {
		t.Fatalf("drop cascade: %s", got)
	}
	if _, e := item(contract.GoalItemRequest{Item: sub, State: contract.GoalItemOpen}); e != nil {
		t.Fatal(e)
	}
	if got := states(); got != "open open" {
		t.Fatalf("reopen: %s", got)
	}
	if got := r.goals(r.child, false)[0].Items[1].Parent; got != 1 {
		t.Fatalf("parent %d", got)
	}
}

func TestACloseEndsAFinishedGoalAndFoldsAnOpenOne(t *testing.T) {
	r := newGoalRig(t)
	lead := asSession(r.lead)
	if _, e := r.s.GoalSet(r.ctx, lead, contract.GoalSetRequest{Text: "lead work", DoneWhen: "led"}); e != nil {
		t.Fatal(e)
	}
	// A finished child: no open item, closed on request, ends done.
	r.close(r.child)
	if got := r.goals(r.child, true); len(got) != 1 || got[0].State != contract.GoalDone || got[0].Summary != "ended when child closed" {
		t.Fatalf("finished child: %+v", got)
	}
	// A child with open work folds into the lead's goal.
	second, e := r.s.SessionStart(r.ctx, lead, contract.SessionStartRequest{Room: r.room, Name: "second", Profile: "test", Goal: "review it", DoneWhen: "reviewed"})
	if e != nil {
		t.Fatal(e)
	}
	held := r.goals(second.Session, false)[0].ID
	for _, text := range []string{"read", "write"} {
		if _, e := r.s.GoalItem(r.ctx, asSession(second.Session), contract.GoalItemRequest{Goal: held, Text: text}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := r.s.GoalItem(r.ctx, asSession(second.Session), contract.GoalItemRequest{Goal: held, Item: 1, State: contract.GoalItemDone}); e != nil {
		t.Fatal(e)
	}
	r.close(second.Session)
	goals := r.goals(r.lead, false)
	if len(goals) != 1 {
		t.Fatalf("lead goals %+v", goals)
	}
	items := goals[0].Items
	if len(items) != 2 || items[0].Text != "from second, "+string(held)+": review it — done when: reviewed" || items[1].Text != "write" || items[1].Parent != items[0].Number {
		t.Fatalf("folded items %+v", items)
	}
	ended := r.goals(second.Session, true)[0]
	if ended.State != contract.GoalDropped || !strings.HasPrefix(ended.Summary, "folded into "+string(goals[0].ID)+" as item 1") {
		t.Fatalf("folded goal %+v", ended)
	}
	if _, e := r.s.GoalItem(r.ctx, lead, contract.GoalItemRequest{Goal: held, Text: "late"}); e == nil || !strings.Contains(e.Message, "was folded into "+string(goals[0].ID)+" as item 1") {
		t.Fatalf("using the folded goal: %v", e)
	}
}
