package harness

import (
	"testing"

	"dyna-agent/internal/workeroutput"
)

func TestTurnStartPreservesNoProgressLimit(t *testing.T) {
	state := &runState{}
	turn := workeroutput.DecodeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectPiJSON}, `{"type":"agent_start"}`)
	state.observeRecord(turn)
	if state.hasRealProgress() || state.hasActivity() {
		t.Fatal("turn start counted as output")
	}
	step := workeroutput.DecodeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectOpenCodeJSON},
		`{"type":"step_start","part":{"type":"step-start"}}`)
	state.observeRecord(step)
	state.observeRecord(turn)
	state.observeRecord(step)
	if !state.stepsStalled(2) || state.hasRealProgress() {
		t.Fatal("turn start changed the empty-step limit")
	}
}
