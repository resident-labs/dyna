package pi

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestTranscriptItemsMaskAndKeepToolIdentity(t *testing.T) {
	data := `{"type":"message","id":"m1","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","deliveryId":"1","content":[{"type":"text","text":"wake"}]}}
{"type":"message","id":"m2","timestamp":"2026-01-01T00:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"reason"},{"type":"text","text":"answer"},{"type":"toolCall","id":"call1","name":"bash","arguments":{"command":"token=secretvalue"}}]}}
{"type":"message","id":"m3","timestamp":"2026-01-01T00:00:02Z","message":{"role":"toolResult","toolCallId":"call1","content":[{"type":"text","text":"token=secretvalue"}]}}
`
	items, _, _, err := ReadTranscript(strings.NewReader(data), map[contract.DeliverySeq]contract.TurnID{1: "turn_a"}, "test", "", 0, 64)
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].Kind != contract.TranscriptThinking || items[1].Kind != contract.TranscriptText || items[2].ID != "call1" || items[2].Turn != "turn_a" || strings.Contains(items[2].Arguments, "secretvalue") || strings.Contains(items[2].Output, "secretvalue") {
		t.Fatalf("items=%+v", items)
	}
}

func TestTranscriptReportsOversizedRecord(t *testing.T) {
	user := `{"type":"message","message":{"role":"user","deliveryId":"1"}}` + "\n"
	oversized := strings.Repeat("x", 80) + "\n"
	items, _, _, err := readTranscript(strings.NewReader(user+oversized), map[contract.DeliverySeq]contract.TurnID{1: "turn_a"}, "file", "", 0, 64, 70)
	if err != nil || len(items) != 1 || items[0].Kind != contract.TranscriptTooLarge || items[0].Bytes != int64(len(oversized)) || items[0].Turn != "turn_a" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestTranscriptItemIndexContinuesInOrder(t *testing.T) {
	data := `{"type":"message","message":{"role":"user","deliveryId":"1"}}
{"type":"message","id":"m","message":{"role":"assistant","content":[{"type":"text","text":"a"},{"type":"text","text":"b"},{"type":"text","text":"c"}]}}
`
	deliveries := map[contract.DeliverySeq]contract.TurnID{1: "t"}
	first, _, more, err := ReadTranscript(strings.NewReader(data), deliveries, "x", "", 0, 2)
	if err != nil || !more || len(first) != 2 || first[1].Text != "b" {
		t.Fatalf("first=%+v more=%v err=%v", first, more, err)
	}
	next, _, more, err := ReadTranscript(strings.NewReader(data), deliveries, "x", "", 2, 2)
	if err != nil || more || len(next) != 1 || next[0].Text != "c" {
		t.Fatalf("next=%+v more=%v err=%v", next, more, err)
	}
}

func TestTranscriptCallClaimRequiresExactID(t *testing.T) {
	data := `{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","id":"right","name":"mcp__shell__shell"}]}}
{"type":"message","message":{"role":"toolResult","toolCallId":"right","content":[{"type":"text","text":"fake-id"}]}}
`
	ids := map[string]bool{"right": false, "fake-id": false}
	if err := TranscriptFindCalls(strings.NewReader(data), ids); err != nil || !ids["right"] || ids["fake-id"] {
		t.Fatalf("calls=%v err=%v", ids, err)
	}
}

func TestTranscriptBoundsToolIdentity(t *testing.T) {
	id := strings.Repeat("x", 200000)
	name := strings.Repeat("n", 200000)
	data := `{"type":"message","message":{"role":"user","deliveryId":"1"}}
{"type":"message","message":{"role":"assistant","content":[{"type":"toolCall","id":"` + id + `","name":"` + name + `"}]}}
`
	items, _, _, err := ReadTranscript(strings.NewReader(data), map[contract.DeliverySeq]contract.TurnID{1: "t"}, "x", "", 0, 1)
	if err != nil || len(items) != 1 || len(items[0].ID) > 256 || len(items[0].Tool) > 256 {
		t.Fatalf("item identity was not bounded: %v", err)
	}
	full, _, _, err := ReadTranscript(strings.NewReader(data), map[contract.DeliverySeq]contract.TurnID{1: "t"}, "x", items[0].ID, 0, 1)
	if err != nil || len(full) != 1 || full[0].ID != items[0].ID {
		t.Fatalf("stable item lookup failed: %v", err)
	}
}
