package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/resident-labs/dyna/contract"
)

// The engine's workflow settings turn a feature off at a step's start, so each gated call must say which feature it is.
func TestEachGatedCallNamesItsFeatureAtItsStart(t *testing.T) {
	h, _ := newTreeHarness(t)
	if err := os.WriteFile(filepath.Join(h.workdir, "child.js"), []byte("export const meta = { name: 'child', profiles: ['fast'] }\nreturn 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.result(crewMeta + `
		const results = [await agent('one', { label: 'tree', worktree: true })]
		await merge(results, { label: 'merge it' })
		await run('true', { label: 'command' })
		await agent('plain', { label: 'plain' })
		await post('parent', 'hello')
		await workflow('child.js')
		log('line')`)
	want := map[string]contract.WorkflowFeature{
		"tree": contract.WorkflowFeatureWorktree, "merge it": contract.WorkflowFeatureMerge, "command": contract.WorkflowFeatureRun,
		"plain": "", "post: hello": contract.WorkflowFeaturePost, "workflow: child.js": contract.WorkflowFeatureWorkflow, "log: line": "",
	}
	for name, feature := range want {
		if got, ok := h.journal.features[name]; !ok || got != feature {
			t.Errorf("step %q named feature %q (started: %v), want %q", name, got, ok, feature)
		}
	}
}

// A start the engine refuses throws in the script with the engine's words, and the step's work never runs.
func TestAFeatureTheEngineRefusesThrowsAndDoesNothing(t *testing.T) {
	h := newCrewHarness(t)
	h.journal.refuse = map[contract.WorkflowFeature]error{contract.WorkflowFeatureRun: errors.New("run is turned off (workflow setting run)")}
	got := h.result(crewMeta + `
		const refused = await run('touch ran').then(() => 'ran', (error) => error.message)
		const allowed = await agent('still works')
		return { refused, allowed }`).(map[string]any)
	if got["refused"] != "run is turned off (workflow setting run)" || got["allowed"] == nil {
		t.Fatalf("result %v", got)
	}
	if _, err := os.Stat(filepath.Join(h.workdir, "ran")); !os.IsNotExist(err) {
		t.Fatal("the refused command ran")
	}
}

// The room's default timeout bounds a step whose script sets none, and a step's own timeout wins over it.
func TestTheRoomsTimeoutBoundsStepsThatSetNone(t *testing.T) {
	h := newCrewHarness(t)
	h.timeout = 1
	started := time.Now()
	got := h.result(crewMeta + `
		const slow = await run('sleep 5', { check: false }).then(() => 'finished', (error) => 'timed out')
		const own = await run('sleep 1.5; echo done', { timeout: 10 })
		return { slow, own: own.stdout.trim() }`).(map[string]any)
	if got["slow"] != "timed out" || got["own"] != "done" {
		t.Fatalf("result %v", got)
	}
	if took := time.Since(started); took > 4*time.Second {
		t.Fatalf("the default timeout of 1s let a command run %s", took)
	}
}
