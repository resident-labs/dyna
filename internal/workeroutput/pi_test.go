package workeroutput

import (
	"reflect"
	"strings"
	"testing"
)

// Recorded fork output: codemode updates carry script output, not nested
// call snapshots. The nested calls already have their own full events.
func TestPiCodemodeUpdateStreamsScriptOutput(t *testing.T) {
	decl := &Declaration{Dialect: DialectPiJSON}
	line := `{"type":"tool_execution_update","toolCallId":"outer","toolName":"codemode","args":{"code":"console.log('reading')"},"partialResult":{"content":[{"type":"text","text":"Script running\nOutput:\n"},{"type":"text","text":"<console_output>\nreading\n</console_output>"}]}}`
	events := DecodeLine(decl, line).Events
	paired, _ := PairTools(events)
	if len(paired) != 1 {
		t.Fatalf("paired events = %+v, want one codemode row and no nested rows", paired)
	}
	got := paired[0]
	want := "Script running\nOutput:\n\n<console_output>\nreading\n</console_output>"
	if got.Kind != "tool" || got.ID != "outer" || got.Name != "codemode" || got.State != "updated" || got.Output != want {
		t.Fatalf("codemode row = %+v, want updated script output %q", got, want)
	}
}

// Pi lists a thought before the reply it led to; the events keep that order.
func TestPiReasoningKeepsBlockOrder(t *testing.T) {
	line := `{"type":"message_end","message":{"role":"assistant","content":[` +
		`{"type":"thinking","thinking":"Check the file first."},{"type":"text","text":"It is"},{"type":"text","text":"fine."},` +
		`{"type":"thinking","thinking":"","thinkingSignature":"x","redacted":true}],"stopReason":"stop"}}`
	got := DecodeLine(&Declaration{Dialect: DialectPiJSON}, line).Events
	want := []Event{
		{V: SchemaVersion, Kind: "reasoning", State: "completed", Text: "Check the file first."},
		{V: SchemaVersion, Kind: "message", Role: "assistant", Text: "It is\nfine."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %+v", got)
	}
}

// A Dyna shell result must reach the transcript as its text, which starts
// "Job sh_...", and not as the JSON envelope: only that lead carries the
// exact job ID. The records are real Pi output from the PR #1165 live QA.
func TestPiToolResultsKeepTheirText(t *testing.T) {
	decl := &Declaration{Dialect: DialectPiJSON}
	line := `{"type":"tool_execution_end","toolCallId":"call_ac6afd12dcd643a385e146db","toolName":"mcp__dyna__shell","result":{"content":[{"type":"text","text":"Job sh_20261021-080938-d68f7c94. Exit code 0.\ndyna dev+df64d0752c9a\n"}],"details":{"server":"dyna","tool":"shell"},"structuredContent":{"content":[{"text":"Job sh_20261021-080938-d68f7c94. Exit code 0.\ndyna dev+df64d0752c9a\n","type":"text"}],"isError":false}},"isError":false,"durationMs":212}`
	events := DecodeLine(decl, line).Events
	if len(events) != 1 || events[0].Kind != "tool" {
		t.Fatalf("events = %+v", events)
	}
	if want := "Job sh_20261021-080938-d68f7c94. Exit code 0.\ndyna dev+df64d0752c9a\n"; events[0].Output != want {
		t.Fatalf("output = %q, want the result text", events[0].Output)
	}
	// A result without text blocks keeps its compact JSON, and a result left
	// out entirely stays empty.
	only := `{"type":"tool_execution_end","toolCallId":"c2","toolName":"mcp__other__get","result":{"content":[{"type":"image","data":""}]}}`
	if got := DecodeLine(decl, only).Events[0].Output; !strings.HasPrefix(got, `{"content":`) {
		t.Fatalf("fallback output = %q, want the compact JSON envelope", got)
	}
	none := `{"type":"tool_execution_end","toolCallId":"c3","toolName":"mcp__other__get"}`
	if got := DecodeLine(decl, none).Events[0].Output; got != "" {
		t.Fatalf("absent result output = %q, want empty", got)
	}
	// A streaming partial carries the same content envelope as a final result
	// (shipped docs/json.md), so its text shows while the call runs.
	partial := `{"type":"tool_execution_update","toolCallId":"call_ac6afd12dcd643a385e146db","toolName":"mcp__dyna__shell","args":{"command":"go test ./...","description":"Run the tests"},"partialResult":{"content":[{"type":"text","text":"partial output"}],"details":{}}}`
	update := DecodeLine(decl, partial).Events[0]
	if update.State != "updated" || update.Output != "partial output" {
		t.Fatalf("partial = %s %q, want updated text", update.State, update.Output)
	}
}
