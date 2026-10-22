package subagent

import (
	"testing"

	"dyna-agent/internal/callspec"
	"dyna-agent/internal/engineclient"
)

func TestRunnerNativeAdmissionOptions(t *testing.T) {
	// No sender is needed: this checks that the runner preserves the actual
	// outbox pointer, including its existing prefix and ordinary sequence.
	out := new(engineclient.Outbox)
	writes := &runnerWrites{id: "session", n: 7, out: out}
	got := writes.nativeExecution("")
	if got.Outbox != out || got.Execution != (callspec.ExecutionRef{Session: "session", Turn: 7}) || !got.DisableNativeSubagents {
		t.Fatalf("detached execution: %+v", got)
	}
	writes.out = nil
	got = writes.nativeExecution("")
	if got.Outbox != nil || got.Execution != (callspec.ExecutionRef{}) || !got.DisableNativeSubagents {
		t.Fatalf("engine-local embedding: %+v", got)
	}
}
