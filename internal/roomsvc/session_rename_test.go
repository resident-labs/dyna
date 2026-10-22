package roomsvc

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

func TestRenamedSessionKeepsConversationAndMedia(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	peer := e.start(rec, "peer", nil)
	store := e.svc.Notify.Rooms
	card, err := store.PutAttachment(rec.ID, "card.html", []byte("<!doctype html><html><body>Ready</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	card.Render = room.RenderHTML
	oldAddr := lead.Address()
	bound := map[string]string{oldAddr: lead.ID}
	for _, in := range []room.PostInput{
		{From: peer.Address(), To: []string{oldAddr}, SessionIDs: bound, Text: "Before rename"},
		{From: peer.Address(), To: []string{oldAddr}, SessionIDs: bound, Text: "Card for lead", Attachments: []room.Attachment{card}},
		{From: peer.Address(), To: []string{peer.Address()}, SessionIDs: map[string]string{peer.Address(): peer.ID}, Text: "Other session's post"},
	} {
		if _, err := store.Post(rec.ID, in); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.svc.Transcript(lead.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.mgr.Rename(context.Background(), lead.ID, "captain"); err != nil {
		t.Fatal(err)
	}
	view, err := e.svc.Transcript(lead.ID)
	if err != nil || len(view.Posts) != 2 || len(view.Media) != 1 {
		t.Fatalf("renamed indexed conversation: %+v, %v", view, err)
	}
	msgs, err := store.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	newAddr := rec.Name + ":captain"
	book := e.svc.avatars(rec.ID)
	posts, err := e.svc.sessionPosts(rec, msgs, lead.ID, newAddr, time.Time{}, time.Time{}, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 || posts[0].Text != "Before rename" || posts[1].Text != "Card for lead" {
		t.Fatalf("renamed lead posts = %+v", posts)
	}
	media, err := e.svc.sessionMedia(rec, msgs, lead.ID, newAddr, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 1 {
		t.Fatalf("renamed lead media = %+v", media)
	}
}
