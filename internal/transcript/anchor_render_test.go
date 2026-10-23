package transcript

import (
	"testing"

	"dyna-agent/internal/subagent"
	"dyna-agent/internal/workeroutput"
)

func TestAnchorSplitsEqualTimestampAssistantOutput(t *testing.T) {
	anchor := int64(20)
	for _, tc := range []struct{ name, state, before, after string }{
		{"messages", "", "before", "after"},
		{"deltas", "delta", "before", "after"},
		{"repeated", "", "This answer is long enough to trigger reply deduplication.", "This answer is long enough to trigger reply deduplication."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := subagent.Turn{Prompt: "task", Steering: []subagent.Steer{{Message: "check", TS: 1, Delivered: true, SourceOffset: &anchor}}}
			events := []workeroutput.Event{
				{V: workeroutput.SchemaVersion, Kind: "message", Role: "assistant", ID: "reply", State: tc.state, Text: tc.before, TS: 1, SourceOffset: 10},
				{V: workeroutput.SchemaVersion, Kind: "message", Role: "assistant", ID: "reply", State: tc.state, Text: tc.after, TS: 1, SourceOffset: 20},
			}
			items := SessionTurn(turn, events, false).Items
			if len(items) != 4 || items[1].Text != tc.before || items[2].Kind != Steering || items[3].Text != tc.after {
				t.Fatalf("anchor did not split assistant output: %+v", items)
			}
		})
	}
}
