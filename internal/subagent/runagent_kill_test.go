package subagent

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/runstore/runstoretest"
	"dyna-agent/internal/store"
)

// killRecorder is a fake process kill: it records the stamps it was asked to
// end and fails while fail is set.
type killRecorder struct {
	stamps []string
	fail   bool
}

func (k *killRecorder) kill(stamp string, leader int, _ map[int]bool, _ []string) error {
	k.stamps = append(k.stamps, stamp)
	if k.fail {
		return errors.New("run processes did not exit")
	}
	return nil
}

func (k *killRecorder) count(stamp string) int {
	n := 0
	for _, s := range k.stamps {
		if s == stamp {
			n++
		}
	}
	return n
}

func startRunAgent(t *testing.T, e *testEnv, run string, agent int) Info {
	t.Helper()
	info, err := e.m.StartRunAgent(e.ctx, StartOptions{Run: &RunOwner{ID: run, Agent: agent}, Profile: "mock", Task: "RESPOND: done", Dir: t.TempDir(), Room: e.room.ID})
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestEndedRunsEndTheirProcessesWhateverTheirStatus(t *testing.T) {
	for _, status := range []string{"ok", "error"} {
		t.Run(status, func(t *testing.T) {
			e := newTestEnv(t)
			rec := &killRecorder{}
			e.m.killProcs = rec.kill
			t.Setenv("DYNA_RUN_ID", "wf_over")
			over, err := runstore.Create("over", "return null", nil)
			if err != nil {
				t.Fatal(err)
			}
			startRunAgent(t, e, over.Meta.ID, 1)
			if err := runstore.ForceStatus(over.Meta.ID, status, "ended"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.TickSessions(e.ctx); err != nil {
				t.Fatal(err)
			}
			if want := "DYNA_WORKER_RUN=" + over.Meta.ID; rec.count(want) != 1 {
				t.Fatalf("kill stamps = %v; want %s once", rec.stamps, want)
			}
		})
	}
}

func TestClosedRunAgentEndsOnlyItsOwnProcesses(t *testing.T) {
	e := newTestEnv(t)
	rec := &killRecorder{}
	e.m.killProcs = rec.kill
	const run = "wf_20261026-094500-aaaaaaaa"
	one := startRunAgent(t, e, run, 1)
	startRunAgent(t, e, run, 2)
	if got, err := e.m.WaitRunAgent(e.ctx, run, 1, time.Minute); err != nil || !got.Done {
		t.Fatalf("wait = %+v, %v", got, err)
	}
	if want := []string{"DYNA_SESSION=" + one.ID}; !slices.Equal(rec.stamps, want) {
		t.Fatalf("kill stamps = %v; want only %v", rec.stamps, want)
	}
}

func TestFailedAgentKillKeepsTheResultAndTheRunCleanupRetries(t *testing.T) {
	e := newTestEnv(t)
	rec := &killRecorder{fail: true}
	e.m.killProcs = rec.kill
	t.Setenv("DYNA_RUN_ID", "wf_kill")
	r, err := runstore.Create("kill", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run := r.Meta.ID
	startRunAgent(t, e, run, 1)
	got, err := e.m.WaitRunAgent(e.ctx, run, 1, time.Minute)
	if err != nil || !got.Done || got.Turn.Result != "done" {
		t.Fatalf("wait = %+v, %v; want the finished result despite the failed kill", got, err)
	}
	events, err := runstore.ReadEvents(run)
	if err != nil {
		t.Fatal(err)
	}
	logged := false
	for _, ev := range events {
		logged = logged || ev.T == "log" && ev.Provenance == runstore.LogProvenanceSystem && strings.Contains(ev.Msg, "could not end the processes of agent 1")
	}
	if !logged {
		t.Fatalf("run events = %+v; want a system log of the failed kill", events)
	}
	// The run ends while the kill still fails: the cleanup stays pending, then
	// succeeds on the next pass and does not run again after that.
	if err := runstore.ForceStatus(run, "ok", "ended"); err != nil {
		t.Fatal(err)
	}
	stamp := "DYNA_WORKER_RUN=" + run
	if err := e.m.CloseEndedRunAgents(e.ctx); err == nil {
		t.Fatal("run cleanup error = nil; want the failed kill reported")
	}
	rec.fail = false
	for range 2 {
		if err := e.m.CloseEndedRunAgents(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := rec.count(stamp); n != 2 {
		t.Fatalf("run kills = %d; want 2 (one failed, one retry) and none after", n)
	}
}

func TestRestartedEngineCleansAnEndedRunOnceAndMarksIt(t *testing.T) {
	e := newTestEnv(t)
	t.Setenv("DYNA_RUN_ID", "wf_restart")
	r, err := runstore.Create("restart", "return null", nil)
	if err != nil {
		t.Fatal(err)
	}
	run := r.Meta.ID
	// The run ended while no engine was watching: no open agent, no mark.
	if err := runstore.ForceStatus(run, "ok", "ended"); err != nil {
		t.Fatal(err)
	}
	rec := &killRecorder{}
	fresh := &Manager{Store: e.m.Store, killProcs: rec.kill} // a Manager with no memory of the run
	for range 2 {
		if err := fresh.CloseEndedRunAgents(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := rec.count("DYNA_WORKER_RUN=" + run); n != 1 {
		t.Fatalf("run kills = %d; want exactly one", n)
	}
	if meta, err := runstore.ReadMeta(run); err != nil || meta.ProcessesEnded == nil {
		t.Fatalf("run meta = %+v, %v; want ProcessesEnded set", meta, err)
	}
}

func TestOldUnmarkedEndedRunIsStillCleaned(t *testing.T) {
	e := newTestEnv(t)
	old := time.Now().Add(-72 * time.Hour)
	runstoretest.Write(t, store.DataDir(), runstore.Meta{ID: "wf_old", Status: "ok", StartedAt: old, EndedAt: old})
	rec := &killRecorder{}
	e.m.killProcs = rec.kill
	if err := e.m.CloseEndedRunAgents(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := rec.count("DYNA_WORKER_RUN=wf_old"); n != 1 {
		t.Fatalf("run kills = %d; want 1 for a run that ended three days ago without a mark", n)
	}
}

func TestDeadCoordinatorRunIsRetriedAfterAFailedCleanup(t *testing.T) {
	e := newTestEnv(t)
	// The coordinator is gone and the run still reads running; its agents are closed.
	runstoretest.Write(t, store.DataDir(), runstore.Meta{ID: "wf_dead", Status: "running", Pid: 0, StartedAt: time.Now()})
	rec := &killRecorder{fail: true}
	e.m.killProcs = rec.kill
	if err := e.m.CloseEndedRunAgents(e.ctx); err == nil {
		t.Fatal("first pass error = nil; want the failed kill")
	}
	rec.fail = false
	if err := e.m.CloseEndedRunAgents(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := rec.count("DYNA_WORKER_RUN=wf_dead"); n != 2 {
		t.Fatalf("run kills = %d; want a failed pass and a successful retry with no restart", n)
	}
}
