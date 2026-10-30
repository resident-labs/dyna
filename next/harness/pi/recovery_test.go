package pi

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/resident-labs/dyna/contract"
)

func TestRecoveryOfSettledAssistant(t *testing.T) {
	h := recoveryFixture(t, `{"type":"message","message":{"role":"assistant","model":"requested","responseModel":"served","stopReason":"stop","content":[{"type":"text","text":"done"}],"usage":{"input":4,"output":2}}}`)
	result, done, err := h.Recover("turn_a", []contract.TurnInput{{Delivery: 9}})
	if err != nil || !done || result.Final != "done" || result.Usage.Model != "served" || result.Usage.InputTokens != 4 {
		t.Fatalf("result=%+v done=%v err=%v", result, done, err)
	}
}

func TestRecoveryOfInterruptedToolCall(t *testing.T) {
	h := recoveryFixture(t, `{"type":"message","message":{"role":"assistant","stopReason":"toolUse","content":[{"type":"toolCall"}]}}`)
	_, done, err := h.Recover("turn_a", []contract.TurnInput{{Delivery: 9}})
	if err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
}

func TestRecoveryAfterToolResultNeedsContinuation(t *testing.T) {
	h := recoveryFixture(t, `{"type":"message","message":{"role":"toolResult","content":[{"type":"text","text":"result"}]}}`)
	_, done, err := h.Recover("turn_a", []contract.TurnInput{{Delivery: 9}})
	if err != nil || done {
		t.Fatalf("done=%v err=%v", done, err)
	}
}

func TestPartialSessionEntryIsNotAcknowledged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"message","message":{"role":"user","deliveryId":"9"`), 0600); err != nil {
		t.Fatal(err)
	}
	h := &Harness{session: path}
	delivered, err := h.Delivered(context.Background(), 9)
	if err != nil || delivered {
		t.Fatalf("delivered=%v err=%v", delivered, err)
	}
}

func recoveryFixture(t *testing.T, tail string) *Harness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := `{"type":"message","message":{"role":"user","deliveryId":"9","content":"input"}}` + "\n" + tail + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return &Harness{session: path}
}
