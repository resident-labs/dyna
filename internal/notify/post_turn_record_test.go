package notify

import (
	"errors"
	"testing"
)

// A post stored from a turn whose record is missing still reaches its
// recipient. The caller gets the stored post and a failed delivery, so it
// does not send the same text again.
func TestStoredPostWithoutTurnRecordReportsFailedDelivery(t *testing.T) {
	e := newTestEnv(t, withImmediateTurns)
	rec := e.room()
	parent := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &parent)
	// Turn 9 has no record, so recording the post on it fails after the store.
	child.CurrentTurn = 9
	res, err := e.svc.Post(e.ctx, PostInput{Sender: Sender{Kind: SenderSession, Room: rec.Name, Session: child}, Mentions: []string{"@parent"}, Text: "Which option sets the lifetime?"})
	if res.Message.ID == "" {
		t.Fatalf("the post was not stored: %v", err)
	}
	if !errors.Is(err, ErrDelivery) {
		t.Fatalf("a stored post reported %v, want ErrDelivery", err)
	}
	if failed := res.Failed(); len(failed) != 1 {
		t.Fatalf("failed deliveries = %+v, want only the turn record", failed)
	}
}
