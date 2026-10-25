package subagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/sessionclose"
	"dyna-agent/internal/settings"
)

// writeTestSession stores a session record with the directory a real session
// has, so a move can lock it.
func writeTestSession(t *testing.T, e *testEnv, meta Meta) {
	t.Helper()
	dir, err := e.m.Store.Dir(meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	if meta.Dir == "" {
		meta.Dir = dir
	}
	if err := e.m.Store.writeMeta(meta); err != nil {
		t.Fatal(err)
	}
}

// childWithWork is a session that holds a goal, so a close must move it rather
// than close it.
func childWithWork(t *testing.T, e *testEnv, parent Meta, depth int) Meta {
	t.Helper()
	child := Meta{ID: NewID(), Name: "kid", RoomID: e.room.ID, Room: e.room.Name, Depth: depth, ParentSession: parent.ID}
	writeTestSession(t, e, child)
	if _, err := e.m.goalStore().Add(e.room.ID, child.ID, goals.Input{Text: "Open work", Holders: []string{child.ID}}); err != nil {
		t.Fatal(err)
	}
	return child
}

// A child that could not move is named by its own outcome, so the app can show
// which child stayed behind and offer to move it.
func TestChildHandoffFailureNamesTheChild(t *testing.T) {
	e := newTestEnv(t)
	// The depth cap refuses the move; the child is the work that stays behind.
	e.m.Settings = func() settings.Values {
		v := settings.Defaults()
		v.SubagentMaxDepth = 1
		return v
	}
	parent := Meta{ID: NewID(), Name: "parent", RoomID: e.room.ID, Room: e.room.Name, Depth: 1}
	writeTestSession(t, e, parent)
	child := childWithWork(t, e, parent, 2)
	heir := Info{Meta: Meta{ID: NewID(), Name: "heir", RoomID: e.room.ID, Room: e.room.Name, Depth: 2}}
	writeTestSession(t, e, heir.Meta)

	_, summary, err := e.m.handOverChildren(e.ctx, parent, heir)
	if err == nil || !strings.Contains(err.Error(), "subagent.maxDepth") {
		t.Fatalf("handoff error = %v, want the depth cap", err)
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

// A child that became top level names no receiver, so its row says "top level"
// instead of an address built from an empty heir.
func TestTopLevelChildMoveNamesNoReceiver(t *testing.T) {
	e := newTestEnv(t)
	parent := Meta{ID: NewID(), Name: "parent", RoomID: e.room.ID, Room: e.room.Name, Depth: 1}
	writeTestSession(t, e, parent)
	child := childWithWork(t, e, parent, 2)

	_, summary, err := e.m.handOverChildren(e.ctx, parent, Info{})
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range summary {
		if outcome.Kind != sessionclose.ResponsibilityChild || outcome.Result != sessionclose.ResultMoved {
			continue
		}
		if outcome.ID != child.ID || outcome.To != "" {
			t.Fatalf("top-level child row = %+v, want %s with no destination", outcome, child.ID)
		}
		return
	}
	t.Fatalf("handoff outcomes = %+v, want a moved row for child %s", summary, child.ID)
}
