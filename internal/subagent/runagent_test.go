package subagent

import (
	"context"
	"dyna-agent/internal/authority"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
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

func TestEngineClosesRunAgentsOfAnEndedRunOnly(t *testing.T) {
	e := newTestEnv(t)
	t.Setenv("DYNA_RUN_ID", "wf_over")
	over, err := runstore.Create("over", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYNA_RUN_ID", "wf_live")
	live, err := runstore.Create("live", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { live.Finish("ok", "null", nil) })
	start := func(run string) Info {
		info, err := e.m.StartRunAgent(e.ctx, StartOptions{Run: &RunOwner{ID: run, Agent: 1}, Profile: "mock", Task: "RESPOND: done", Dir: t.TempDir(), Room: e.room.ID})
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	overAgent, liveAgent := start(over.Meta.ID), start(live.Meta.ID)
	// The coordinator is gone: the engine marks the run failed.
	if err := runstore.ForceStatus(over.Meta.ID, "error", "lost"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.TickSessions(e.ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{overAgent.ID: StatusClosed, liveAgent.ID: ""} {
		got, err := e.m.store().Info(id)
		if err != nil || (want == StatusClosed) != (got.Status == StatusClosed) {
			t.Fatalf("session %s status %q, %v; want closed=%v", id, got.Status, err, want == StatusClosed)
		}
	}
}

func TestRunAgentIDsDifferAndKeepTheRunsDate(t *testing.T) {
	seen := map[string]string{}
	for _, run := range []string{"wf_20261026-094500-aaaaaaaa", "wf_20261026-094500-aaaaaaab", "wf_20261027-010101-aaaaaaaa"} {
		for agent := 1; agent <= 40; agent++ {
			id := RunAgentID(run, agent)
			if prev, dup := seen[id]; dup {
				t.Fatalf("%s agent %d and %s share ID %s", run, agent, prev, id)
			}
			seen[id] = run
			if !ValidID(id) || !strings.HasPrefix(id, "ses_"+run[3:18]+"-") {
				t.Fatalf("ID %s is not valid or does not keep the date and time of %s", id, run)
			}
		}
	}
}

func TestCoordinatorAndEngineCanCloseRunAgents(t *testing.T) {
	e := newTestEnv(t)
	coordinator := authority.WithActor(e.ctx, authority.Actor{Role: authority.Worker, Run: "wf_20261026-094500-aaaaaaaa"})
	start := func(agent int, task string) Info {
		info, err := e.m.StartRunAgent(coordinator, StartOptions{Run: &RunOwner{ID: "wf_20261026-094500-aaaaaaaa", Agent: agent}, Profile: "mock", Task: task, Dir: t.TempDir(), Room: e.room.ID})
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	done := start(1, "RESPOND: done")
	if got, err := e.m.WaitRunAgent(coordinator, "wf_20261026-094500-aaaaaaaa", 1, time.Minute); err != nil || !got.Done || got.Turn.Result != "done" {
		t.Fatalf("wait as the coordinator = %+v, %v; want the result", got, err)
	}
	running := start(2, "SLEEP:60\nRESPOND: late")
	if err := e.m.CloseRunAgents(coordinator, "wf_20261026-094500-aaaaaaaa"); err != nil {
		t.Fatalf("close as the coordinator: %v", err)
	}
	for _, id := range []string{done.ID, running.ID} {
		if got, err := e.m.store().Info(id); err != nil || got.Status != StatusClosed {
			t.Fatalf("session %s = %+v, %v; want closed", id, got.Status, err)
		}
	}
	// The engine has no caller: its own cleanup closes a run's agents too.
	late := start(3, "RESPOND: x")
	restore := authority.InEngine()
	defer restore()
	if err := e.m.closeEndedRunAgents(context.Background(), []Meta{late.Meta}); err == nil {
		t.Log("run still live in this test, so nothing to close")
	}
	if _, err := e.m.closeRunAgent(context.Background(), late.ID); err != nil {
		t.Fatalf("engine close without a caller: %v", err)
	}
}
