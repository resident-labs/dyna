package subagent

import (
	"dyna-agent/internal/callspec"
	"dyna-agent/internal/harness"
)

// nativeExecution uses the same ordered outbox as the turn's other writes.
// In-process managers retain their explicit engine-local execution path.
func (w *runnerWrites) nativeExecution(role string) harness.ExecutionOptions {
	exec := sessionExecution(role)
	if w.out != nil {
		exec.Execution = callspec.ExecutionRef{Session: w.id, Turn: w.n}
		exec.Outbox = w.out
	}
	return exec
}
