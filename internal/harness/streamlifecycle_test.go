package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

type streamTestWriter struct{ bytes.Buffer }

func (*streamTestWriter) Close() error { return nil }

func replayLine(text string) string {
	var r map[string]any
	_ = json.Unmarshal(workeroutput.ClaudeUserMessage(text), &r)
	r["isReplay"] = true
	b, _ := json.Marshal(r)
	return string(b)
}

func TestClaudeReplayOnlyAcknowledgesHeadContent(t *testing.T) {
	s := newStreamInput(&streamTestWriter{})
	first := runstore.SteeringMessage{ID: "first", Message: "one"}
	second := runstore.SteeringMessage{ID: "second", Message: "two"}
	s.write(streamItemSteering, "one", []runstore.SteeringMessage{first}, 0)
	s.write(streamItemSteering, "two", []runstore.SteeringMessage{second}, 0)
	for _, line := range []string{
		`{"type":"user","isReplay":true,"origin":{"kind":"task-notification"},"message":{"content":"one"}}`,
		`{"type":"user","isReplay":true,"message":{"content":"unknown"}}`,
		replayLine("two"),
	} {
		s.observe(line, 0, workeroutput.Usage{})
		for _, n := range s.drain() {
			if n.consumed != nil {
				t.Fatalf("false acknowledgment of %s", line)
			}
		}
	}
	s.observe(`{"type":"user","isReplay":true,"message":{"content":"one"}}`, 0, workeroutput.Usage{})
	ns := s.drain()
	if len(ns) != 1 || ns[0].consumed == nil || ns[0].consumed.text != "one" {
		t.Fatalf("canonical string replay not acknowledged: %#v", ns)
	}
	pending, _ := s.pending()
	if len(pending) != 1 || pending[0].ID != "second" {
		t.Fatalf("queue corrupted: %#v", pending)
	}
	s.observe(replayLine("two"), 0, workeroutput.Usage{})
	if !s.idle() {
		t.Fatal("matching head replay was not consumed")
	}
}

func TestClaudeTerminalStateIndependentOfNoticeBatching(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(fmt.Sprintf("split=%v", split), func(t *testing.T) {
			s := newStreamInput(&streamTestWriter{})
			s.write(streamItemSteering, "pending", nil, 0)
			s.observe(`{"type":"result","result":"before input"}`, 1, workeroutput.Usage{})
			terminal := s.lastTerminal()
			if split {
				s.drain()
			}
			s.observe(replayLine("pending"), 2, workeroutput.Usage{})
			s.drain()
			s.drain() // stale notification is harmless
			if !s.awaitingResult() || s.canSeal(terminal) {
				t.Fatal("result before acknowledgment incorrectly became terminal")
			}
			s.observe(`{"type":"result","result":"covers input"}`, 3, workeroutput.Usage{})
			terminal = s.lastTerminal()
			s.drain()
			s.drain()
			if s.awaitingResult() || !s.canSeal(terminal) {
				t.Fatal("covering result did not become terminal")
			}
			s.write(streamItemSteering, "new input from final read", nil, 0)
			s.drain()
			if !s.awaitingResult() || s.canSeal(terminal) {
				t.Fatal("final-read input did not invalidate prior terminal")
			}
		})
	}
}

func TestClaudeAckStallIsSeparateFromResultCoverage(t *testing.T) {
	s := newStreamInput(&streamTestWriter{})
	s.write(streamItemSteering, "one", nil, 0)
	s.observe(`{"type":"result","result":"uncovered"}`, 1, workeroutput.Usage{})
	if !s.awaitingAcknowledgment() {
		t.Fatal("unacknowledged terminal did not arm stall bound")
	}
	s.observe(replayLine("one"), 2, workeroutput.Usage{})
	s.write(streamItemSteering, "two", nil, 0)
	s.observe(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"long"}]}}`, 3, workeroutput.Usage{})
	s.drain()
	s.drain()
	if s.awaitingAcknowledgment() || !s.awaitingResult() {
		t.Fatal("active generation confused with acknowledged result coverage")
	}
	// Its tool can remain silent arbitrarily long; only another terminal with
	// still-unacknowledged input can rearm the input-only stall recovery.
	s.observe(`{"type":"result","result":"still uncovered"}`, 4, workeroutput.Usage{})
	if !s.awaitingAcknowledgment() {
		t.Fatal("next uncovered result did not rearm acknowledgment bound")
	}
}

func TestClaudeReplayRequiresActualTextForEmptyPayload(t *testing.T) {
	for _, content := range []string{`null`, `[{"type":"text","text":null}]`, `[{"type":"text"}]`, `[]`, `0`} {
		line := `{"type":"user","isReplay":true,"message":{"content":` + content + `}}`
		if texts, ok := workeroutput.ClaudeReplayTexts(line); ok {
			t.Fatalf("non-text content acknowledged %q: %s", texts, content)
		}
	}
	for _, content := range []string{`""`, `[{"type":"text","text":""}]`} {
		line := `{"type":"user","isReplay":true,"message":{"content":` + content + `}}`
		if texts, ok := workeroutput.ClaudeReplayTexts(line); !ok || len(texts) != 1 || texts[0] != "" {
			t.Fatalf("actual empty text was not acknowledged: %s", content)
		}
	}
}
