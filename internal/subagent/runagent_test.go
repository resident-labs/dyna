package subagent

import (
	"testing"
	"time"
)

func TestRunAgentStartsOneLeafSessionAndClosesAfterItsResult(t *testing.T) {
	e := newTestEnv(t)
	owner := &RunOwner{ID: "wf_1", Agent: 3, Journal: "/runs/wf_1/agent-3.jsonl", JournalRoot: "/runs"}
	opts := StartOptions{Run: owner, Profile: "mock", Task: "RESPOND: done", Dir: t.TempDir(), Room: e.room.ID}
	first, err := e.m.StartRunAgent(e.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != RunAgentID("wf_1", 3) || !first.Leaf || !first.RunOwned() || first.ParentSession != "" || first.Goal != "" {
		t.Fatalf("run agent session = %+v", first.Meta)
	}
	again, err := e.m.StartRunAgent(e.ctx, opts)
	if err != nil || again.ID != first.ID || again.Turns != 1 {
		t.Fatalf("second start = %+v, %v; want the same session with one turn", again.Meta, err)
	}
	env := map[string]string{}
	stampSession(env, first.Meta)
	if env["DYNA_AGENT_JOURNAL"] != owner.Journal || env["DYNA_AGENT_JOURNAL_ROOT"] != owner.JournalRoot {
		t.Fatalf("session env = %v; want the run journal paths", env)
	}
	got, err := e.m.WaitRunAgent(e.ctx, "wf_1", 3, time.Minute)
	if err != nil || !got.Done || got.Turn.Result != "done" {
		t.Fatalf("wait = %+v, %v; want the first turn's result", got, err)
	}
	closed, err := e.m.store().Info(first.ID)
	if err != nil || closed.Status != StatusClosed {
		t.Fatalf("session after its result = %+v, %v; want closed", closed, err)
	}
	if again, err := e.m.WaitRunAgent(e.ctx, "wf_1", 3, time.Minute); err != nil || !again.Done || again.Turn.Result != "done" {
		t.Fatalf("wait after close = %+v, %v; want the stored result", again, err)
	}
}

func TestRunAgentsAreNotRoomMembers(t *testing.T) {
	e := newTestEnv(t)
	agent := Meta{ID: RunAgentID("wf_1", 1), RoomID: e.room.ID, Room: e.room.Name, Created: time.Now(), Leaf: true, Run: &RunOwner{ID: "wf_1", Agent: 1}}
	plain := Meta{ID: NewID(), RoomID: e.room.ID, Room: e.room.Name, Created: time.Now()}
	got := RoomMembership([]Info{{Meta: agent}, {Meta: plain}}, e.room.ID, true)
	if len(got.Sessions) != 1 || got.Sessions[0].ID != plain.ID {
		t.Fatalf("members = %+v; want only the ordinary session", got.Sessions)
	}
}
