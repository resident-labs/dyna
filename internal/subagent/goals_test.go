package subagent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/structuredoutput"
)

// Goals change every turn, so they stay in the turn prompt, never in the
// frozen instructions.
func TestGoalsStayInTheTurnPrompt(t *testing.T) {
	block := goals.Frame([]goals.Goal{{ID: "g1", Text: "Ship the relay", State: "active", Shared: true, Setter: "owner"}}, "a", nil, nil)
	withGoal := firstTurnPrompt("room context\n"+block, "Task")
	schema := withSchema(withGoal, `{"type":"object"}`)
	if !strings.Contains(withGoal, "Ship the relay") || strings.Index(schema, "[DYNA GOALS]") < strings.Index(schema, structuredoutput.SchemaSuffix(`{"type":"object"}`)) {
		t.Fatal("goal block moved before the schema or out of the prompt")
	}
	if !strings.Contains(followUpPrompt(2, "Next", "room context\n"+block), block) {
		t.Fatal("follow-up lost goals")
	}
}
func TestGoalFrameNudgesInRoomWithNoGoals(t *testing.T) {
	m := &Manager{Store: &Store{Root: filepath.Join(t.TempDir(), "sessions")}}
	frame := m.goalFrame(Meta{ID: "ses_x", RoomID: "room_empty"}, "")
	if !strings.Contains(frame, "None open for you") {
		t.Fatalf("empty room frame = %q; want the goal nudge", frame)
	}
}

func TestStartCreatesSpawnGoalWithBrief(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	const text, brief = "Ship the decoder fix; done when CI passes", "SLEEP:1 RESPOND: ok. Own internal/decoder only."
	lead, _ := e.start(brief, func(so *StartOptions) { so.Goal = text })
	lead, _ = e.wait(lead.ID, 1)
	g, err := e.m.goalStore().Get(lead.RoomID, lead.Goal)
	if err != nil || g.Text != text || g.Detail != brief || g.Setter != "owner" || !g.Open() || !slices.Equal(g.Holders, []string{lead.ID}) {
		t.Fatalf("spawn goal = %+v, %v", g, err)
	}
	stampParent(t, lead)
	child, _ := e.start("SLEEP:1 RESPOND: child", func(so *StartOptions) { so.Goal = "Check the child" })
	// A child holds an item on the lead's goal, set by the lead.
	if child.Goal != lead.Goal || child.Item == "" {
		t.Fatalf("child goal=%q item=%q, want an item on %q", child.Goal, child.Item, lead.Goal)
	}
	items, err := e.m.goalStore().Items(child.RoomID, lead.Goal)
	if err != nil || len(items) != 1 || items[0].Assignee != child.ID || !strings.HasPrefix(items[0].Text, "Check the child") {
		t.Fatalf("child item = %v, %v", items, err)
	}
	e.wait(child.ID, 1)
}

// Root and service starts must not add goals that never change.
func TestRootAndReviewerStartsCreateNoGoal(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	for _, role := range []string{"reviewer", RoleRoot} {
		info, _ := e.start("SLEEP:1 RESPOND: first assignment", func(so *StartOptions) {
			so.Role, so.Name, so.Goal = role, role, "Serve the room"
			so.Service = role != RoleRoot
		})
		info, _ = e.wait(info.ID, 1)
		held, err := e.m.goalStore().OpenHeldBy(info.RoomID, info.ID)
		if info.Goal != "" || err != nil || len(held) != 0 {
			t.Fatalf("%s start: goal=%q held=%v err=%v", role, info.Goal, held, err)
		}
	}
}

// Root holds no goals, so an implicit lookup gives the child a goal of its own.
// An explicit --under still selects its goal, and a bad ID is still an error.
func TestRootStartNeverJoinsARootGoal(t *testing.T) {
	m := &Manager{Store: &Store{Root: filepath.Join(t.TempDir(), "sessions")}}
	rootID := "ses_20261022-000000-rrrrrrrr"
	if err := m.Store.writeMeta(Meta{ID: rootID, Name: RoleRoot, Role: RoleRoot, Service: true, RoomID: "r", Room: "work", Depth: 1}); err != nil {
		t.Fatal(err)
	}
	store := m.goalStore()
	rootGoal, err := store.Add("r", rootID, goals.Input{Text: "Room work", Holders: []string{rootID}})
	if err != nil {
		t.Fatal(err)
	}
	// An implicit lookup finds no root goal.
	meta := Meta{ID: "child", RoomID: "r", ParentSession: rootID}
	goalID, itemID, err := m.addSpawnGoal(meta, "", "Child work", "Brief")
	if err != nil || itemID != "" || goalID == rootGoal.ID {
		t.Fatalf("implicit start: goal=%q item=%q err=%v", goalID, itemID, err)
	}
	if g, err := store.Get("r", goalID); err != nil || !g.HeldBy(meta.ID) {
		t.Fatalf("implicit goal = %+v, err=%v", g, err)
	}
	// An explicit --under names a goal an agent holds, so the child joins it.
	leadGoal, err := store.Add("r", "owner", goals.Input{Text: "Lead work", Holders: []string{"lead"}})
	if err != nil {
		t.Fatal(err)
	}
	goalID, itemID, err = m.addSpawnGoal(Meta{ID: "child-2", RoomID: "r", ParentSession: rootID}, leadGoal.ID, "Child work", "Brief")
	if err != nil || itemID == "" || goalID != leadGoal.ID {
		t.Fatalf("explicit agent goal: goal=%q item=%q err=%v", goalID, itemID, err)
	}
	// A goal root holds, and a bad ID, are both refused.
	for _, under := range []string{rootGoal.ID, "g99"} {
		if _, _, err := m.addSpawnGoal(Meta{ID: "child-3", RoomID: "r", ParentSession: rootID}, under, "Child work", "Brief"); err == nil {
			t.Fatalf("under=%q accepted", under)
		}
	}
	if items, err := store.Items("r", rootGoal.ID); err != nil || len(items) != 0 {
		t.Fatalf("root goal items = %v, %v", items, err)
	}
}

