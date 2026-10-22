package roomsvc

import (
	"testing"

	"dyna-agent/internal/notify"
	"dyna-agent/internal/room"
)

func TestVisualsShowsEachVisualOnceAtItsNewestVersion(t *testing.T) {
	e := newEnv(t)
	rec := e.room("visuals")
	snd := notify.Sender{Kind: notify.SenderOwner, Room: rec.Name}
	post := func(doc, visual string) room.Message {
		r, err := e.svc.Notify.Post(e.ctx, notify.PostInput{Sender: snd, Mentions: []string{"@root"}, Text: "t",
			Files: []notify.File{{Name: "v.html", Render: room.RenderHTML, Visual: visual, Data: []byte(doc)}}})
		if err != nil {
			t.Fatal(err)
		}
		return r.Message
	}
	first := post("<title>Chart</title><p>1</p>", "")
	other := post("<title>Other</title><p>x</p>", "")
	post("<title>Chart v2</title><p>2</p>", first.ID)
	got, err := e.svc.Visuals(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != first.ID || got[1].ID != other.ID {
		t.Fatalf("one row per visual, newest update first: %+v", got)
	}
	v := got[0]
	if v.Attachment.Title != "Chart v2" || v.Attachment.Version != 2 || len(v.Versions) != 2 || v.Versions[1].Post.ID != first.ID {
		t.Fatalf("a visual shows its newest version and keeps the older ones: %+v", v)
	}
}
