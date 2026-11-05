package pig

import (
	"strings"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

// The records have the shape of horizon's fork-review-lead session at 13:00:01: a model call failed with a socket error
// after it streamed some thinking, and Pi removed the message and its paired tool result from the context with context_edit.
const failedAttemptRecords = `{"type":"message","id":"u1","timestamp":"2026-11-04T12:58:00Z","message":{"role":"user","deliveryId":"1"}}
{"type":"message","id":"a1","timestamp":"2026-11-04T12:58:30Z","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"text","text":"reading the diff"},{"type":"toolCall","id":"kept","name":"bash","arguments":{}}]}}
{"type":"message","id":"r1","timestamp":"2026-11-04T12:58:31Z","message":{"role":"toolResult","toolCallId":"kept","content":[{"type":"text","text":"ok"}]}}
{"type":"message","id":"a2","timestamp":"2026-11-04T13:00:01Z","message":{"role":"assistant","stopReason":"error","errorMessage":"unexpected EOF","diagnostics":[{"type":"provider_transport_failure","timestamp":1,"error":{"name":"SocketError","message":"unexpected EOF","code":"UND_ERR_SOCKET"},"details":{"phase":"after_message_stream_start"}}],"content":[{"type":"thinking","thinking":"**Checking the lock**"},{"type":"thinking","thinking":""},{"type":"toolCall","id":"lost","name":"bash","arguments":{}}]}}
{"type":"message","id":"r2","timestamp":"2026-11-04T13:00:01Z","message":{"role":"toolResult","toolCallId":"lost","content":[{"type":"text","text":"interrupted"}]}}
{"type":"context_edit","id":"e1","parentId":"r2","timestamp":"2026-11-04T13:00:02Z","targetId":"a2","replacement":null}
{"type":"context_edit","id":"e2","parentId":"e1","timestamp":"2026-11-04T13:00:02Z","targetId":"r2","replacement":null}
{"type":"message","id":"a3","timestamp":"2026-11-04T13:01:00Z","message":{"role":"assistant","stopReason":"stop","content":[{"type":"text","text":"the lock is free"}]}}
`

var attemptDeliveries = map[contract.DeliverySeq]contract.TurnID{1: "t"}

func TestTranscriptReplacesAFailedAttemptPiGRemovedWithOneMarker(t *testing.T) {
	items, seen, _, err := ReadTranscript(strings.NewReader(failedAttemptRecords), attemptDeliveries, "x", "", 0, 64)
	if err != nil || seen != 4 || len(items) != 4 {
		t.Fatalf("seen=%d items=%+v err=%v", seen, items, err)
	}
	kinds := []contract.TranscriptItemKind{contract.TranscriptText, contract.TranscriptTool, contract.TranscriptAttemptFailed, contract.TranscriptText}
	for i, kind := range kinds {
		if items[i].Kind != kind || items[i].Index != i {
			t.Fatalf("item %d is %s at index %d, want %s: %+v", i, items[i].Kind, items[i].Index, kind, items)
		}
	}
	marker := items[2]
	if marker.ErrorName != "SocketError" || marker.Text != "" || marker.Turn != "t" || marker.ID != "a2" || marker.At.IsZero() {
		t.Fatalf("marker=%+v", marker)
	}
	if items[1].Output != "ok" {
		t.Fatalf("the kept call lost its result: %+v", items[1])
	}
}

func TestTranscriptMarkerIsStableAcrossPagesAndRecentReads(t *testing.T) {
	page, seen, more, err := ReadTranscript(strings.NewReader(failedAttemptRecords), attemptDeliveries, "x", "", 2, 1)
	if err != nil || seen != 4 || !more || len(page) != 1 || page[0].Kind != contract.TranscriptAttemptFailed || page[0].Index != 2 {
		t.Fatalf("page=%+v seen=%d more=%v err=%v", page, seen, more, err)
	}
	recent, seen, more, err := readTranscriptMode(strings.NewReader(failedAttemptRecords), attemptDeliveries, nil, "x", "", 0, 64, 16<<20, true)
	if err != nil || seen != 4 || more || len(recent) != 4 || recent[2].Kind != contract.TranscriptAttemptFailed || recent[3].Text != "the lock is free" {
		t.Fatalf("recent=%+v seen=%d more=%v err=%v", recent, seen, more, err)
	}
}

func TestTranscriptKeepsMessagesPiKept(t *testing.T) {
	data := `{"type":"message","id":"u1","message":{"role":"user","deliveryId":"1"}}
{"type":"message","id":"a1","message":{"role":"assistant","stopReason":"error","content":[{"type":"text","text":"partial answer"}]}}
{"type":"message","id":"a2","message":{"role":"assistant","stopReason":"aborted","content":[{"type":"text","text":"edited answer"}]}}
{"type":"context_edit","id":"e1","targetId":"a2","replacement":{"content":"shorter"}}
`
	items, _, _, err := ReadTranscript(strings.NewReader(data), attemptDeliveries, "x", "", 0, 64)
	if err != nil || len(items) != 2 || items[0].Text != "partial answer" || items[1].Text != "edited answer" {
		t.Fatalf("a kept message, or one an edit only rewrote, must show: %+v err=%v", items, err)
	}
}

func TestTranscriptSkipsBlocksWithNothingInThem(t *testing.T) {
	data := `{"type":"message","id":"u1","message":{"role":"user","deliveryId":"1"}}
{"type":"message","id":"a1","message":{"role":"assistant","stopReason":"aborted","content":[{"type":"thinking","thinking":""},{"type":"text","text":""}]}}
{"type":"message","id":"a2","message":{"role":"assistant","stopReason":"stop","content":[{"type":"thinking","thinking":""},{"type":"text","text":"answer"}]}}
`
	items, seen, _, err := ReadTranscript(strings.NewReader(data), attemptDeliveries, "x", "", 0, 64)
	if err != nil || seen != 1 || len(items) != 1 || items[0].Text != "answer" || items[0].Index != 0 {
		t.Fatalf("seen=%d items=%+v err=%v", seen, items, err)
	}
}

func TestTranscriptMarkerWithoutADiagnosticHasNoName(t *testing.T) {
	data := `{"type":"message","id":"u1","message":{"role":"user","deliveryId":"1"}}
{"type":"message","id":"a1","message":{"role":"assistant","stopReason":"error","content":[{"type":"text","text":"half"}]}}
{"type":"context_edit","id":"e1","targetId":"a1","replacement":null}
`
	items, _, _, err := ReadTranscript(strings.NewReader(data), attemptDeliveries, "x", "", 0, 64)
	if err != nil || len(items) != 1 || items[0].Kind != contract.TranscriptAttemptFailed || items[0].ErrorName != "" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestTranscriptMarkerNameIsMaskedAndBounded(t *testing.T) {
	data := `{"type":"message","id":"u1","message":{"role":"user","deliveryId":"1"}}
{"type":"message","id":"a1","message":{"role":"assistant","content":[],"diagnostics":[{"type":"x","error":{"name":"` + strings.Repeat("E", 500) + `"}}]}}
{"type":"context_edit","id":"e1","targetId":"a1","replacement":null}
`
	items, _, _, err := ReadTranscript(strings.NewReader(data), attemptDeliveries, "x", "", 0, 64)
	if err != nil || len(items) != 1 || len(items[0].ErrorName) != 64 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestTranscriptMarkerNameHidesSecrets(t *testing.T) {
	data := `{"type":"message","id":"u1","message":{"role":"user","deliveryId":"1"}}
{"type":"message","id":"a1","message":{"role":"assistant","content":[],"diagnostics":[{"type":"x","error":{"name":"failed ghp_abcdefghijklmnopqrstuvwxyz0123"}}]}}
{"type":"context_edit","id":"e1","targetId":"a1","replacement":null}
`
	items, _, _, err := ReadTranscript(strings.NewReader(data), attemptDeliveries, "x", "", 0, 64)
	if err != nil || len(items) != 1 || strings.Contains(items[0].ErrorName, "ghp_") {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}
