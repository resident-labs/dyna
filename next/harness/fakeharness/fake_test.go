package fakeharness

import (
	"context"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestScriptReportsToolCalls(t *testing.T) {
	var seen ToolCall
	h := &Harness{Turns: []Turn{{Tools: []ToolCall{{Name: "shell", Input: "pwd", Output: "/box"}}}}, OnTool: func(call ToolCall) { seen = call }}
	if _, err := h.Start(context.Background(), "turn_a", []contract.TurnInput{{Delivery: 1, Text: "run"}}); err != nil {
		t.Fatal(err)
	}
	if seen.Name != "shell" || seen.Input != "pwd" || seen.Output != "/box" {
		t.Fatalf("tool call: %+v", seen)
	}
}
