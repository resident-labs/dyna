package roomsvc

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"dyna-agent/internal/room"
)

func TestFeedViewRouting(t *testing.T) {
	data, err := os.ReadFile("testdata/feedviews.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Members []Member `json:"members"`
		Cases   []struct {
			Name  string
			Post  room.Message
			Views []string
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	views := newFeedViews(fixture.Members)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := slices.Sorted(slices.Values(views.viewsFor(tc.Post)))
			if !reflect.DeepEqual(got, tc.Views) {
				t.Fatalf("views = %v, want %v", got, tc.Views)
			}
		})
	}
}

func TestFeedViewUnreadUsesWholeSharedIndex(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	child := e.start(rec, "child", &lead)
	grandchild := e.start(rec, "grandchild", &child)
	first := e.say(rec, "child", "already read")
	e.say(rec, "child", "older than the page")
	if err := e.svc.MarkReadMany(rec.ID, []string{first.ID}); err != nil {
		t.Fatal(err)
	}
	completed, err := e.svc.rooms().Post(rec.ID, room.PostInput{From: grandchild.Address(), To: []string{rec.Name}, Kind: "completed", Text: "finished"})
	if err != nil {
		t.Fatal(err)
	}
	e.say(rec, "lead", "general")
	// Warm the same index through the room unread call before requesting a page.
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 3 {
		t.Fatalf("room unread = %d", got)
	}
	page, err := e.svc.FeedTail(rec.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"general": 2, "channel:" + lead.ID: 1, "channel:" + child.ID: 1}
	if page.Unread != 3 || len(page.Posts) != 1 || !reflect.DeepEqual(page.ViewUnread, want) {
		t.Fatalf("feed unread = %d, posts = %d, views = %v, want %v", page.Unread, len(page.Posts), page.ViewUnread, want)
	}
	if err := e.svc.MarkReadMany(rec.ID, []string{completed.ID}); err != nil {
		t.Fatal(err)
	}
	page, err = e.svc.FeedTail(rec.ID, "", 1)
	delete(want, "channel:"+child.ID)
	if err != nil || page.Unread != 2 || !reflect.DeepEqual(page.ViewUnread, want) {
		t.Fatalf("after read: %+v, %v", page, err)
	}
}

// Feed posts carry their feed views, the sender's home first, so the web
// needs no copy of the routing. Live posts carry them too.
func TestFeedPostsCarryViews(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	e.start(rec, "child", &lead)
	e.say(rec, "child", "to the owner")
	page, err := e.svc.FeedTail(rec.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"channel:" + lead.ID, "general"}
	last := page.Posts[len(page.Posts)-1]
	if !reflect.DeepEqual(last.Views, want) {
		t.Fatalf("feed views = %v, want %v", last.Views, want)
	}
	live, _, err := e.svc.FeedSince(rec.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := live[len(live)-1].Views; !reflect.DeepEqual(got, want) {
		t.Fatalf("live views = %v, want %v", got, want)
	}
}

// Voice's lines to the owner are heard live in the call and never shown in a
// feed view, so they never count as unread: not in the room, not in the feed.
func TestVoiceCallLinesAreNeverUnread(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	voice := func(text string) room.Message {
		m, err := e.svc.rooms().Post(rec.ID, room.PostInput{From: rec.Name + ":" + room.VoiceAgent, To: []string{rec.Name}, Kind: "voice", Source: room.SourceVoice, Spoken: true, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	voice("an older call line")
	e.say(rec, "lead", "a real post")
	voice("a voice reply")
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 1 {
		t.Fatalf("room unread = %d, want 1 (only the real post)", got)
	}
	page, err := e.svc.FeedTail(rec.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Unread != 1 || page.ViewUnread[VoiceChannel] != 0 {
		t.Fatalf("feed unread = %d, views = %v; want 1 and no voice count", page.Unread, page.ViewUnread)
	}
	for _, p := range page.Posts {
		if p.Source == room.SourceVoice && p.Unread {
			t.Fatalf("voice post %s is marked unread", p.ID)
		}
	}
}
