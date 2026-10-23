package roomsvc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
)

// The DM, the seat's DM and the media list read the stored home and delivery
// rows (internal/room/postindex.go), never the address strings of a post. One
// test per rule.

// A post written in the DM is the DM's own post. A post the session writes
// elsewhere, and a completion, are not.
func TestSessionDMReadsThePostsWrittenInIt(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	peer := e.start(rec, "peer", nil)
	store := e.svc.Notify.Rooms
	dm := room.DMConversation(lead.ID)
	bound := map[string]string{lead.Address(): lead.ID}
	var want []string
	for _, in := range []room.PostInput{
		// The owner's DM composer says where it writes.
		{From: rec.Name, Human: true, Conversation: dm, To: []string{lead.Address()}, SessionIDs: bound, Text: "In the DM"},
		// The session's answer to that post stays in the DM.
		{From: lead.Address(), Conversation: dm, To: []string{rec.Name}, SessionIDs: bound, Text: "Answer in the DM"},
		// A post the session writes to another agent is not the DM's.
		{From: lead.Address(), To: []string{peer.Address()}, SessionIDs: map[string]string{peer.Address(): peer.ID}, Text: "To peer"},
		// A completion folds into its turn, so the DM does not draw it again.
		{From: lead.Address(), Kind: "completed", Conversation: dm, To: []string{rec.Name}, SessionIDs: bound, Text: "Finished"},
	} {
		msg, err := store.Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		if in.Conversation == dm && in.Kind == "" {
			want = append(want, msg.ID)
		}
	}
	sess, err := e.svc.transcript(lead.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, post := range sess.Posts {
		got = append(got, post.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DM posts = %v, want %v", got, want)
	}
	// The session's own completion of a turn in the room is the transcript's,
	// not a reference row.
	done, err := store.Post(rec.ID, room.PostInput{From: lead.Address(), Kind: "completed", To: []string{rec.Name}, SessionIDs: bound, Text: "Turn done"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err = e.svc.transcript(lead.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range sess.References {
		if row.PostID == done.ID {
			t.Fatalf("a completion reached the DM as a reference row: %+v", row)
		}
	}
}

// A post of another conversation that reached this session is one compact
// reference row, not a second copy.
func TestSessionDMShowsReceivedPostsAsReferences(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	peer := e.start(rec, "peer", nil)
	store := e.svc.Notify.Rooms
	attrs, err := store.PutAttachment(rec.ID, "log.txt", []byte("log"))
	if err != nil {
		t.Fatal(err)
	}
	peerPost, err := store.Post(rec.ID, room.PostInput{From: peer.Address(), To: []string{lead.Address()},
		SessionIDs: map[string]string{peer.Address(): peer.ID, lead.Address(): lead.ID}, Text: "Please review the parser",
		Attachments: []room.Attachment{attrs}})
	if err != nil {
		t.Fatal(err)
	}
	// A post delivered to another session is not this session's row.
	if _, err := store.Post(rec.ID, room.PostInput{From: peer.Address(), To: []string{rec.Name + ":other"},
		SessionIDs: map[string]string{peer.Address(): peer.ID}, Text: "Not for lead"}); err != nil {
		t.Fatal(err)
	}
	sess, err := e.svc.transcript(lead.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Posts) != 0 {
		t.Fatalf("a post of the room feed reached the DM posts: %+v", sess.Posts)
	}
	if len(sess.References) != 1 {
		t.Fatalf("references = %+v", sess.References)
	}
	row := sess.References[0]
	if row.PostID != peerPost.ID || row.From != peer.Address() || row.Speaker != "peer" || row.Conversation != room.ConversationRoom.String() {
		t.Fatalf("reference row = %+v", row)
	}
	if row.Preview != "Please review the parser" || row.Attachments != 1 || row.TS != peerPost.TS || row.FromHue != NameHue(peer.Address()) {
		t.Fatalf("reference row = %+v", row)
	}
}

// A post the session wrote in another conversation is a reference row too, so
// the DM keeps what the session said where the owner could see it.
func TestSessionDMShowsItsOwnPostsElsewhereAsReferences(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	peer := e.start(rec, "peer", nil)
	store := e.svc.Notify.Rooms
	report, err := store.Post(rec.ID, room.PostInput{From: lead.Address(), To: []string{peer.Address()},
		SessionIDs: map[string]string{lead.Address(): lead.ID, peer.Address(): peer.ID}, Text: "Parser is fixed"})
	if err != nil {
		t.Fatal(err)
	}
	sess, err := e.svc.transcript(lead.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	rows := rowsByPost(sess.References)
	row, ok := rows[report.ID]
	if !ok || len(sess.References) == 0 {
		t.Fatalf("references = %+v", sess.References)
	}
	if row.Speaker != "lead" || row.Conversation != room.ConversationRoom.String() {
		t.Fatalf("reference row = %+v", row)
	}
}

// The root seat's DM names the seat: the owner's DM post is the seat's own
// post, and a post that reached the seat from the room feed is a reference.
// The seat holder can change without moving either.
func TestSeatDMReadsTheSeatRows(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	store := e.svc.Notify.Rooms
	seat := room.SeatDMConversation(rec.ID)
	ask, err := store.Post(rec.ID, room.PostInput{From: rec.Name, Human: true, Conversation: seat, To: []string{rec.Name}, Via: []string{"@root"}, Text: "In the root DM"})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := store.PutAttachment(rec.ID, "screenshot.png", []byte("\x89PNG\r\n\x1a\n screenshot"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := store.Post(rec.ID, room.PostInput{From: lead.Address(), To: []string{rec.Name}, Via: []string{"@root"},
		SessionIDs: map[string]string{lead.Address(): lead.ID}, Text: "The parser is fixed", Attachments: []room.Attachment{shot}})
	if err != nil {
		t.Fatal(err)
	}
	// An external root posts as the room, not as a person, and no delivery row
	// names the seat for its own post: the rows name the seat as its author, so
	// the seat's DM keeps its answers whoever holds the seat.
	answer, err := store.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{rec.Name}, Text: "Fixed on the new branch"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := e.svc.RootSession(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(session.Posts) != 1 || session.Posts[0].ID != ask.ID {
		t.Fatalf("seat posts = %+v", session.Posts)
	}
	// The agent's report reached the seat from the room feed, so it is one row
	// that jumps to the post there.
	row, ok := rowsByPost(session.References)[report.ID]
	if !ok || row.Conversation != room.ConversationRoom.String() || row.Preview != "The parser is fixed" {
		t.Fatalf("seat references = %+v", session.References)
	}
	// The seat's own post is a reference row too, with the seat's label.
	own, ok := rowsByPost(session.References)[answer.ID]
	if !ok || own.Speaker != room.OwnerLabel(false) || own.Preview != "Fixed on the new branch" {
		t.Fatalf("the seat's own post is not a reference row: %+v", session.References)
	}
	if len(session.Media) != 1 || session.Media[0].ID != report.ID {
		t.Fatalf("seat media = %+v", session.Media)
	}
}

// rowsByPost keys reference rows by the post they name.
func rowsByPost(rows []ReferenceRow) map[string]ReferenceRow {
	out := make(map[string]ReferenceRow, len(rows))
	for _, row := range rows {
		out[row.PostID] = row
	}
	return out
}

// The DM composers say where they wrote: the session's DM composer names that
// session's DM, and the seat's names the seat's, so both posts read back in the
// conversation they were typed in.
func TestDMComposersNameTheirConversation(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	sent, err := e.svc.SendToSession(e.ctx, lead.ID, "Hello lead", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := sent.Message.Conversation, room.DMConversation(lead.ID); got != want {
		t.Fatalf("session DM post stored in %q, want %q", got, want)
	}
	seat, err := e.svc.SendFromConversation(e.ctx, rec.ID, "@root hello seat", "", "", room.SeatDMConversation(rec.ID), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := seat.Post.Message.Conversation, room.SeatDMConversation(rec.ID); got != want {
		t.Fatalf("seat DM post stored in %q, want %q", got, want)
	}
	// The room composer names nobody, so its post stays in the feed.
	feed, err := e.svc.Send(e.ctx, rec.ID, "@lead hello feed", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := feed.Post.Message.Conversation; got != room.ConversationRoom {
		t.Fatalf("room composer post stored in %q, want the room feed", got)
	}
}

// The media list is the files of the DM, of the posts delivered to the session
// and of the posts it wrote elsewhere, and of nothing else.
func TestSessionMediaFollowsTheDMRows(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	peer := e.start(rec, "peer", nil)
	store := e.svc.Notify.Rooms
	book := e.svc.avatars(rec.ID)
	att, err := store.PutAttachment(rec.ID, "notes.txt", []byte("notes"))
	if err != nil {
		t.Fatal(err)
	}
	dm := room.DMConversation(lead.ID)
	posts := map[string]room.PostInput{
		"own":       {From: lead.Address(), Conversation: dm, To: []string{rec.Name}, SessionIDs: map[string]string{lead.Address(): lead.ID}, Attachments: []room.Attachment{att}},
		"delivered": {From: peer.Address(), To: []string{lead.Address()}, SessionIDs: map[string]string{peer.Address(): peer.ID, lead.Address(): lead.ID}, Attachments: []room.Attachment{att}},
		"authored":  {From: lead.Address(), To: []string{peer.Address()}, SessionIDs: map[string]string{lead.Address(): lead.ID, peer.Address(): peer.ID}, Attachments: []room.Attachment{att}},
		"other":     {From: peer.Address(), To: []string{rec.Name + ":other"}, SessionIDs: map[string]string{peer.Address(): peer.ID}, Attachments: []room.Attachment{att}},
	}
	want := map[string]bool{}
	for name, in := range posts {
		msg, err := store.Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		want[msg.ID] = name != "other"
	}
	media, err := e.svc.sessionMedia(rec, dm, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 3 {
		t.Fatalf("media = %+v", media)
	}
	for _, post := range media {
		if !want[post.ID] {
			t.Errorf("media holds %s, which is in none of the DM's rows", post.ID)
		}
	}
}

func TestSessionMediaNewest200AndEmpty(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	book := e.svc.avatars(rec.ID)
	dm := room.DMConversation("ses_20261023-120000-aaaaaaaa")
	empty, err := e.svc.sessionMedia(rec, dm, book)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty media = %+v, %v", empty, err)
	}
	att, err := store.PutAttachment(rec.ID, "notes.txt", []byte("notes"))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 203; i++ {
		// Append out of time order to test chronological sorting before the cap.
		store.Now = func() time.Time { return time.Unix(2000-int64(i), 0) }
		msg, err := store.Post(rec.ID, room.PostInput{From: "alpha:lead", Conversation: dm, To: []string{rec.Name}, Attachments: []room.Attachment{att}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	store.Now = nil
	if _, err := store.Post(rec.ID, room.PostInput{From: "alpha:peer", To: []string{rec.Name}, Attachments: []room.Attachment{att}}); err != nil {
		t.Fatal(err)
	}
	media, err := e.svc.sessionMedia(rec, dm, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 200 {
		t.Fatalf("media count = %d", len(media))
	}
	for i, p := range media {
		if p.ID != ids[i] {
			t.Errorf("media %d = %s, want %s", i, p.ID, ids[i])
		}
	}
}

func TestSessionPostsTimeWindow(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	dm := room.DMConversation("ses_20261023-120000-aaaaaaaa")
	started := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var ids []string
	for _, delta := range []time.Duration{-time.Second, 885 * time.Millisecond, time.Second} {
		store.Now = func() time.Time { return started.Add(delta) }
		msg, err := store.Post(rec.ID, room.PostInput{From: "alpha:lead", Conversation: dm, To: []string{rec.Name}, Text: fmt.Sprint(delta)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	store.Now = nil
	for _, tc := range []struct {
		name  string
		start time.Time
		want  []string
	}{
		{"previous omitted turn ended", started, ids[1:]},
		{"no turns", time.Time{}, ids},
		{"no matching posts", started.Add(time.Hour), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs, err := store.ConversationMessages(rec.ID, dm, tc.start, time.Time{}, maxSessionPosts)
			if err != nil {
				t.Fatal(err)
			}
			posts, err := e.svc.shapeSessionPosts(rec, msgs, e.svc.avatars(rec.ID))
			if err != nil {
				t.Fatal(err)
			}
			if len(posts) != len(tc.want) {
				t.Fatalf("posts = %+v, want IDs %v", posts, tc.want)
			}
			for i, id := range tc.want {
				if posts[i].ID != id {
					t.Errorf("post %d = %q, want %q", i, posts[i].ID, id)
				}
			}
		})
	}
}

func TestSessionPostsKeepNewest300(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	dm := room.DMConversation("ses_20261023-120000-aaaaaaaa")
	var ids []string
	for i := 0; i < 303; i++ {
		msg, err := store.Post(rec.ID, room.PostInput{From: "alpha:lead", Conversation: dm, To: []string{rec.Name}, Text: fmt.Sprintf("Post %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	// Unrelated posts must not consume the conversation's limit.
	if _, err := store.Post(rec.ID, room.PostInput{From: "alpha:peer", To: []string{rec.Name}, Text: "Unrelated"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.ConversationMessages(rec.ID, dm, time.Time{}, time.Time{}, maxSessionPosts)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := e.svc.shapeSessionPosts(rec, msgs, e.svc.avatars(rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 300 {
		t.Fatalf("post count = %d, want 300", len(posts))
	}
	for i, post := range posts {
		if post.ID != ids[i+3] {
			t.Errorf("post %d = %q, want %q", i, post.ID, ids[i+3])
		}
	}
}

// A turn item names the post that carried it. A post of the DM that the page
// window left out still belongs to the message, so its files reach the DM.
func TestSessionPostsHoldThePostOfATurnItemOutsideThePage(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	shot, err := store.PutAttachment(rec.ID, "screenshot.png", []byte("\x89PNG\r\n\x1a\n screenshot"))
	if err != nil {
		t.Fatal(err)
	}
	dm := room.DMConversation("ses_20261023-120000-aaaaaaaa")
	ask, err := store.Post(rec.ID, room.PostInput{From: rec.Name, Human: true, Conversation: dm, To: []string{"alpha:lead"}, Text: "Look", Attachments: []room.Attachment{shot}})
	if err != nil {
		t.Fatal(err)
	}
	book := e.svc.avatars(rec.ID)
	// The DM page starts after the post, and only its turn item still names it.
	items := []Item{{Kind: "prompt", PostID: ask.ID, Text: "Look", From: rec.Name + " (human)"}}
	posts, err := e.svc.withItemPosts(rec, nil, dm, "", "alpha:lead", []Turn{{N: 1, Items: items}}, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].ID != ask.ID || len(posts[0].Attachments) != 1 {
		t.Fatalf("item posts: %+v", posts)
	}
	// A post of another conversation stays a reference row, not a post.
	posts, err = e.svc.withItemPosts(rec, nil, room.DMConversation("ses_20261023-120001-bbbbbbbb"), "", "alpha:lead", []Turn{{N: 1, Items: items}}, book)
	if err != nil || len(posts) != 0 {
		t.Fatalf("item posts of another DM: %+v, %v", posts, err)
	}
}

func TestTranscriptPostsUseOmittedTurnEnd(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	// Store two completed turns with a gap before the waking post.
	base := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	if _, err := e.mgr.Store.UpdateTurn(lead.ID, 1, func(turn *subagent.Turn) { turn.Started = base; turn.Ended = base.Add(time.Minute) }); err != nil {
		t.Fatal(err)
	}
	second := subagent.Turn{N: 2, Status: subagent.TurnOK, Started: base.Add(2*time.Minute + 72*time.Millisecond), Ended: base.Add(3 * time.Minute)}
	data, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	seedTurnRow(t, e.mgr.Store, lead.ID, 2, data)
	if _, err := e.mgr.Store.UpdateMeta(lead.ID, func(meta *subagent.Meta) { meta.Turns = 2 }); err != nil {
		t.Fatal(err)
	}
	store := e.svc.Notify.Rooms
	dm := room.DMConversation(lead.ID)
	var ids []string
	for _, delta := range []time.Duration{30 * time.Second, 2*time.Minute - 43*time.Millisecond, 4 * time.Minute} {
		store.Now = func() time.Time { return base.Add(delta) }
		msg, err := store.Post(rec.ID, room.PostInput{From: lead.Address(), Conversation: dm, To: []string{rec.Name}, Text: "report"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	store.Now = nil
	for _, tc := range []struct {
		name    string
		count   int
		focused bool
		want    []string
	}{
		{"trimmed", 1, false, ids[1:]},
		{"untrimmed", 0, false, ids},
		{"focused", 0, true, ids[1:2]},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Session
			var err error
			if tc.focused {
				got, err = e.svc.TranscriptTurn(lead.ID, 2)
			} else {
				got, err = e.svc.transcript(lead.ID, tc.count, 0)
			}
			if err != nil {
				t.Fatal(err)
			}
			var actual []string
			for _, p := range got.Posts {
				actual = append(actual, p.ID)
			}
			if !reflect.DeepEqual(actual, tc.want) {
				t.Fatalf("posts %v, want %v", actual, tc.want)
			}
			if got.Earlier != (tc.count == 1) {
				t.Fatalf("earlier=%v", got.Earlier)
			}
		})
	}
}

// The DM's posts are stored posts shaped like the feed's, so the two views draw
// the same row.
func TestTranscriptPostsMatchFeed(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	store := e.svc.Notify.Rooms
	dm := room.DMConversation(lead.ID)
	card, err := store.PutAttachment(rec.ID, "card.html", []byte("<!doctype html><html><head><title>Build status</title></head><body>Ready</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	card.Render = room.RenderHTML
	var ids []string
	for _, in := range []room.PostInput{
		{From: rec.Name, Human: true, Conversation: dm, To: []string{lead.Address()}, Text: "Owner prompt"},
		{From: lead.Address(), Conversation: dm, To: []string{rec.Name}, Text: "My card", Attachments: []room.Attachment{card}},
		{From: lead.Address(), Conversation: dm, To: []string{rec.Name}, Text: "No files"},
	} {
		msg, err := store.Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	sess, err := e.svc.transcript(lead.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Address != lead.Address() || len(sess.Posts) != len(ids) {
		t.Fatalf("address = %q, posts = %+v", sess.Address, sess.Posts)
	}
	feed, _, err := e.svc.FeedSince(rec.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Post{}
	for _, post := range feed {
		// Feed views route the room feed only; transcripts have none.
		post.setViews(nil)
		byID[post.ID] = post
	}
	for i, id := range ids {
		if !reflect.DeepEqual(sess.Posts[i], byID[id]) {
			t.Errorf("post %d = %+v, want feed post %+v", i, sess.Posts[i], byID[id])
		}
	}
	first := sess.Posts[1]
	if first.Owner || first.Speaker != "lead" || first.FromEmoji == "" || first.FromHue != NameHue(lead.Address()) {
		t.Fatalf("sender shaping = %+v", first)
	}
	if len(first.Attachments) != 1 || !first.Attachments[0].Rich() || first.Attachments[0].Title != "Build status" {
		t.Fatalf("card = %+v", first.Attachments)
	}
}
