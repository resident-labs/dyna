package roomsvc

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/subagent"

	"dyna-agent/internal/room"
)

func TestTranscriptPostsMatchFeed(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	peer := e.start(rec, "peer", nil)
	store := e.svc.Notify.Rooms
	turn, err := e.mgr.Store.ReadTurn(lead.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return turn.Started.Add(-time.Second) }
	before, err := store.Post(rec.ID, room.PostInput{From: lead.Address(), To: []string{rec.Name}, Text: "Before the returned turn"})
	if err != nil {
		t.Fatal(err)
	}
	store.Now = nil
	card, err := store.PutAttachment(rec.ID, "card.html", []byte("<!doctype html><html><head><title>Build status</title></head><body>Ready</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	card.Render = room.RenderHTML
	inputs := []room.PostInput{
		{From: lead.Address(), To: []string{rec.Name}, Text: "My card", Attachments: []room.Attachment{card}},
		{From: lead.Address(), To: []string{peer.Address()}, Text: "To peer"},
		{From: peer.Address(), To: []string{lead.Address()}, Text: "From peer"},
		{From: peer.Address(), Kind: "completed", To: []string{lead.Address()}, Text: "Peer finished"},
		{From: rec.Name, Human: true, To: []string{lead.Address()}, Text: "Owner prompt"},
		{From: rec.Name, To: []string{lead.Address()}, Text: "Owner steering"},
		{From: peer.Address(), To: []string{rec.Name}, Text: "Unrelated"},
		{From: lead.Address(), Kind: "completed", To: []string{rec.Name}, Text: "Finished"},
		{From: lead.Address(), Kind: "failed", To: []string{rec.Name}, Text: "Failed"},
		{From: lead.Address(), Kind: "stopped", To: []string{rec.Name}, Text: "Stopped"},
	}
	ids := []string{before.ID}
	for i, input := range inputs {
		if input.Text == "" {
			input.Text = "Media report is ready."
		}
		msg, err := store.Post(rec.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		if i < 6 {
			ids = append(ids, msg.ID)
		}
	}
	// A PR event is an engine post; it reaches the feed like any other post.
	// A typed PR event reaches the feed like any other post.
	if _, err := store.PostEventInput(room.PostEventInput{RoomID: rec.ID, To: []string{rec.Name}, RoomService: true,
		Event: room.PREvent{Type: room.PRApproved, Label: "PR #1", Location: "github.com/o/r#1", ChangeID: "github.com/o/r#1", Number: 1, Head: strings.Repeat("a", 40)}}); err != nil {
		t.Fatal(err)
	}
	sess, err := e.svc.transcript(lead.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Address != lead.Address() || len(sess.Posts) != len(ids) {
		t.Fatalf("address = %q, posts = %+v", sess.Address, sess.Posts)
	}
	feed, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Post{}
	for _, post := range feed.Posts {
		// Feed views route the room feed only; transcripts have none.
		post.Views = nil
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

func TestSessionPostsTimeWindow(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	started := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	var ids []string
	for _, delta := range []time.Duration{-time.Second, 885 * time.Millisecond, time.Second} {
		store.Now = func() time.Time { return started.Add(delta) }
		msg, err := store.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{rec.Name}, Text: fmt.Sprint(delta)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
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
			msgs, err := store.Messages(rec.ID)
			if err != nil {
				t.Fatal(err)
			}
			posts, err := e.svc.sessionPosts(rec, msgs, "", "alpha:lead", tc.start, time.Time{}, e.svc.avatars(rec.ID))
			if err != nil {
				t.Fatal(err)
			}
			if posts == nil || len(posts) != len(tc.want) {
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
	var ids []string
	for i := 0; i < 303; i++ {
		msg, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{rec.Name}, Text: fmt.Sprintf("Post %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	// Unrelated posts must not consume the conversation's limit.
	if _, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: "alpha:peer", To: []string{rec.Name}, Text: "Unrelated"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := e.svc.Notify.Rooms.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := e.svc.sessionPosts(rec, msgs, "", "alpha:lead", time.Time{}, time.Time{}, e.svc.avatars(rec.ID))
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

func TestTranscriptMediaIncludesWholeConversation(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	peer := e.start(rec, "peer", nil)
	store := e.svc.Notify.Rooms
	card, err := store.PutAttachment(rec.ID, "card.html", []byte("<!doctype html><title>Media report</title><p>Ready</p>"))
	if err != nil {
		t.Fatal(err)
	}
	card.Render = room.RenderHTML
	turn, err := e.mgr.Store.ReadTurn(lead.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	store.Now = func() time.Time { return turn.Started.Add(-time.Hour) }
	inputs := []room.PostInput{
		{From: lead.Address(), To: []string{rec.Name}, Attachments: []room.Attachment{card}},
		{From: rec.Name, Human: true, To: []string{lead.Address()}, Attachments: []room.Attachment{card}},
		{From: peer.Address(), To: []string{lead.Address()}, Attachments: []room.Attachment{card}},
		{From: lead.Address(), Kind: "completed", To: []string{rec.Name}, Attachments: []room.Attachment{card}},
		{From: peer.Address(), To: []string{rec.Name}, Attachments: []room.Attachment{card}},
		{From: rec.Name, To: []string{peer.Address()}, Attachments: []room.Attachment{card}},
		{From: lead.Address(), To: []string{rec.Name}, Text: "No attachments"},
	}
	var ids []string
	for i, input := range inputs {
		if input.Text == "" {
			input.Text = "Media report is ready."
		}
		msg, err := store.Post(rec.ID, input)
		if err != nil {
			t.Fatal(err)
		}
		if i < 4 {
			ids = append(ids, msg.ID)
		}
	}
	store.Now = nil
	feed, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Post{}
	for _, p := range feed.Posts {
		p.Views = nil
		byID[p.ID] = p
	}
	for _, exact := range []bool{false, true} {
		var sess Session
		if exact {
			sess, err = e.svc.TranscriptTurn(lead.ID, 1)
		} else {
			sess, err = e.svc.transcript(lead.ID, 1, 0)
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(sess.Media) != 4 || len(sess.Posts) != 4 {
			t.Fatalf("media = %+v, posts = %+v", sess.Media, sess.Posts)
		}
		for i, p := range sess.Media {
			if !reflect.DeepEqual(p, byID[ids[len(ids)-1-i]]) {
				t.Errorf("media %d differs from feed: %+v", i, p)
			}
		}
		if sess.Media[2].Speaker != "you" || !sess.Media[2].Owner {
			t.Errorf("owner media = %+v", sess.Media[2])
		}
		if sess.Media[0].Attachments[0].Title != "Media report" {
			t.Errorf("card = %+v", sess.Media[0].Attachments)
		}
	}
}

func TestSessionMediaNewest200AndEmpty(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	book := e.svc.avatars(rec.ID)
	empty, err := e.svc.sessionMedia(rec, nil, "", "alpha:lead", book)
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
		msg, err := store.Post(rec.ID, room.PostInput{From: "alpha:lead", To: []string{rec.Name}, Attachments: []room.Attachment{att}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	store.Now = nil
	if _, err := store.Post(rec.ID, room.PostInput{From: "alpha:peer", To: []string{rec.Name}, Attachments: []room.Attachment{att}}); err != nil {
		t.Fatal(err)
	}
	msgs, err := store.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := e.svc.sessionMedia(rec, msgs, "", "alpha:lead", book)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 200 {
		t.Fatalf("media count = %d", len(posts))
	}
	for i, p := range posts {
		if p.ID != ids[i] {
			t.Errorf("media %d = %s, want %s", i, p.ID, ids[i])
		}
	}
}

func TestSessionPostsHoldEveryOwnerPostToTheSession(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	att, err := store.PutAttachment(rec.ID, "notes.txt", []byte("notes"))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []room.PostInput{
		{From: rec.Name, Human: true, To: []string{"alpha:lead"}, Text: "with files", Attachments: []room.Attachment{att}},
		{From: rec.Name, Human: true, To: []string{"alpha:lead"}, Text: "text only"},
		{From: rec.Name, Human: true, To: []string{"alpha:peer"}, Text: "other session", Attachments: []room.Attachment{att}},
	} {
		if _, err := store.Post(rec.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := store.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	posts, err := e.svc.sessionPosts(rec, msgs, "", "alpha:lead", time.Time{}, time.Time{}, e.svc.avatars(rec.ID))
	if err != nil {
		t.Fatal(err)
	}
	// Both owner posts are the stored posts the DM shows: text, files and all.
	if len(posts) != 2 || !posts[0].Owner || posts[0].Text != "with files" || len(posts[0].Attachments) != 1 || posts[1].Text != "text only" {
		t.Fatalf("owner posts: %+v", posts)
	}
}

// The owner's post to @root is addressed to the room, and it belongs to the
// root seat's conversation: root's DM draws the stored post, so an attached
// screenshot shows there.
func TestSessionPostsHoldThePostTheOwnerSentToTheSeat(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	shot, err := store.PutAttachment(rec.ID, "screenshot.png", []byte("\x89PNG\r\n\x1a\n screenshot"))
	if err != nil {
		t.Fatal(err)
	}
	inputs := []room.PostInput{
		{From: rec.Name, Human: true, To: []string{rec.Name}, Via: []string{"@root"}, Text: "What happened here?", Attachments: []room.Attachment{shot}},
		{From: rec.Name, Human: true, To: []string{rec.Name}, Via: []string{"@" + rec.Name + ":root"}, Text: "And now?"},
		{From: "alpha:peer", To: []string{rec.Name}, Text: "A report to the owner"},
	}
	for _, input := range inputs {
		if _, err := store.Post(rec.ID, input); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := store.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	book := e.svc.avatars(rec.ID)
	posts, err := e.svc.sessionPosts(rec, msgs, "", "alpha:root", time.Time{}, time.Time{}, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 2 || posts[0].Text != "What happened here?" || len(posts[0].Attachments) != 1 || posts[0].Attachments[0].ID != shot.ID || posts[1].Text != "And now?" {
		t.Fatalf("seat posts: %+v", posts)
	}
	media, err := e.svc.sessionMedia(rec, msgs, "", "alpha:root", book)
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 1 || media[0].Attachments[0].ID != shot.ID {
		t.Fatalf("seat media: %+v", media)
	}
	// A worker session keeps the posts addressed to it, not the seat's.
	posts, err = e.svc.sessionPosts(rec, msgs, "", "alpha:lead", time.Time{}, time.Time{}, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 0 {
		t.Fatalf("worker posts: %+v", posts)
	}
}

// A turn item names the post that carried it. A post the page window left out
// still belongs to the message, so its attachments reach the DM.
func TestSessionPostsHoldThePostOfATurnItemOutsideThePage(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	store := e.svc.Notify.Rooms
	shot, err := store.PutAttachment(rec.ID, "screenshot.png", []byte("\x89PNG\r\n\x1a\n screenshot"))
	if err != nil {
		t.Fatal(err)
	}
	ask, err := store.Post(rec.ID, room.PostInput{From: rec.Name, Human: true, To: []string{"alpha:lead"}, Text: "Look", Attachments: []room.Attachment{shot}})
	if err != nil {
		t.Fatal(err)
	}
	book := e.svc.avatars(rec.ID)
	// The DM page starts after the post, and only its turn item still names it.
	items := []Item{{Kind: "prompt", PostID: ask.ID, Text: "Look", From: rec.Name + " (human)"}}
	posts, err := e.svc.withItemPosts(rec, nil, "lead", "alpha:lead", []Turn{{N: 1, Items: items}}, book)
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].ID != ask.ID || len(posts[0].Attachments) != 1 {
		t.Fatalf("item posts: %+v", posts)
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
	var ids []string
	for _, delta := range []time.Duration{30 * time.Second, 2*time.Minute - 43*time.Millisecond, 4 * time.Minute} {
		store.Now = func() time.Time { return base.Add(delta) }
		msg, err := store.Post(rec.ID, room.PostInput{From: lead.Address(), To: []string{rec.Name}, Text: "report"})
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

// A typed PR event is a delivered notice the DM draws as a card, so the
// session's conversation includes it. A PR post stored before the typed events
// keeps its raw text and stays out, as it always did.
func TestSessionConversationIncludesTypedPREvents(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	store := e.svc.Notify.Rooms
	event, err := store.PostEventInput(room.PostEventInput{RoomID: rec.ID, To: []string{lead.Address()}, RoomService: true,
		Event: room.PREvent{Type: room.PRCIFailed, Label: "PR #7", Location: "github.com/o/r#7", ChangeID: "github.com/o/r#7", Number: 7,
			Head: strings.Repeat("a", 40), CIAttempt: 1, CIMax: 3, Summary: "unit"}})
	if err != nil {
		t.Fatal(err)
	}
	// Old history keeps PR posts with only text and a notice kind.
	legacy := room.Message{Kind: "pr", From: rec.Name, To: []string{lead.Address()}, Notice: room.EventNotice{Kind: "ready"}, Text: "PR #7 is ready to land.",
		SessionIDs: map[string]string{lead.Address(): lead.ID}}
	if sessionPost(rec, legacy, lead.ID, lead.Address()) {
		t.Fatal("a stored PR post without a typed payload reached the session conversation")
	}
	sess, err := e.svc.transcript(lead.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, p := range sess.Posts {
		ids[p.ID] = true
	}
	if !ids[event.ID] {
		t.Fatalf("typed PR event is missing from the session conversation: %+v", sess.Posts)
	}
}
