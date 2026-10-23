package roomsvc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"dyna-agent/internal/listpage"
	"dyna-agent/internal/room"
)

// say posts from a member to the room owner, so the post counts as unread.
func (e *env) say(rec room.Record, from, text string) room.Message {
	e.t.Helper()
	m, err := e.svc.rooms().Post(rec.ID, room.PostInput{From: rec.Name + ":" + from, To: []string{rec.Name}, Text: text})
	if err != nil {
		e.t.Fatal(err)
	}
	return m
}

func TestOpenMatchesTheSeparateCalls(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	e.start(rec, "decoder", &lead)
	var first room.Message
	for i := 0; i < 6; i++ {
		m := e.say(rec, "lead", fmt.Sprintf("note %d", i))
		if i == 0 {
			first = m
		}
	}
	if err := e.svc.Pin("alpha", first.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.svc.MarkReadMany(rec.ID, []string{first.ID}); err != nil {
		t.Fatal(err)
	}

	got, err := e.svc.Open("alpha", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	members, _ := e.svc.Members("alpha", false)
	choices, _ := e.svc.MentionChoices("alpha")
	pins, _ := e.svc.Pins("alpha")
	runs, _ := e.svc.Runs("alpha")
	if !reflect.DeepEqual(got.Members, members) || !reflect.DeepEqual(got.Choices, choices) || !reflect.DeepEqual(got.Pins, pins) || !reflect.DeepEqual(got.Runs, runs) {
		t.Fatalf("Open differs from the separate calls:\n%+v\nmembers %+v\nchoices %v\npins %+v", got, members, choices, pins)
	}
	// Unread history stays available without expanding the requested tail.
	full, _ := e.svc.FeedTail("alpha", "", 0)
	if full.Earlier != 0 {
		t.Fatalf("Feed is the whole log, Earlier = %d", full.Earlier)
	}
	if want := len(full.Posts) - 0; got.Feed.Earlier+len(got.Feed.Posts) != want {
		t.Fatalf("earlier %d + posts %d != %d", got.Feed.Earlier, len(got.Feed.Posts), want)
	}
	if len(got.Feed.Posts) != 3 || got.Feed.Unread != 5 {
		t.Fatalf("tail or unread count: %+v", got.Feed)
	}
	if !got.Feed.Posts[0].Unread {
		t.Fatalf("the tail must retain unread marks: %+v", got.Feed.Posts[0])
	}
}

func TestFeedTailCountsEarlierPosts(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	var ids []string
	for i := 0; i < 8; i++ {
		ids = append(ids, e.say(rec, "lead", fmt.Sprintf("note %d", i)).ID)
	}
	if err := e.svc.MarkReadMany(rec.ID, ids); err != nil {
		t.Fatal(err)
	}
	f, err := e.svc.FeedTail("alpha", "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if f.Earlier != len(ids)-3 || len(f.Posts) != 3 || f.Posts[2].ID != ids[7] {
		t.Fatalf("tail = earlier %d, %d posts", f.Earlier, len(f.Posts))
	}
	if f, _ := e.svc.FeedTail("alpha", "", 0); f.Earlier != 0 || len(f.Posts) != len(ids) {
		t.Fatalf("default tail must include this short log, got earlier %d, %d posts", f.Earlier, len(f.Posts))
	}
}

func TestRoomsUnreadFollowsNewPostsAndReads(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	unread := func() int {
		t.Helper()
		rooms, err := e.svc.Rooms()
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rooms {
			if r.ID == rec.ID {
				return r.Unread
			}
		}
		t.Fatal("room missing")
		return 0
	}
	if n := unread(); n != 0 {
		t.Fatalf("unread = %d, want 0", n)
	}
	a := e.say(rec, "lead", "one")
	e.say(rec, "lead", "two")
	if n := unread(); n != 2 {
		t.Fatalf("unread = %d, want 2", n)
	}
	e.say(rec, "lead", "three") // appended after the first count was cached
	if n := unread(); n != 3 {
		t.Fatalf("unread = %d, want 3 after an append", n)
	}
	if err := e.svc.MarkRead(rec.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if n := unread(); n != 2 {
		t.Fatalf("unread = %d, want 2 after a read", n)
	}
}

func TestSessionInfoFollowsChangesThroughTheCache(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	state := func() string {
		t.Helper()
		ms, err := e.svc.Members("alpha", true)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.ID == lead.ID {
				return fmt.Sprintf("%s/%d/%v", m.State, m.Turns, m.Closed)
			}
		}
		t.Fatal("member missing")
		return ""
	}
	if got := state(); got != "done/1/false" {
		t.Fatalf("after turn 1: %s", got)
	}
	if _, err := e.svc.Post(e.ctx, "alpha", "@lead RESPOND: again", ""); err != nil {
		t.Fatal(err)
	}
	e.wait(lead.ID, 2)
	if got := state(); got != "done/2/false" {
		t.Fatalf("after turn 2: %s", got)
	}
	e.doneGoal(lead)
	if _, err := e.svc.ForceCloseSession(e.ctx, "alpha", lead.ID); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != "closed/2/true" {
		t.Fatalf("after close: %s", got)
	}
}

func TestShellResultsNeverCountAsUnread(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	if _, err := e.svc.rooms().Post(rec.ID, room.PostInput{From: "shell:sh_20261017-120000-0000000a", To: []string{rec.Name}, Kind: "failed", Text: "shell:sh_20261017-120000-0000000a failed with exit code 1."}); err != nil {
		t.Fatal(err)
	}
	e.say(rec, "lead", "a real post")
	rooms, err := e.svc.Rooms()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rooms {
		if r.ID == rec.ID && r.Unread != 1 {
			t.Fatalf("unread = %d, want 1: a shell result must not count", r.Unread)
		}
	}
	f, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Posts) != 2 || f.Posts[0].Unread || !f.Posts[1].Unread {
		t.Fatalf("feed = %+v, want the shell result read and the post unread", f.Posts)
	}
}

func TestUnreadHistoryDoesNotExpandRequestedTail(t *testing.T) {
	e := newEnv(t)
	rec := e.room("unread-tail")
	for i := 0; i < 8; i++ {
		e.say(rec, "lead", fmt.Sprintf("unread note %d", i))
	}
	feed, err := e.svc.FeedTail(rec.ID, "", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed.Posts) != 3 || feed.Earlier != 5 {
		t.Fatalf("tail grew to unread history: %d posts, %d earlier", len(feed.Posts), feed.Earlier)
	}
	opened, err := e.svc.Open(rec.ID, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Feed.Posts) != 3 || opened.Feed.Earlier != 5 {
		t.Fatalf("Open tail grew: %d posts, %d earlier", len(opened.Feed.Posts), opened.Feed.Earlier)
	}
}

func TestFeedDefaultsMaximumAndCursor(t *testing.T) {
	e := newEnv(t)
	rec := e.room("page-history")
	var log bytes.Buffer
	encoder := json.NewEncoder(&log)
	for i := 0; i < 205; i++ {
		if err := encoder.Encode(room.Message{ID: fmt.Sprintf("msg_%05d", i), TS: int64(i), From: rec.Name + ":lead", To: []string{rec.Name}, Text: "Retained history"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(e.svc.rooms().Root, rec.ID, "messages.jsonl"), log.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := e.svc.Open(rec.ID, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Feed.Posts) != listpage.DefaultLimit || first.Feed.Earlier != 155 || first.Feed.Unread != 205 || first.Feed.FirstUnread != "msg_00000" {
		t.Fatalf("default bounded tail: %+v", first.Feed)
	}
	capped, err := e.svc.FeedTail(rec.ID, "", 1000)
	if err != nil || len(capped.Posts) != listpage.MaxLimit || capped.Earlier != 5 {
		t.Fatalf("maximum: %d posts, %d earlier, %v", len(capped.Posts), capped.Earlier, err)
	}
	defaultTail, err := e.svc.FeedTail(rec.ID, "", 0)
	if err != nil || len(defaultTail.Posts) != listpage.DefaultLimit {
		t.Fatalf("FeedTail default: %d posts, %v", len(defaultTail.Posts), err)
	}
	maximumOpen, err := e.svc.Open(rec.ID, 1000, false)
	if err != nil || len(maximumOpen.Feed.Posts) != listpage.MaxLimit {
		t.Fatalf("Open maximum: %d posts, %v", len(maximumOpen.Feed.Posts), err)
	}
	// A client trim moves the oldest retained post without a server request.
	trimmed, err := e.svc.FeedTail(rec.ID, first.Feed.Posts[25].ID, 50)
	if err != nil || len(trimmed.Posts) != 50 || trimmed.Posts[49].ID != "msg_00179" {
		t.Fatalf("trimmed history boundary: %+v %v", trimmed, err)
	}
	e.say(rec, "lead", "Appended after the first page")
	seen := map[string]bool{}
	for _, post := range first.Feed.Posts {
		seen[post.ID] = true
	}
	page := first.Feed
	for page.Earlier > 0 {
		next, err := e.svc.FeedTail(rec.ID, page.Cursor, 50)
		if err != nil {
			t.Fatal(err)
		}
		if next.Earlier >= page.Earlier || len(next.Posts) > 50 {
			t.Fatalf("cursor did not advance: %+v", next)
		}
		for _, post := range next.Posts {
			if seen[post.ID] {
				t.Fatalf("duplicate post %s", post.ID)
			}
			seen[post.ID] = true
		}
		page = next
	}
	if len(seen) != 205 || !seen["msg_00000"] || page.Cursor != "" {
		t.Fatalf("history incomplete: %d posts, cursor %q", len(seen), page.Cursor)
	}
	for _, cursor := range []string{"invalid!", "missing"} {
		if _, err := e.svc.FeedTail(rec.ID, cursor, 50); err == nil {
			t.Fatalf("accepted invalid cursor %q", cursor)
		}
	}
}
