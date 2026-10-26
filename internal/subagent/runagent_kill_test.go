package subagent

import (
	"slices"
	"testing"
	"time"

	"dyna-agent/internal/runstore"
)

// killRecorder is a fake process kill: it records the stamps it was asked to end.
type killRecorder struct{ stamps []string }

func (k *killRecorder) kill(stamp string, leader int, _ map[int]bool, _ []string) error {
	k.stamps = append(k.stamps, stamp)
	return nil
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
			if _, err := e.m.StartRunAgent(e.ctx, StartOptions{Run: &RunOwner{ID: over.Meta.ID, Agent: 1}, Profile: "mock", Task: "RESPOND: done", Dir: t.TempDir(), Room: e.room.ID}); err != nil {
				t.Fatal(err)
			}
			if err := runstore.ForceStatus(over.Meta.ID, status, "ended"); err != nil {
				t.Fatal(err)
			}
			if _, err := e.m.TickSessions(e.ctx); err != nil {
				t.Fatal(err)
			}
			if want := "DYNA_WORKER_RUN=" + over.Meta.ID; !slices.Contains(rec.stamps, want) {
				t.Fatalf("kill stamps = %v; want %s", rec.stamps, want)
			}
		})
	}
}

func TestClosedRunAgentEndsOnlyItsOwnProcesses(t *testing.T) {
	e := newTestEnv(t)
	rec := &killRecorder{}
	e.m.killProcs = rec.kill
	const run = "wf_20261026-094500-aaaaaaaa"
	start := func(agent int) Info {
		info, err := e.m.StartRunAgent(e.ctx, StartOptions{Run: &RunOwner{ID: run, Agent: agent}, Profile: "mock", Task: "RESPOND: done", Dir: t.TempDir(), Room: e.room.ID})
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	one := start(1)
	start(2)
	if got, err := e.m.WaitRunAgent(e.ctx, run, 1, time.Minute); err != nil || !got.Done {
		t.Fatalf("wait = %+v, %v", got, err)
	}
	if want := []string{"DYNA_SESSION=" + one.ID}; !slices.Equal(rec.stamps, want) {
		t.Fatalf("kill stamps = %v; want only %v", rec.stamps, want)
	}
}
