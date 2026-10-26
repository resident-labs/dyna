package subagent

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/room"
)

// Folding b into a merges b's checklist into a's effective goal: b's goal
// closes as folded, items move with fresh IDs, assignees follow to a, and
// session records remap. No goal row changes hands.
func TestConsolidationMergesGoalItems(t *testing.T) {
	e, _ := consolidationEnv(t)
	e.joinRunners()
	a, _ := e.start("SLEEP:1 RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("SLEEP:1 RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	stampParent(t, b)
	c, _ := e.start("RESPOND: c")
	c, _ = e.wait(c.ID, 1)
	e.joinRunners()()
	gs := e.m.goalStore()
	gA, gB := a.Goal, b.Goal
	// b is also assignee of an item on a shared goal: only the assignee
	// follows b to a.
	shared, err := gs.Add(b.RoomID, "owner", goals.Input{Text: "Shared", Shared: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gs.AddItem(b.RoomID, shared.ID, "owner", "shared work", b.ID); err != nil {
		t.Fatal(err)
	}
	// The survivor also works on two other goals. Its spawn goal still
	// determines where absorbed checklist items go.
	for _, text := range []string{"Other goal one", "Other goal two"} {
		other, err := gs.Add(a.RoomID, "owner", goals.Input{Text: text, Shared: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := gs.AddItem(a.RoomID, other.ID, "owner", text, a.ID); err != nil {
			t.Fatal(err)
		}
	}
	fold := consolidatePair(t, e, a, b)
	submitAll(t, e, fold)
	finishConsolidation(t, e, fold)

	fresh, err := gs.Get(b.RoomID, gB)
	if err != nil || fresh.State != "done" || fresh.Done == nil || !strings.Contains(fresh.Done.Summary, "folded") {
		t.Fatalf("gB = %+v err=%v", fresh, err)
	}
	items, err := gs.Items(b.RoomID, gA)
	if err != nil || len(items) != 1 {
		t.Fatalf("gA items = %v err=%v", items, err)
	}
	if items[0].Assignee != c.ID || items[0].State != goals.ItemOpen {
		t.Fatalf("moved item = %+v", items[0])
	}
	got, err := e.m.Store.Info(c.ID)
	if err != nil || got.Goal != gA || got.Item != items[0].ID {
		t.Fatalf("c -> goal %q item %q, want %q %q (err %v)", got.Goal, got.Item, gA, items[0].ID, err)
	}
	sharedItems, err := gs.Items(b.RoomID, shared.ID)
	if err != nil || len(sharedItems) != 1 || sharedItems[0].Assignee != a.ID {
		t.Fatalf("shared item = %v err=%v", sharedItems, err)
	}
	// a keeps exactly its own goal; nothing was transferred to it.
	held, err := gs.OpenHeldBy(b.RoomID, a.ID)
	if err != nil || len(held) != 1 || held[0].ID != gA {
		t.Fatalf("a holds = %v err=%v", held, err)
	}
}

// A done kept goal takes no folded items, and it must not block the commit.
func TestConsolidationCommitsWhenKeptGoalIsDone(t *testing.T) {
	e, _ := consolidationEnv(t)
	e.joinRunners()
	a, _ := e.start("RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	e.joinRunners()()
	gs := e.m.goalStore()
	if _, err := gs.Finish(a.RoomID, a.Goal, a.ID, "finished", false); err != nil {
		t.Fatal(err)
	}
	// The spawn goal is closed and cannot receive the source checklist.
	if _, err := gs.AddItem(b.RoomID, b.Goal, b.ID, "Keep this work", ""); err != nil {
		t.Fatal(err)
	}
	fold := consolidatePair(t, e, a, b)
	if err := e.m.commitConsolidation(fold); err != nil {
		t.Fatalf("commit with a done kept goal: %v", err)
	}
	fresh, err := e.m.Store.Info(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Closed.IsZero() || fresh.ConsumedBy != a.ID {
		t.Fatalf("source not absorbed: %+v", fresh.Meta)
	}
	items, err := gs.Items(b.RoomID, a.Goal)
	if err != nil || len(items) != 0 {
		t.Fatalf("done goal received items: %v, %v", items, err)
	}
	// The source's unfinished goal stays open with its checklist, and the kept
	// session holds it.
	src, err := gs.Get(b.RoomID, b.Goal)
	if err != nil || !src.Open() || !src.HeldBy(a.ID) || src.HeldBy(b.ID) {
		t.Fatalf("source goal = %+v, err %v", src, err)
	}
	kept, err := gs.Items(b.RoomID, b.Goal)
	if err != nil || len(kept) != 1 {
		t.Fatalf("source checklist changed: %v, %v", kept, err)
	}
}

// A saved error in the committing phase never allows cancellation: the commit
// may have closed sources already, so the run can only resume.
func TestConsolidationCommittingWithErrorCannotCancel(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	c := consolidatePair(t, e, a, b)
	if err := e.m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error {
		s.Phase, s.Error = "committing", "Paused: a later fold step failed. Resume to continue."
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.CancelConsolidation(context.Background(), c.RoomID, c.ID); err == nil {
		t.Fatal("cancelled a committing consolidation")
	}
	got, err := e.m.consolidation(c.RoomID, c.ID)
	if err != nil || got.Phase != "committing" {
		t.Fatalf("state = %+v, err %v", got, err)
	}
}

// A consolidation that pauses in a startup phase tells root with a typed
// alert, and its saved error never blocks cancellation.
func TestConsolidationPausedAlertsRootAndCanCancel(t *testing.T) {
	e, _ := consolidationEnv(t)
	a, _ := e.start("RESPOND: a")
	a, _ = e.wait(a.ID, 1)
	b, _ := e.start("RESPOND: b")
	b, _ = e.wait(b.ID, 1)
	c := consolidatePair(t, e, a, b)
	if err := e.m.updateConsolidation(c.Target, c.ID, func(s *ConsolidationState) error { s.Phase = "starting"; return nil }); err != nil {
		t.Fatal(err)
	}
	// The target has no transfer, so the startup step fails and pauses.
	if _, err := e.m.Store.UpdateMeta(c.Target, func(meta *Meta) { meta.Transfer = nil }); err != nil {
		t.Fatal(err)
	}
	if err := e.m.runConsolidation(e.ctx, c.Target, c.ID); err == nil {
		t.Fatal("startup step succeeded without a target transfer")
	}
	paused, err := e.m.consolidation(c.RoomID, c.ID)
	if err != nil || paused.Error == "" {
		t.Fatalf("paused = %+v, err %v", paused, err)
	}
	posts, err := e.m.rooms().Messages(c.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, post := range posts {
		if alert, ok := post.Alert(); ok && alert.Source == room.AlertConsolidation && strings.Contains(alert.Remedy, c.ID) {
			found = true
		}
	}
	if !found {
		t.Fatal("no consolidation alert reached root")
	}
	got, err := e.m.CancelConsolidation(context.Background(), c.RoomID, c.ID)
	if err != nil || got.Phase != "cancelled" {
		t.Fatalf("cancel = %+v, err %v", got, err)
	}
}
