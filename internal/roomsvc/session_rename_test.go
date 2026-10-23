package roomsvc

import (
	"context"
	"testing"
	"time"

	"dyna-agent/internal/room"
)

// A rename moves the session's address, not its rows: its DM and its media
// still read the same posts, because the posts name the session by ID.
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
	before, err := e.mgr.Store.ReadTurn(lead.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	// The posts of this DM are written before the rename, and its address is
	// the one the reader no longer knows.
	store.Now = func() time.Time { return before.Started.Add(-time.Minute) }
	dm := room.DMConversation(lead.ID)
	oldAddr := lead.Address()
	var want []string
	for _, in := range []room.PostInput{
		{From: rec.Name, Human: true, Conversation: dm, To: []string{oldAddr}, SessionIDs: map[string]string{oldAddr: lead.ID}, Text: "Before rename"},
		{From: rec.Name, Human: true, Conversation: dm, To: []string{oldAddr}, SessionIDs: map[string]string{oldAddr: lead.ID}, Text: "Card for lead", Attachments: []room.Attachment{card}},
		{From: peer.Address(), To: []string{peer.Address()}, SessionIDs: map[string]string{peer.Address(): peer.ID}, Text: "Other session's post"},
	} {
		msg, err := store.Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		if in.Conversation == dm {
			want = append(want, msg.ID)
		}
	}
	store.Now = nil
	if _, err := e.mgr.Rename(context.Background(), lead.ID, "captain"); err != nil {
		t.Fatal(err)
	}
	view, err := e.svc.Transcript(lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, post := range view.Posts {
		got = append(got, post.ID)
	}
	if len(view.Media) != 1 {
		t.Fatalf("renamed media = %+v", view.Media)
	}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("renamed DM posts = %v, want %v", got, want)
	}
	if view.Media[0].ID != want[1] {
		t.Fatalf("renamed media = %+v, want the card post", view.Media)
	}
}
