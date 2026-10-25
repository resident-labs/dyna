package workeroutput

import (
	"testing"
)

func TestDecodedRecordSharesSessionAndUsage(t *testing.T) {
	decl := &Declaration{Dialect: DialectClaudeStreamJSON}
	n := NewUsageNormalizer("", nil)
	start := DecodeLine(decl, `{"type":"system","subtype":"init","session_id":"session-1"}`)
	if start.SessionID != "session-1" {
		t.Fatalf("session = %q", start.SessionID)
	}
	n.ObserveRecord(start)
	end := DecodeLine(decl, `{"type":"result","usage":{"input_tokens":12,"cache_read_input_tokens":2,"cache_creation_input_tokens":1,"output_tokens":4}}`)
	usage, ok := n.ObserveRecord(end)
	if !ok || usage.InputTokens != 15 || usage.CachedTokens != 2 || usage.OutputTokens != 4 {
		t.Fatalf("usage = %#v, %v", usage, ok)
	}
}
