package subagent

import (
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/settings"
)

// A child that could not move is named by its own outcome, so the app can show
// which child stayed behind and offer to move it.
func TestChildHandoffFailureNamesTheChild(t *testing.T) {
	e := newTestEnv(t)
	// The depth cap makes the move fail; the child itself is the work that
	// stayed behind.
	e.m.Settings = func() settings.Values {
		v := settings.Defaults()
		v.SubagentMaxDepth = 1
		return v
	}
	parent := Meta{ID: NewID(), Name: "parent", RoomID: e.room.ID, Room: e.room.Name, Depth: 1}
	child := Meta{ID: NewID(), Name: "kid", RoomID: e.room.ID, Room: e.room.Name, Depth: 2, ParentSession: parent.ID}
	heir := Info{Meta: Meta{ID: NewID(), Name: "heir", RoomID: e.room.ID, Room: e.room.Name, Depth: 2}}
	for _, meta := range []Meta{parent, child, heir.Meta} {
		if err := e.m.Store.writeMeta(meta); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.m.goalStore().Add(e.room.ID, child.ID, goals.Input{Text: "Open work", Holders: []string{child.ID}}); err != nil {
		t.Fatal(err)
	}
	_, summary, err := e.m.handOverChildren(e.ctx, parent, heir)
	if err == nil {
		t.Fatal("child moved past subagent.maxDepth")
	}
	for _, outcome := range summary {
		if outcome.Kind != sessionclose.ResponsibilityChild || outcome.Result != sessionclose.ResultFailed {
			continue
		}
		if outcome.ID != child.ID || outcome.Name != child.Name || outcome.Failure != sessionclose.FailureChildHandoff {
			t.Fatalf("failed child row = %+v, want %s (%s) with a child handoff failure", outcome, child.Name, child.ID)
		}
		return
	}
	t.Fatalf("handoff outcomes = %+v, want a failed row for child %s", summary, child.ID)
}
