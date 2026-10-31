package pi

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptMasksBeforeLabelLimit(t *testing.T) {
	secret := "ghp_" + strings.Repeat("a", 100)
	data := `{"type":"message","message":{"role":"user","deliveryId":"1"}}` + "\n" +
		`{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","id":"c","name":"bash","arguments":{"command":"` + secret + `"}}]}}` + "\n"
	steps, _, err := ReadSteps(strings.NewReader(data), map[contract.DeliverySeq]contract.TurnID{1: "turn_a"}, "", 200)
	if err != nil || len(steps) != 1 || steps[0].Label != "bash [masked]" {
		t.Fatalf("steps=%+v err=%v", steps, err)
	}
}

func TestTranscriptNewestAndIncomplete(t *testing.T) {
	data := `{"type":"message","message":{"role":"user","deliveryId":"1"}}
{"type":"message","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"toolCall","id":"a","name":"old"},{"type":"toolCall","id":"b","name":"new"}]}}
{"type":"message","message":{"role":"toolResult","toolCallId":"b","content":[{"type":"text","text":"ok"}]}}
{"type":"message"`
	steps, truncated, err := ReadSteps(strings.NewReader(data), map[contract.DeliverySeq]contract.TurnID{1: "turn_a"}, "", 1)
	if err != nil || !truncated || len(steps) != 1 || steps[0].Tool != "new" || steps[0].Preview != "ok" {
		t.Fatalf("steps=%+v truncated=%v err=%v", steps, truncated, err)
	}
}

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