func TestStartWithInvalidGoalLeavesNoSession(t *testing.T) {
	e := newTestEnv(t)
	e.joinRunners()
	lead, _ := e.start("SLEEP:1 RESPOND: lead")
	lead, _ = e.wait(lead.ID, 1)
	// The room holds any number of open goals, so goal text validation is the
	// deterministic way a start fails before a session exists.
	before, _ := e.m.Store.List(true)
	_, _, err := e.m.Start(context.Background(), StartOptions{Goal: strings.Repeat("x", 2001), Profile: "mock", Dir: t.TempDir(), Task: "x"})
	if err == nil || !strings.Contains(err.Error(), "--goal: goal text exceeds 2000 characters") {
		t.Fatalf("invalid goal start: %v", err)
	}
	if after, _ := e.m.Store.List(true); len(after) != len(before) {
		t.Fatal("rejected start left a session")
	}
	svc, _ := e.start("SLEEP:1 RESPOND: service", func(so *StartOptions) { so.Service, so.Goal = true, "Serve the room" })
	e.wait(svc.ID, 1)
}

func (e *testEnv) openGoals(roomID string) []goals.Goal {
	all, _ := e.m.goalStore().List(roomID)
	return slices.DeleteFunc(all, func(g goals.Goal) bool { return !g.Open() })
}

func TestSpawnGoalUsesEffectiveGoal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		active  int
		parent  string
		under   string
		wantErr string
	}{
		{name: "top-level"},
		{name: "top-level explicit", active: 2, under: "g2"},
		{name: "none", parent: "parent"},
		{name: "self-set", active: 1, parent: "parent"},
		{name: "one", active: 1, parent: "parent"},
		{name: "ambiguous", active: 2, parent: "parent", wantErr: "ambiguous"},
		{name: "explicit under", active: 2, parent: "parent", under: "g2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &Manager{Store: &Store{Root: filepath.Join(t.TempDir(), "sessions")}}
			store := m.goalStore()
			add := func(room string, in goals.Input) goals.Goal {
				t.Helper()
				g, err := store.Add(room, "parent", in)
				if err != nil {
					t.Fatal(err)
				}
				return g
			}
			// Shared, finished, other-room and setter-only goals are not held work.
			add("r", goals.Input{Text: "Shared", Shared: true})
			add("r", goals.Input{Text: "Assigned elsewhere", Holders: []string{"other"}})
			add("other-room", goals.Input{Text: "Other room", Holders: []string{"parent"}})
			closed := add("r", goals.Input{Text: "Finished", Holders: []string{"parent"}})
			if _, err := store.Finish("r", closed.ID, "parent", "Done", false); err != nil {
				t.Fatal(err)
			}
			want := ""
			for i := 0; i < tc.active; i++ {
				setter := "owner"
				if tc.name == "self-set" {
					setter = "parent"
				}
				g, err := store.Add("r", setter, goals.Input{Text: "Active work", Holders: []string{"parent"}})
				if err != nil {
					t.Fatal(err)
				}
				if tc.active == 1 {
					want = g.ID
				}
			}
			if tc.under != "" {
				want = tc.under
			}
			if tc.parent == "" && tc.under == "" {
				meta := Meta{ID: "child", RoomID: "r"}
				goalID, itemID, err := m.addSpawnGoal(meta, tc.under, "Child work", "Brief")
				if err != nil {
					t.Fatal(err)
				}
				if goalID == "" || itemID != "" {
					t.Fatalf("top-level spawn: goal=%q item=%q", goalID, itemID)
				}
				g, err := store.Get("r", goalID)
				if err != nil || !g.HeldBy("child") {
					t.Fatalf("top-level goal = %+v err=%v", g, err)
				}
				return
			}
			meta := Meta{ID: "child", RoomID: "r", ParentSession: tc.parent}
			goalID, itemID, err := m.addSpawnGoal(meta, tc.under, "Child work", "Brief")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.active == 0 || tc.name == "self-set" {
				g, err := store.Get("r", goalID)
				if err != nil || itemID != "" || !g.HeldBy("child") || g.Setter != tc.parent || g.Detail != "Brief" {
					t.Fatalf("standalone child goal = %+v, item=%q, err=%v", g, itemID, err)
				}
				return
			}
			if goalID != want || itemID == "" {
				t.Fatalf("spawn: goal=%q item=%q want goal %q", goalID, itemID, want)
			}
			items, err := store.Items("r", want)
			if err != nil || len(items) != 1 {
				t.Fatalf("items = %v err=%v", items, err)
			}
			it := items[0]
			if it.Assignee != "child" || it.State != goals.ItemOpen || !strings.HasPrefix(it.Text, "Child work") || !strings.Contains(it.Text, "Brief") {
				t.Fatalf("item = %+v", it)
			}
			// A delegation creates no goal of its own.
			held, err := store.OpenHeldBy("r", "child")
			if err != nil || len(held) != 0 {
				t.Fatalf("child holds goals: %v err=%v", held, err)
			}
		})
	}
}

