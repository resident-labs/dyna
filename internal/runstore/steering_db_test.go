package runstore

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/plan/ancestry"
	"dyna-agent/internal/store"
)

func steeringTexts(t *testing.T, runID string, offset int64) ([]string, int64) {
	t.Helper()
	messages, next, err := ReadAgentSteeringFrom(runID, 1, offset, false)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range messages {
		out = append(out, m.Message)
	}
	return out, next
}

// The worker's steering state lives in dyna.db (rule r10), and activating
// again keeps the queue: reads start after the messages from before.
func TestSteeringStateLivesInDynaDB(t *testing.T) {
	run := createSteeringTestRun(t, "wf_steering-db")
	dir, _ := agentSteeringDir(run.Meta.ID, 1)
	if err := ActivateAgentSteering(run.Meta.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := SubmitAgentSteering(run.Meta.ID, 1, "first"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "steering-state.json")); err != nil {
		t.Fatal(err)
	}
	if got, _ := steeringTexts(t, run.Meta.ID, 0); strings.Join(got, ",") != "first" {
		t.Fatalf("messages without steering-state.json = %v; want first", got)
	}
	if err := ActivateAgentSteering(run.Meta.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := SubmitAgentSteering(run.Meta.ID, 1, "second"); err != nil {
		t.Fatal(err)
	}
	if got, _ := steeringTexts(t, run.Meta.ID, 0); strings.Join(got, ",") != "second" {
		t.Fatalf("messages after activating again = %v; want only second", got)
	}
}

// An earlier release's worker activates by truncating the queue and writing
// steering-state.json with no queueStart. The import resets the start, so
// the reader does not begin inside the new, shorter queue.
func TestOldWorkerActivationResetsQueueStart(t *testing.T) {
	run := createSteeringTestRun(t, "wf_steering-old")
	dir, _ := agentSteeringDir(run.Meta.ID, 1)
	if err := ActivateAgentSteering(run.Meta.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := SubmitAgentSteering(run.Meta.ID, 1, "before"); err != nil {
		t.Fatal(err)
	}
	if err := ActivateAgentSteering(run.Meta.ID, 1); err != nil {
		t.Fatal(err)
	}
	// The old worker's queue is longer than the start the row holds.
	var lines []byte
	for _, m := range []string{"old one", "old two"} {
		b, _ := json.Marshal(SteeringMessage{Message: m})
		lines = append(append(lines, b...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, "steering.jsonl"), lines, 0o600); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(steeringState{RunID: run.Meta.ID, AgentID: 1, Pid: run.Meta.Pid, StartedAt: run.Meta.StartedAt.UnixNano(), Active: true})
	if err := os.WriteFile(filepath.Join(dir, "steering-state.json"), state, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := steeringTexts(t, run.Meta.ID, 0); strings.Join(got, ",") != "old one,old two" {
		t.Fatalf("messages after an old worker's activation = %v; want both", got)
	}
}

// A read never starts past the queue's end: a shorter queue was truncated,
// so the read starts from 0.
func TestSteeringReadPastTheEndStartsAtZero(t *testing.T) {
	run := createSteeringTestRun(t, "wf_steering-short")
	dir, _ := agentSteeringDir(run.Meta.ID, 1)
	if err := ActivateAgentSteering(run.Meta.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := SubmitAgentSteering(run.Meta.ID, 1, "a long first message"); err != nil {
		t.Fatal(err)
	}
	_, next := steeringTexts(t, run.Meta.ID, 0)
	b, _ := json.Marshal(SteeringMessage{Message: "x"})
	if err := os.WriteFile(filepath.Join(dir, "steering.jsonl"), append(b, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := steeringTexts(t, run.Meta.ID, next); strings.Join(got, ",") != "x" {
		t.Fatalf("messages after a truncation = %v; want x", got)
	}
}

// Removing a run deletes, in one write, its meta, steering and ancestry
// rows and the stamps of all their copies.
func TestRemoveDeletesEveryRowOfTheRun(t *testing.T) {
	run := createSteeringTestRun(t, "wf_steering-rm")
	if err := ActivateAgentSteering(run.Meta.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.Dir, "plan.json"), []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (ancestry.Store{DataDir: store.DataDir()}).Stamp(context.Background(), run.Dir, "wf_parent"); err != nil {
		t.Fatal(err)
	}
	run.Finish("ok", "null", nil)
	db, err := runsDB()
	if err != nil {
		t.Fatal(err)
	}
	id := run.Meta.ID
	count := func() map[string]int {
		got := map[string]int{}
		for name, q := range map[string]string{
			"runs":     "SELECT count(*) FROM runs WHERE id=?",
			"steering": "SELECT count(*) FROM run_agent_steering WHERE run=?",
			"ancestry": "SELECT count(*) FROM plan_ancestry WHERE plan=?",
			"stamps":   "SELECT count(*) FROM state_mirrors WHERE key LIKE 'runs/' || ?1 || '/%' OR key LIKE 'plans/' || ?1 || '%'",
		} {
			var n int
			if err := db.QueryRow(q, id).Scan(&n); err != nil {
				t.Fatal(err)
			}
			got[name] = n
		}
		return got
	}
	before := count()
	if before["runs"] != 1 || before["steering"] != 1 || before["ancestry"] != 1 || before["stamps"] < 3 {
		t.Fatalf("before Remove = %v; want a row of each kind and the meta, steering and plan stamps", before)
	}
	if err := Remove(id); err != nil {
		t.Fatal(err)
	}
	if after := count(); after["runs"]+after["steering"]+after["ancestry"]+after["stamps"] != 0 {
		t.Fatalf("after Remove = %v; want nothing left", after)
	}
}
