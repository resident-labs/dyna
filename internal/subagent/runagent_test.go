package subagent

import (
	"context"
	"dyna-agent/internal/authority"
	"dyna-agent/internal/profile"
	"io"
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
	e := newTestEnv(t, mockProfile("mock"), profile.Profile{Name: "hold", Harness: profile.HarnessClaudeCode})
	// The fake process waits only for cancellation, so no timer controls when
	// the harness starts or ends.
	started, cancelled := make(chan struct{}), make(chan struct{})
	e.m.ProcessRunner = func(ctx context.Context, _ []string, _, _ io.Writer) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}
	const run = "wf_20261026-094500-aaaaaaaa"
	coordinator := authority.WithActor(e.ctx, authority.Actor{Role: authority.Worker, Run: run})
	start := func(agent int, prof, task string) Info {
		info, err := e.m.StartRunAgent(coordinator, StartOptions{Run: &RunOwner{ID: run, Agent: agent}, Profile: prof, Task: task, Dir: t.TempDir(), Room: e.room.ID})
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	done := start(1, "mock", "RESPOND: done")
	if got, err := e.m.WaitRunAgent(coordinator, run, 1, time.Minute); err != nil || !got.Done || got.Turn.Result != "done" {
		t.Fatalf("wait as the coordinator = %+v, %v; want the result", got, err)
	}
	single := start(4, "mock", "RESPOND: y")
	// One agent of a run closes alone, for a nested script that ends early.
	if err := e.m.CloseRunAgent(coordinator, run, 4); err != nil {
		t.Fatalf("close one agent as the coordinator: %v", err)
	}
	if got, err := e.m.store().Info(single.ID); err != nil || got.Status != StatusClosed {
		t.Fatalf("agent 4 = %+v, %v; want closed", got.Status, err)
	}
	running := start(2, "hold", "wait for stop")
	<-started
	if err := e.m.CloseRunAgents(coordinator, run); err != nil {
		t.Fatalf("close as the coordinator: %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("close returned before it cancelled the harness")
	}
	if turn, err := e.m.store().ReadTurn(running.ID, 1); err != nil || turn.Status != TurnStopped {
		t.Fatalf("turn of the closed agent = %+v, %v; want stopped", turn.Status, err)
	}
	for _, id := range []string{done.ID, running.ID} {
		if got, err := e.m.store().Info(id); err != nil || got.Status != StatusClosed {
			t.Fatalf("session %s = %+v, %v; want closed", id, got.Status, err)
		}
	}
	// The engine has no caller: its own cleanup closes a run's agents too.
	restore := authority.InEngine()
	defer restore()
	e.m.ProcessRunner = nil
	// (A different run: this one's agents are closed for good.)
	late, err := e.m.StartRunAgent(coordinator, StartOptions{Run: &RunOwner{ID: "wf_20261026-094500-cccccccc", Agent: 1}, Profile: "mock", Task: "RESPOND: x", Dir: t.TempDir(), Room: e.room.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.closeRunAgent(context.Background(), late.ID); err != nil {
		t.Fatalf("engine close without a caller: %v", err)
	}
}

func TestRunAgentRunsOnTheRunsOwnMockProfileOnly(t *testing.T) {
	e := newTestEnv(t)
	own := &profile.Profile{Name: "mock-demo", Harness: profile.HarnessMock, Taste: 5, Intelligence: 5, Cost: 10}
	start := func(agent int, p *profile.Profile) error {
		_, err := e.m.StartRunAgent(e.ctx, StartOptions{Run: &RunOwner{ID: "wf_demo", Agent: agent, Profile: p}, Profile: "mock-demo", Task: "RESPOND: demo", Dir: t.TempDir(), Room: e.room.ID})
		return err
	}
	if err := start(1, own); err != nil {
		t.Fatalf("a run's own mock profile was refused: %v", err)
	}
	if got, err := e.m.WaitRunAgent(e.ctx, "wf_demo", 1, time.Minute); err != nil || !got.Done || got.Turn.Result != "demo" {
		t.Fatalf("wait = %+v, %v; want the demo agent's result", got, err)
	}
	if err := start(2, &profile.Profile{Name: "mock-demo", Harness: profile.HarnessClaudeCode}); err == nil {
		t.Fatal("a run brought a real harness profile")
	}
	if err := start(3, nil); err == nil {
		t.Fatal("an unregistered profile was accepted without the run's own definition")
	}
}

func TestCloseWaitsForAStartInFlightAndRefusesLaterStarts(t *testing.T) {
	e := newTestEnv(t)
	const run = "wf_20261026-094500-bbbbbbbb"
	coordinator := authority.WithActor(e.ctx, authority.Actor{Role: authority.Worker, Run: run})
	opts := func(agent int) StartOptions {
		return StartOptions{Run: &RunOwner{ID: run, Agent: agent}, Profile: "mock", Task: "RESPOND: x", Dir: t.TempDir(), Room: e.room.ID}
	}
	// A start whose request was canceled keeps running in the engine. The
	// close of that agent must wait for it, then close what it made.
	inStart, release := make(chan struct{}), make(chan struct{})
	e.m.runAgentStartHook = func(string) { close(inStart); <-release }
	started := make(chan error, 1)
	go func() {
		canceled, cancel := context.WithCancel(coordinator)
		cancel()
		_, err := e.m.StartRunAgent(context.WithoutCancel(canceled), opts(1))
		started <- err
	}()
	<-inStart
	closed := make(chan error, 1)
	go func() { closed <- e.m.CloseRunAgent(coordinator, run, 1) }()
	close(release)
	if err := <-started; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if got, err := e.m.store().Info(RunAgentID(run, 1)); err != nil || got.Status != StatusClosed {
		t.Fatalf("agent 1 = %+v, %v; want the session the start made, closed", got.Status, err)
	}
	// A close that found no session also stops a start that comes after it.
	e.m.runAgentStartHook = nil
	if err := e.m.CloseRunAgent(coordinator, run, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.StartRunAgent(coordinator, opts(2)); err == nil {
		t.Fatal("an agent was started after its close")
	}
	if err := e.m.CloseRunAgents(coordinator, run); err != nil {
		t.Fatal(err)
	}
	if _, err := e.m.StartRunAgent(coordinator, opts(3)); err == nil {
		t.Fatal("an agent was started after its run's agents were closed")
	}
}
