package harness

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"dyna-agent/internal/runstore"
	"dyna-agent/internal/workeroutput"
)

type discardCloser struct{ io.Writer }

func (discardCloser) Close() error { return nil }

func mergedReplayLine(t *testing.T, texts ...string) string {
	t.Helper()
	blocks := []map[string]string{}
	for _, text := range texts {
		blocks = append(blocks, map[string]string{"type": "text", "text": text})
	}
	b, err := json.Marshal(map[string]any{"type": "user", "isReplay": true, "message": map[string]any{"role": "user", "content": blocks}})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Claude merges messages it takes in at one tool boundary into one replay with
// a text block per message (seen in a real session). Each must count as
// acknowledged, or the turn resends every later post as a numbered batch.
func TestMergedReplayAcknowledgesEachWrittenMessage(t *testing.T) {
	s := newStreamInput(discardCloser{io.Discard})
	first := []runstore.SteeringMessage{{ID: "msg_a", Message: frame("msg_a")}}
	second := []runstore.SteeringMessage{{ID: "msg_b", Message: frame("msg_b")}}
	third := []runstore.SteeringMessage{{ID: "msg_c", Message: frame("msg_c")}}
	for _, m := range [][]runstore.SteeringMessage{first, second, third} {
		if !s.write(streamItemSteering, steeringPrompt(m), m, 1) {
			t.Fatal("write failed")
		}
	}
	// Out of order is no acknowledgment.
	s.observe(mergedReplayLine(t, steeringPrompt(second)), 0, workeroutput.Usage{})
	if got, _ := s.pending(); len(got) != 3 {
		t.Fatalf("out-of-order replay acknowledged: %+v", got)
	}
	s.observe(mergedReplayLine(t, steeringPrompt(first), steeringPrompt(second)), 0, workeroutput.Usage{})
	var consumed []string
	for _, n := range s.drain() {
		if n.consumed != nil {
			consumed = append(consumed, n.consumed.messages[0].ID)
		}
	}
	if len(consumed) != 2 || consumed[0] != "msg_a" || consumed[1] != "msg_b" {
		t.Fatalf("consumed = %v", consumed)
	}
	s.observe(mergedReplayLine(t, steeringPrompt(third)), 0, workeroutput.Usage{})
	if got, _ := s.pending(); len(got) != 0 {
		t.Fatalf("still pending after merged replay: %+v", got)
	}
}

// In real merged replays (engine-lead turn 308) Claude ends every block but
// the last with a newline that Dyna never wrote. The match must not depend on
// it, or every later post is resent as a numbered batch.
func TestMergedReplayWithTrailingNewlinesAcknowledges(t *testing.T) {
	s := newStreamInput(discardCloser{io.Discard})
	first := []runstore.SteeringMessage{{ID: "msg_a", Message: frame("msg_a")}}
	second := []runstore.SteeringMessage{{ID: "msg_b", Message: frame("msg_b")}}
	for _, m := range [][]runstore.SteeringMessage{first, second} {
		if !s.write(streamItemSteering, steeringPrompt(m), m, 1) {
			t.Fatal("write failed")
		}
	}
	s.observe(mergedReplayLine(t, steeringPrompt(first)+"\n", steeringPrompt(second)), 0, workeroutput.Usage{})
	if got, _ := s.pending(); len(got) != 0 {
		t.Fatalf("merged replay with a separator newline left %d pending", len(got))
	}
}

func frame(id string) string {
	return "[dyna message " + id + " from dyna-agent]\nTo: dyna-agent:x (you)\n\nbody"
}

// Delivery confirmation does not depend on Claude echoing text byte for byte:
// a replay with changed whitespace still acknowledges the posts it holds, a
// post that only quotes another post's ID does not acknowledge that post,
// and an acknowledged post is never resent.
func TestReplayAcknowledgesPostsByID(t *testing.T) {
	s := newStreamInput(discardCloser{io.Discard})
	first := []runstore.SteeringMessage{{ID: "msg_a", Message: frame("msg_a")}}
	second := []runstore.SteeringMessage{{ID: "msg_b", Message: frame("msg_b")}}
	for _, m := range [][]runstore.SteeringMessage{first, second} {
		if !s.write(streamItemSteering, steeringPrompt(m), m, 1) {
			t.Fatal("write failed")
		}
	}
	// Only quotes msg_b, in a reply to msg_a.
	s.observe(mergedReplayLine(t, "  "+steeringPrompt(first)+"\n\nsee msg_b \n"), 0, workeroutput.Usage{})
	got, _ := s.pending()
	if len(got) != 1 || got[0].ID != "msg_b" {
		t.Fatalf("pending after the first replay = %+v", got)
	}
	s.observe(mergedReplayLine(t, strings.ReplaceAll(steeringPrompt(second), "\n", "\r\n")), 0, workeroutput.Usage{})
	if got, _ := s.pending(); len(got) != 0 {
		t.Fatalf("pending after the second replay = %+v", got)
	}
}

// Two identical unframed writes and a replay holding only one: the second
// write stays pending, so the turn cannot seal before the worker reads it.
func TestOneReplayBlockAcknowledgesOneIdenticalWrite(t *testing.T) {
	s := newStreamInput(discardCloser{io.Discard})
	same := []runstore.SteeringMessage{{Message: "same"}}
	for range 2 {
		if !s.write(streamItemSteering, steeringPrompt(same), same, 1) {
			t.Fatal("write failed")
		}
	}
	s.observe(mergedReplayLine(t, steeringPrompt(same)), 0, workeroutput.Usage{})
	if s.idle() {
		t.Fatal("one replay block acknowledged both identical writes")
	}
	s.observe(mergedReplayLine(t, steeringPrompt(same)), 0, workeroutput.Usage{})
	if !s.idle() {
		t.Fatal("second replay did not acknowledge the second write")
	}
}
