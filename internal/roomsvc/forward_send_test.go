package roomsvc

import (
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func TestSendForwardsPostWithoutANote(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	original, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{rec.Name}, Text: "original result"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.Send(e.ctx, rec.ID, "@root", "", original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Kind != "post" || res.Post.Message.Forward == nil || res.Post.Message.Forward.ID != original.ID || res.Post.Message.Forward.Text != original.Text {
		t.Fatalf("forward = %+v", res)
	}
	if _, err := e.svc.Send(e.ctx, rec.ID, "@root", "", "missing"); err == nil {
		t.Fatal("an unknown forward source must fail")
	}
}

func TestSendRejectsForwardedSpawn(t *testing.T) {
	e := newEnv(t)
	if _, err := e.svc.Send(e.ctx, "alpha", "/mock check this", "", "msg_source"); err == nil || !strings.Contains(err.Error(), "forwarded post") {
		t.Fatalf("spawn with a forward = %v", err)
	}
}

func TestSendToSessionForwardsPostWithoutANote(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	original, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{rec.Name}, Text: "original result"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.SendToSession(e.ctx, lead.ID, "", original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Message.Forward == nil || res.Message.Forward.ID != original.ID || res.Message.Forward.Text != original.Text || len(res.Message.To) != 1 || res.Message.To[0] != lead.Address() {
		t.Fatalf("forward = %+v", res.Message)
	}
	e.wait(lead.ID, 2)
}
