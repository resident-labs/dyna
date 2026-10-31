package pi

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptSteps(t *testing.T) {
	data := `{"type":"message","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"1","content":"start"}}
{"type":"message","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"c1","name":"read","arguments":{"path":"/tmp/a.go"}}]}}
{"type":"message","timestamp":"2026-01-01T00:00:02Z","message":{"role":"toolResult","toolCallId":"c1","isError":true,"content":[{"type":"text","text":"bad"}]}}
`
	steps, truncated, err := ReadSteps(strings.NewReader(data), map[contract.DeliverySeq]contract.TurnID{1: "turn_a"}, "", 200)
	if err != nil || truncated || len(steps) != 1 {
		t.Fatalf("steps=%+v truncated=%v err=%v", steps, truncated, err)
	}
	s := steps[0]
	if s.Turn != "turn_a" || s.Label != "read a.go" || !s.Failed || s.Preview != "bad" || s.Ended == nil {
		t.Fatalf("step=%+v", s)
	}
}
