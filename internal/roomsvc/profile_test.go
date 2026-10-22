package roomsvc

import (
	"testing"

	"dyna-agent/internal/room"
)

func TestProfileShowsParentAndNewestPosts(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	kid := e.start(rec, "decoder", &lead)
	for _, text := range []string{"one", "two", "three"} {
		if _, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: kid.Address(), To: []string{"alpha"}, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := e.svc.Profile(kid.ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if p.Member.Name != "decoder" || p.Parent != "lead" {
		t.Fatalf("profile = %+v", p)
	}
	if len(p.Posts) != 2 || p.Posts[0].Text != "two" || p.Posts[1].Text != "three" {
		t.Fatalf("posts = %+v, want the newest two, oldest first", p.Posts)
	}
}

func TestSendToSessionWakesItAsHuman(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	if _, err := e.svc.SendToSession(e.ctx, lead.ID, "   ", ""); err == nil {
		t.Fatal("an empty message must fail")
	}
	res, err := e.svc.SendToSession(e.ctx, lead.ID, "RESPOND: on it", "")
	if err != nil {
		t.Fatal(err)
	}
	m := res.Message
	if !m.Human || m.From != "alpha" || len(m.To) != 1 || m.To[0] != lead.Address() || SpeakerLabel(m, "alpha") != "you" {
		t.Fatalf("message = %+v", m)
	}
	e.wait(lead.ID, 2) // the post woke the next turn
}
