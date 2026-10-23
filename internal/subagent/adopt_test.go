package subagent

import (
	"context"
	"strings"
	"testing"

	"dyna-agent/internal/mode"
	"dyna-agent/internal/room"
	"dyna-agent/internal/store/storetest"
)

// tree starts sessions for adopt tests. Each parent stamp is set before its
// child starts, then cleared, so the caller acts as the owner.
func (e *testEnv) tree(parent *Info) Info {
	e.t.Helper()
	if parent != nil {
		stampParent(e.t, *parent)
	} else {
		e.owner()
	}
	info, _ := e.start("SLEEP:0 RESPOND: ready")
	info, _ = e.wait(info.ID, 1)
	e.owner()
	return info
}

// owner clears the caller stamp and selects this fixture's room.
func (e *testEnv) owner() {
	storetest.ClearStamp(e.t)
	e.t.Setenv(mode.EnvRoomID, e.room.ID)
	e.t.Setenv(mode.EnvRoom, e.room.Name)
}

// hierarchyNotices are the session notes about moves and renames: the typed
// events that replaced the free-text "hierarchy" kind.
func hierarchyNotices(t *testing.T, e *testEnv) []room.Message {
	t.Helper()
	msgs, err := e.m.rooms().Messages(e.room.ID)
	if err != nil {
		t.Fatal(err)
	}
	var out []room.Message
	for _, m := range msgs {
		if m.Kind == "session" && m.Notice.Kind != "instructions" && m.Notice.Kind != "seat" && m.Notice.Kind != "spawned" && m.Notice.Kind != "account" {
			out = append(out, m)
		}
	}
	return out
}

func TestAdoptMovesSubtreeAndRecomputesDepth(t *testing.T) {
	e := newTestEnv(t)
	lead := e.tree(nil)
	a := e.tree(nil)
	b := e.tree(&a)
	moved, err := e.m.Adopt(context.Background(), a.ID, lead.Address())
	if err != nil || moved.ParentSession != lead.ID || moved.Owner != lead.ID || moved.Depth != 2 {
		t.Fatalf("Adopt = %+v, %v", moved, err)
	}
	if got, _ := e.m.Store.Info(b.ID); got.Depth != 3 || got.ParentSession != a.ID {
		t.Fatalf("descendant = depth %d parent %s", got.Depth, got.ParentSession)
	}
	// A top-level session had no parent to tell; the new parent gets one notice.
	notes := hierarchyNotices(t, e)
	if len(notes) != 1 || len(notes[0].To) != 1 || notes[0].To[0] != lead.Address() || notes[0].Wake {
		t.Fatalf("notices = %+v", notes)
	}
	// Moving b from a to lead tells both parents, once each.
	if _, err := e.m.Adopt(context.Background(), b.ID, lead.ID); err != nil {
		t.Fatal(err)
	}
	notes = hierarchyNotices(t, e)
	if len(notes) != 2 || strings.Join(notes[1].To, ",") != a.Address()+","+lead.Address() {
		t.Fatalf("second notice = %+v", notes[len(notes)-1])
	}
	if got, _ := e.m.Store.Info(b.ID); got.Depth != 2 || got.ParentSession != lead.ID {
		t.Fatalf("b = depth %d parent %s", got.Depth, got.ParentSession)
	}
}

func TestAdoptRefusesCyclesAndDepthPastTheLimit(t *testing.T) {
	e := newTestEnv(t)
	lead := e.tree(nil)
	a := e.tree(nil)
	b := e.tree(&a)
	c := e.tree(&b)
	for _, under := range []Info{a, b, c} {
		if _, err := e.m.Adopt(context.Background(), a.ID, under.ID); err == nil || !strings.Contains(err.Error(), "cycle") {
			t.Fatalf("Adopt a under %s err = %v", under.Name, err)
		}
	}
	// a, b and c would sit at depths 2, 3 and 4; the default limit is 3.
	if _, err := e.m.Adopt(context.Background(), a.ID, lead.ID); err == nil || !strings.Contains(err.Error(), "subagent.maxDepth") {
		t.Fatalf("Adopt past max depth err = %v", err)
	}
	for _, s := range []Info{a, b, c} {
		got, _ := e.m.Store.Info(s.ID)
		if got.Depth != s.Depth || got.ParentSession != s.ParentSession {
			t.Fatalf("refused adopt changed %s: %+v", s.Name, got)
		}
	}
	if notes := hierarchyNotices(t, e); len(notes) != 0 {
		t.Fatalf("refused adopt posted %+v", notes)
	}
}

func TestAdoptRefusesAServiceSession(t *testing.T) {
	e := newTestEnv(t)
	lead := e.tree(nil)
	service := e.tree(nil)
	if _, err := e.m.Store.UpdateMeta(service.ID, func(meta *Meta) {
		meta.Role, meta.Service = RoleLibrarian, true
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.Adopt(context.Background(), service.ID, lead.ID); err == nil || !strings.Contains(err.Error(), "dyna services") {
		t.Fatalf("service adopt err = %v", err)
	}
	got, err := e.m.Store.Info(service.ID)
	if err != nil || got.ParentSession != "" {
		t.Fatalf("refused adopt changed the service: %+v, %v", got, err)
	}
}

func TestAdoptAllowsOnlyOwnerRootOrCurrentParent(t *testing.T) {
	e := newTestEnv(t)
	lead := e.tree(nil)
	a := e.tree(nil)
	b := e.tree(&a)
	stampParent(t, lead)
	if _, err := e.m.Adopt(context.Background(), b.ID, lead.ID); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("unrelated session adopt err = %v", err)
	}
	stampParent(t, a)
	if moved, err := e.m.Adopt(context.Background(), b.ID, lead.ID); err != nil || moved.ParentSession != lead.ID {
		t.Fatalf("current parent adopt = %+v, %v", moved, err)
	}
}