func TestMovedSpawnInstructionsAndResponsibilities(t *testing.T) {
	e := newTestEnv(t)
	gs := e.m.goalStore()
	meta := Meta{ID: NewID(), Name: "worker", Room: "work", RoomID: "r", Depth: 2, ParentSession: "parent", Resumable: true, HarnessSession: "mock"}
	recordtest.Write(t, e.m.Store.Root, meta.ID, meta)
	source, err := gs.Add("r", "owner", goals.Input{Text: "Source", Holders: []string{"parent"}})
	if err != nil {
		t.Fatal(err)
	}
	dest, err := gs.Add("r", "owner", goals.Input{Text: "Destination", Holders: []string{"parent"}})
	if err != nil {
		t.Fatal(err)
	}
	it, err := gs.AddItem("r", source.ID, "owner", "Fix it\n\nBrief:\nKeep context", meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gs.AddItem("r", source.ID, "owner", "Already complete", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := gs.SetItemState("r", source.ID, "i2", "owner", goals.ItemDone); err != nil {
		t.Fatal(err)
	}
	meta.Goal, meta.Item = source.ID, it.ID
	recordtest.Write(t, e.m.Store.Root, meta.ID, meta)
	// Warm the metadata cache before moving the work.
	if _, err := e.m.Store.Info(meta.ID); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{dest.ID, "new"} {
		moved, err := gs.MoveItem("r", meta.Goal, meta.Item, "owner", to)
		if err != nil {
			t.Fatal(err)
		}
		info, err := e.m.Store.Info(meta.ID)
		if err != nil || info.Goal != moved.To.Goal || info.Item != moved.To.Item || info.ParentSession != "parent" || info.Depth != 2 {
			t.Fatalf("cached placement = %+v, %v", info, err)
		}
		block, hasBrief := e.m.spawnGoalBlock(info.Meta)
		command := "dyna goal item done " + moved.To.Goal + " " + moved.To.Item
		if to == "new" {
			command = "dyna goal done " + moved.To.Goal
		}
		if !hasBrief || !strings.Contains(block, command) || !strings.Contains(block, "Keep context") {
			t.Fatalf("spawn prompt did not follow the work: %s", block)
		}
		owned, err := e.m.itemOwned(map[string]Info{info.ID: info})
		if err != nil {
			t.Fatal(err)
		}
		if to == "new" {
			held, err := e.m.goalOwned(map[string]Info{info.ID: info})
			if err != nil || len(owned) != 0 || len(held) != 1 || held[0].r.ID != moved.To.Goal {
				t.Fatalf("promoted responsibilities: items=%+v goals=%+v err=%v", owned, held, err)
			}
		} else if len(owned) != 1 || owned[0].r.ID != moved.To.String() {
			t.Fatalf("moved responsibility = %+v", owned)
		}
		meta = info.Meta
	}
	items, err := gs.Items("r", source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status := goals.ComputeStatus(source, items, goals.Facts{}, source.CreatedAt); status.Label != goals.StatusReadyToClose || status.Open != 0 {
		t.Fatalf("moved item still blocks the source: %+v", status)
	}
}

// No prompt asks root or a service session to set a goal: they get neither the
// goal rule nor the goal frame. An ordinary worker still gets both.
func TestOnlyWorkersAreToldToSetGoals(t *testing.T) {
	m := &Manager{Store: &Store{Root: filepath.Join(t.TempDir(), "sessions")}}
	for _, tc := range []struct {
		name  string
		meta  Meta
		goals bool
	}{
		{"root", Meta{ID: "r", RoomID: "room", Role: RoleRoot, Service: true}, false},
		{"reviewer", Meta{ID: "v", RoomID: "room", Role: "reviewer", Service: true}, false},
		{"worker", Meta{ID: "w", RoomID: "room"}, true},
	} {
		prompt := rulesFor(tc.meta) + "\n" + m.goalFrame(tc.meta, "")
		if got := strings.Contains(prompt, "dyna goal --for @<you>"); got != tc.goals {
			t.Errorf("%s: goal rule present = %v, want %v", tc.name, got, tc.goals)
		}
		if got := m.goalFrame(tc.meta, "") != ""; got != tc.goals {
			t.Errorf("%s: goal frame present = %v, want %v", tc.name, got, tc.goals)
		}
	}
}
