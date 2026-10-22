package workeroutput

import (
	"testing"
)

func TestDecodedRecordSharesSessionAndUsage(t *testing.T) {
	decl := &Declaration{Dialect: DialectCodexJSONL}
	n := NewUsageNormalizer("", nil)
	start := DecodeLine(decl, `{"type":"thread.started","thread_id":"thread-1"}`)
	if start.SessionID != "thread-1" {
		t.Fatalf("session = %q", start.SessionID)
	}
	n.ObserveRecord(start)
	end := DecodeLine(decl, `{"type":"turn.completed","usage":{"input_tokens":12,"cached_input_tokens":2,"output_tokens":4}}`)
	usage, ok := n.ObserveRecord(end)
	if !ok || usage.InputTokens != 12 || usage.CachedTokens != 2 || usage.OutputTokens != 4 {
		t.Fatalf("usage = %#v, %v", usage, ok)
	}
}

func TestTextlessReasoningStartStillGenerates(t *testing.T) {
	decl := &Declaration{Dialect: DialectCodexJSONL}
	// A reasoning item can start with no text. It is still model work, so it
	// must count as generation even though display filtering drops its events.
	started := DecodeLine(decl, `{"type":"item.started","item":{"type":"reasoning","id":"r1","text":""}}`)
	if !started.Generates() {
		t.Fatalf("textless reasoning start does not generate: %#v", started)
	}
	// Pure session plumbing is not generation.
	thread := DecodeLine(decl, `{"type":"thread.started","thread_id":"thread-1"}`)
	if thread.Generates() {
		t.Fatalf("session plumbing counts as generation: %#v", thread)
	}
}

func TestCodexTurnStartGenerates(t *testing.T) {
	decl := &Declaration{Dialect: DialectCodexJSONL}
	turn := DecodeLine(decl, `{"type":"turn.started"}`)
	if !turn.Generates() {
		t.Fatalf("accepted turn does not generate: %#v", turn)
	}
}
