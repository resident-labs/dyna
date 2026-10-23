package roomsvc

import (
	"bytes"
	"dyna-agent/internal/room"
	"dyna-agent/internal/subagent"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFeedAroundFarPinUsesOneBoundedRead(t *testing.T) {
	e := newEnv(t)
	rec := e.room("far-pin-window")
	var log bytes.Buffer
	enc := json.NewEncoder(&log)
	for i := 0; i < 1051; i++ {
		if err := enc.Encode(room.Message{ID: fmt.Sprintf("msg_%05d", i), TS: int64(i), From: rec.Name + ":lead", To: []string{rec.Name}, Text: "retained"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.svc.rooms().Root, rec.ID, "messages.jsonl"), log.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.rooms().Pin(rec.ID, "msg_00050", rec.Name); err != nil {
		t.Fatal(err)
	}
	// One request finds a pin 1,000 posts behind the newest post.
	page, err := e.svc.FeedAround(rec.ID, "msg_00050", 50)
	if err != nil || len(page.Posts) != 50 || page.Posts[25].ID != "msg_00050" || !page.Posts[25].Pinned || page.Earlier != 25 || page.Newer != 976 {
		t.Fatalf("far pin page: %+v %v", page, err)
	}
	for _, id := range []string{"msg_00000", "msg_01050"} {
		edge, err := e.svc.FeedAround(rec.ID, id, 50)
		if err != nil || len(edge.Posts) != 50 {
			t.Fatalf("edge %s: %+v %v", id, edge, err)
		}
	}
	forward, err := e.svc.FeedAfter(rec.ID, page.Posts[49].ID, 50)
	if err != nil || len(forward.Posts) != 50 || forward.Posts[0].ID != "msg_00075" {
		t.Fatalf("forward page: %+v %v", forward, err)
	}
	for _, tc := range []struct{ limit, want int }{{0, 50}, {-1, 50}, {1000, 200}} {
		capped, err := e.svc.FeedAround(rec.ID, "msg_00050", tc.limit)
		if err != nil || len(capped.Posts) != tc.want {
			t.Fatalf("around limit %d: %d posts %v", tc.limit, len(capped.Posts), err)
		}
	}
	if _, err := e.svc.FeedAround(rec.ID, "", 50); err == nil {
		t.Fatal("accepted missing around target")
	}
	if _, err := e.svc.FeedAfter(rec.ID, "missing", 50); err == nil {
		t.Fatal("accepted unknown forward target")
	}
	if _, err := e.svc.FeedAround(rec.ID, "missing", 50); err == nil {
		t.Fatal("accepted unknown pin")
	}
}

func TestSearchFeedFindsPostsOutsideTheLoadedWindow(t *testing.T) {
	e := newEnv(t)
	rec := e.room("feed-search")
	var log bytes.Buffer
	enc := json.NewEncoder(&log)
	for i := 0; i < 2000; i++ {
		text := "routine"
		if i == 7 || i == 1500 {
			text = fmt.Sprintf("Needle number %d", i)
		}
		if err := enc.Encode(room.Message{ID: fmt.Sprintf("msg_%05d", i), TS: int64(i), From: rec.Name + ":lead", To: []string{rec.Name}, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.svc.rooms().Root, rec.ID, "messages.jsonl"), log.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	// The newest page holds 50 posts; the search reaches the oldest one too.
	found, err := e.svc.SearchFeed(rec.Name, "needle", 0)
	if err != nil || found.More || len(found.Posts) != 2 || found.Posts[0].ID != "msg_01500" || found.Posts[1].ID != "msg_00007" {
		t.Fatalf("search: %+v %v", found, err)
	}
	// A row shows the speaker and the stored text the search matched.
	if p := found.Posts[1]; p.Speaker != "lead" || p.Text != "Needle number 7" {
		t.Fatalf("a result lacks the facts its row shows: %+v", p)
	}
	if one, err := e.svc.SearchFeed(rec.ID, "needle", 1); err != nil || !one.More || len(one.Posts) != 1 {
		t.Fatalf("limited search: %+v %v", one, err)
	}
	if none, err := e.svc.SearchFeed(rec.ID, "absent", 0); err != nil || none.Posts == nil || len(none.Posts) != 0 {
		t.Fatalf("no match should be an empty list: %+v %v", none, err)
	}
	if _, err := e.svc.SearchFeed(rec.ID, "", 0); err == nil {
		t.Fatal("empty search was accepted")
	}
}

func TestTranscriptForwardPageIncludesLiveArrivals(t *testing.T) {
	e := newEnv(t)
	rec := e.room("forward-turns")
	lead := e.start(rec, "lead", nil)
	base := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	write := func(n int) {
		t.Helper()
		data, err := json.Marshal(subagent.Turn{N: n, Status: subagent.TurnOK, Prompt: fmt.Sprint(n), Started: base.Add(time.Duration(n) * time.Minute), Ended: base.Add(time.Duration(n)*time.Minute + 30*time.Second)})
		if err != nil {
			t.Fatal(err)
		}
		seedTurnRow(t, e.mgr.Store, lead.ID, n, data)
		if _, err := e.mgr.Store.UpdateMeta(lead.ID, func(m *subagent.Meta) { m.Turns = n }); err != nil {
			t.Fatal(err)
		}
		e.svc.Notify.Rooms.Now = func() time.Time { return base.Add(time.Duration(n)*time.Minute + 10*time.Second) }
		if _, err := e.svc.Notify.Rooms.Post(rec.ID, room.PostInput{From: lead.Address(), To: []string{rec.Name}, Text: fmt.Sprint(n)}); err != nil {
			t.Fatal(err)
		}
	}
	defer func() { e.svc.Notify.Rooms.Now = nil }()
	for n := 1; n <= 8; n++ {
		write(n)
	}
	older, err := e.svc.TranscriptPage(lead.ID, "6", 3)
	if err != nil {
		t.Fatal(err)
	}
	write(9)
	page, err := e.svc.TranscriptAfter(lead.ID, fmt.Sprint(older.Turns[2].N), 3)
	if err != nil || len(page.Turns) != 3 || page.Turns[0].N != 6 || page.Turns[2].N != 8 || !page.Earlier || !page.Newer || len(page.Posts) != 3 || page.Posts[0].Text != "6" {
		t.Fatalf("forward turn page: %+v %v", page, err)
	}
	tail, err := e.svc.TranscriptAfter(lead.ID, "8", 3)
	if err != nil || len(tail.Turns) != 1 || tail.Turns[0].N != 9 || tail.Newer || len(tail.Posts) != 1 {
		t.Fatalf("live turn: %+v %v", tail, err)
	}
	empty, err := e.svc.TranscriptAfter(lead.ID, "9", 3)
	if err != nil || len(empty.Turns) != 0 || len(empty.Posts) != 0 || empty.Newer {
		t.Fatalf("after final turn: %+v %v", empty, err)
	}
	for _, cursor := range []string{"bad", "-1", "0", "10"} {
		if _, err := e.svc.TranscriptAfter(lead.ID, cursor, 3); err == nil {
			t.Fatalf("accepted turn cursor %s", cursor)
		}
	}
}
