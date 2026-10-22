package harness

import (
	"testing"

	"dyna-agent/internal/profile"
	"dyna-agent/internal/workeroutput"
)

func TestUnrecognizedRecordEvidencePreservesBoundaries(t *testing.T) {
	r := &runState{}
	r.observeLine((&profile.Profile{Harness: profile.HarnessAntigravity}).EffectiveOutput(), `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call","toolCallId":"one","title":"read_file","kind":"read","status":"in_progress"}}}`)
	if r.health.RecognizedRecords != 1 || !r.health.ToolSeen {
		t.Fatalf("hidden ACTIVE event lost runtime evidence: %#v", r.health)
	}
	raw := workeroutput.DecodeLine((&profile.Profile{Harness: profile.HarnessCodex}).EffectiveOutput(), "{future}")
	if raw.Recognized() {
		t.Fatal("malformed envelope was recognized")
	}
}
