package workflow

import (
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
)

func TestPublicLogEmitsWorkflowProvenance(t *testing.T) {
	recorder := &workflowEventRecorder{}
	if _, err := executeWorkflowTest(t, `log("hello from script"); return true;`, Options{
		Store: mockWorkflowStore(0, 0), WorkDir: t.TempDir(), OnEvent: recorder.append,
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	event := mustFindLog(t, recorder.snapshot(), "hello from script")
	if event.Provenance != runstore.LogProvenanceWorkflow || !event.IsWorkflowLog() {
		t.Fatalf("public log() event = %#v, want provenance=workflow", event)
	}
}

func TestParallelAndPipelineDiagnosticsAreSystemProvenance(t *testing.T) {
	recorder := &workflowEventRecorder{}
	script := `
const parallelResult = await parallel([() => { throw new Error("boom"); }]);
const pipelineResult = await pipeline([1], () => { throw new Error("drop"); });
return {parallelResult, pipelineResult};
`
	if _, err := executeWorkflowTest(t, script, Options{
		Store: mockWorkflowStore(0, 0), WorkDir: t.TempDir(), OnEvent: recorder.append,
	}); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	events := recorder.snapshot()
	parallelLog := mustFindLogPrefix(t, events, "parallel: task failed:")
	if parallelLog.Provenance != runstore.LogProvenanceSystem || parallelLog.IsWorkflowLog() {
		t.Fatalf("parallel diagnostic = %#v, want provenance=system", parallelLog)
	}
	pipelineLog := mustFindLogPrefix(t, events, "pipeline: item 0 dropped:")
	if pipelineLog.Provenance != runstore.LogProvenanceSystem || pipelineLog.IsWorkflowLog() {
		t.Fatalf("pipeline diagnostic = %#v, want provenance=system", pipelineLog)
	}
}

func mustFindLog(t *testing.T, events []runstore.Event, msg string) runstore.Event {
	t.Helper()
	for _, event := range events {
		if event.T == "log" && event.Msg == msg {
			return event
		}
	}
	t.Fatalf("missing t=log %q in %#v", msg, events)
	return runstore.Event{}
}

func mustFindLogPrefix(t *testing.T, events []runstore.Event, prefix string) runstore.Event {
	t.Helper()
	for _, event := range events {
		if event.T == "log" && strings.HasPrefix(event.Msg, prefix) {
			return event
		}
	}
	t.Fatalf("missing t=log prefix %q in %#v", prefix, events)
	return runstore.Event{}
}
