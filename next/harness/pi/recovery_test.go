package pi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestDeliveryAfterInterruptedWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	data := "{broken\n" + `{"type":"message","message":{"role":"user","deliveryId":"9"}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	delivered, err := (&Harness{session: path}).Delivered(context.Background(), 9)
	if err != nil || !delivered {
		t.Fatalf("delivered=%v err=%v", delivered, err)
	}
}

func TestDeliverySkipsLongLineAndReadsAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	entry := `{"type":"message","message":{"role":"user","deliveryId":"9"}}`
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 2048)+"\n"+entry[:30]), 0600); err != nil {
		t.Fatal(err)
	}
	h := &Harness{session: path, deliveryLineLimit: 1024}
	if found, err := h.Delivered(context.Background(), 9); err != nil || found {
		t.Fatalf("partial: found=%v err=%v", found, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(entry[30:] + "\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if found, err := h.Delivered(context.Background(), 9); err != nil || !found {
		t.Fatalf("appended: found=%v err=%v", found, err)
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
