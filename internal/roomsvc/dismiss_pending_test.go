package roomsvc

import (
	"dyna-agent/internal/room"
	"testing"
)

func TestDismissPendingAPIRequiresConfirmationAndKeepsHistory(t *testing.T) {
	e := newEnv(t)
	rec := e.room("dismiss")
	info := e.start(rec, "worker", nil)
	msg, err := e.svc.rooms().Post(rec.ID, room.PostInput{Wake: true, From: rec.Name + ":reporter", To: []string{info.Address()}, Text: "unread report"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.svc.DismissPendingPost(e.ctx, info.ID, msg.ID, false); err == nil {
		t.Fatal("API accepted unconfirmed dismissal")
	}
	if err := e.svc.DismissPendingPost(e.ctx, info.ID, msg.ID, true); err != nil {
		t.Fatal(err)
	}
	queue, err := e.svc.SessionQueue(info.ID)
	if err != nil || len(queue) != 1 || queue[0].State != "dismissed" {
		t.Fatalf("queue: %+v %v", queue, err)
	}
	if _, err := e.svc.rooms().Message(rec.ID, msg.ID); err != nil {
		t.Fatal("history lost", err)
	}
}
