package harness

import (
	"testing"

	"dyna-agent/internal/workeroutput"
)

// The first model call of an invocation is kept, and later calls do not
// replace it. A dialect that reports only turn totals leaves it nil.
func TestFirstCallUsageKeepsTheFirstModelCall(t *testing.T) {
	decl := &workeroutput.Declaration{Dialect: workeroutput.DialectPiJSON}
	state := &runState{}
	state.observeRecord(workeroutput.DecodeLine(decl, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"a"}],"usage":{"input":100,"output":1,"cacheRead":5000,"cacheWrite":0}}}`))
	state.observeRecord(workeroutput.DecodeLine(decl, `{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"b"}],"usage":{"input":7,"output":1,"cacheRead":9000,"cacheWrite":0}}}`))

	first := state.firstCallUsage()
	if first == nil || first.Input == nil || *first.Input != 100 || first.Context == nil || *first.Context != 5100 {
		t.Fatalf("first call = %+v, want input 100 and context 5100", first)
	}

	silent := &runState{}
	silent.observeRecord(workeroutput.DecodeLine(&workeroutput.Declaration{Dialect: workeroutput.DialectClaudeStreamJSON}, `{"type":"result","usage":{"input_tokens":10,"output_tokens":5}}`))
	if got := silent.firstCallUsage(); got != nil {
		t.Fatalf("result-only first call = %+v, want unknown nil", got)
	}
}
