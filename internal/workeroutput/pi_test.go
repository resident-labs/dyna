package workeroutput

import (
	"reflect"
	"strings"
	"testing"
)

func TestPiCodemodeNestedCalls(t *testing.T) {
	decl := &Declaration{Dialect: DialectPiJSON}
	lines := []string{
		`{"type":"tool_execution_start","toolCallId":"outer","toolName":"codemode","args":{"code":"run()"}}`,
		`{"type":"tool_execution_start","toolCallId":"outer/1","toolName":"read","args":{"path":"a.go"}}`,
		`{"type":"tool_execution_start","toolCallId":"outer/2","toolName":"mcp__dyna__shell","args":{"command":"pwd","description":"Print the path"}}`,
		`{"type":"tool_execution_update","toolCallId":"outer","toolName":"codemode","partialResult":{"details":{"calls":[{"id":"outer/?","name":"read","status":"running"},{"id":"outer/?","name":"mcp__dyna__shell","status":"running"}]}}}`,
		`{"type":"tool_execution_end","toolCallId":"outer/1","toolName":"read","result":{"content":[{"type":"text","text":"contents"}]}}`,
		`{"type":"tool_execution_end","toolCallId":"outer/2","toolName":"mcp__dyna__shell","result":{"content":[{"type":"text","text":"done"}]}}`,
		`{"type":"tool_execution_end","toolCallId":"outer","toolName":"codemode","result":{"details":{"calls":[{"id":"outer/1","name":"read","status":"ok"},{"id":"outer/2","name":"mcp__dyna__shell","status":"ok"}]}}}`,
	}
	var events []Event
	for _, line := range lines {
		events = append(events, DecodeLine(decl, line).Events...)
	}
	paired, _ := PairTools(events)
	if len(paired) != 3 || paired[1].Name != "read" || paired[1].Output != "contents" ||
		paired[2].Name != "mcp__dyna__shell" || paired[2].Input != `{"command":"pwd","description":"Print the path"}` || paired[2].Output != "done" {
		t.Fatalf("paired events = %+v", paired)
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
