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

// The member scan passes each record it already holds to QueueSignatureOf
// instead of paying one address query per session. A record re-read after a
// rename, and delivery rows an outside writer changed, must give the same
// signature QueueSignature builds from a fresh address lookup: rows are read
// from the table on every call, so no cached address survives a change.
func TestQueueSignatureOfMatchesQueueSignature(t *testing.T) {
	e := newTestEnv(t)
	lead := completedFixtureSession(t, e)
	setDeliveryRow(t, e, lead.ID, "msg_sig_of", room.DeliveryClaimed, 1)
	sigOf := func(stage string) string {
		t.Helper()
		meta, err := e.m.Store.ReadMeta(lead.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := e.m.Store.QueueSignature(lead.ID)
		if got := e.m.Store.QueueSignatureOf(meta); got != want || got == "" {
			t.Fatalf("%s: QueueSignatureOf = %q, want the non-empty QueueSignature %q", stage, got, want)
		}
		return want
	}
	sigOf("after a claim")
	// A rename moves the address the delivery keys use; a re-read record
	// names the new one, as the roster re-read does after the record write.
	if _, err := e.m.Store.UpdateMeta(lead.ID, func(m *Meta) { m.Name = "renamed" }); err != nil {
		t.Fatal(err)
	}
	sigOf("after a rename")
	// An outside writer's dismissal is a fresh row read, not a cached value.
	setDeliveryRow(t, e, lead.ID, "msg_outside", room.DeliveryDismissed, 0)
	sigOf("after an outside delivery write")
}
