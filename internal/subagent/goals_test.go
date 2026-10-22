package subagent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"dyna-agent/internal/goals"
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
func TestKeeperPostsWaitForTurnLock(t *testing.T) {
	e := newTestEnv(t)
	id := NewID()
	dir, _ := e.m.Store.Dir(id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// A stored idle session is sufficient; the busy path must not post or wake.
	if err := e.m.Store.writeMeta(Meta{ID: id, Name: "holder", Room: "work", RoomID: "r", Depth: 1}); err != nil {
		t.Fatal(err)
	}
	lock, err := e.m.Store.tryTurnLock(id)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	wrote := false
	done, err := e.m.DeliverGoal(context.Background(), id, func() (string, error) { wrote = true; return "", nil })
	if err != nil || done || wrote {
		t.Fatal(done, wrote, err)
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

// Root and service sessions hold no goals: the keeper leaves them alone, and
// a start must not add a goal that never changes.
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
	return slices.DeleteFunc(all, func(g goals.Goal) bool { return !g.Open() || g.Service })
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
		{name: "none", parent: "parent", wantErr: "no goal"},
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
				g := add("r", goals.Input{Text: "Active work", Holders: []string{"parent"}})
				if tc.active == 1 {
					want = g.ID
				}
			}
			if tc.under != "" {
				want = tc.under
			}
			if tc.parent == "" {
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
