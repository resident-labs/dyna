package subagent

import (
	"errors"
	"testing"

	"dyna-agent/internal/room"
)

func TestQueueSignatureNeedsOnlyIndexedAddress(t *testing.T) {
	e := newTestEnv(t)
	lead := completedFixtureSession(t, e)
	setDeliveryRow(t, e, lead.ID, "msg_signature", room.DeliveryClaimed, 1)
	want := e.m.Store.QueueSignature(lead.ID)
	if want == "" {
		t.Fatal("fixture has no delivery signature")
	}
	infoCache.Delete(e.m.Store.recordKey(lead.ID))
	e.m.Store.decodeMeta = func([]byte, any) error { return errors.New("full metadata decode is not needed") }
	if got := e.m.Store.QueueSignature(lead.ID); got != want {
		t.Fatalf("QueueSignature = %q, want %q without decoding full metadata", got, want)
	}
}
