package roomsvc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
		{From: lead.Address(), Kind: "pr", To: []string{rec.Name}, Text: "Own PR"},
		{From: peer.Address(), Kind: "pr", To: []string{lead.Address()}, Text: "Incoming PR"},
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
		if i < 4 {
			ids = append(ids, msg.ID)
		}
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
		// Channels route the room feed only; transcripts have none.
		post.Channels = nil
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
		p.Channels = nil
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

func TestSessionPostsOwnerAttachments(t *testing.T) {
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
	if len(posts) != 1 || !posts[0].Owner || len(posts[0].Attachments) != 1 {
		t.Fatalf("owner posts: %+v", posts)
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
	dir, err := e.mgr.Store.Dir(lead.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := subagent.Turn{N: 2, Status: subagent.TurnOK, Started: base.Add(2*time.Minute + 72*time.Millisecond), Ended: base.Add(3 * time.Minute)}
	data, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turns", "2.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
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
