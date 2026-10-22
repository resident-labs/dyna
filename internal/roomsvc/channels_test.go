package roomsvc

import (
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"testing"

	"dyna-agent/internal/room"
)

func TestChannelRouting(t *testing.T) {
	data, err := os.ReadFile("testdata/channels.json")
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
	channels := newChannelIndex(fixture.Members)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := slices.Sorted(slices.Values(channels.views(tc.Post)))
			if !reflect.DeepEqual(got, tc.Views) {
				t.Fatalf("views = %v, want %v", got, tc.Views)
			}
		})
	}
}

func TestFeedChannelUnreadUsesWholeSharedIndex(t *testing.T) {
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
	if page.Unread != 3 || len(page.Posts) != 1 || !reflect.DeepEqual(page.ChannelUnread, want) {
		t.Fatalf("feed unread = %d, posts = %d, channels = %v, want %v", page.Unread, len(page.Posts), page.ChannelUnread, want)
	}
	if err := e.svc.MarkReadMany(rec.ID, []string{completed.ID}); err != nil {
		t.Fatal(err)
	}
	page, err = e.svc.FeedTail(rec.ID, "", 1)
	delete(want, "channel:"+child.ID)
	if err != nil || page.Unread != 2 || !reflect.DeepEqual(page.ChannelUnread, want) {
		t.Fatalf("after read: %+v, %v", page, err)
	}
}

// Feed posts carry their channel views, the sender's home first, so the web
// needs no copy of the routing. Live posts carry them too.
func TestFeedPostsCarryChannels(t *testing.T) {
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
	if !reflect.DeepEqual(last.Channels, want) {
		t.Fatalf("feed channels = %v, want %v", last.Channels, want)
	}
	live, _, err := e.svc.FeedSince(rec.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := live[len(live)-1].Channels; !reflect.DeepEqual(got, want) {
		t.Fatalf("live channels = %v, want %v", got, want)
	}
}

// Voice's lines to the owner are heard live in the call and never shown in a
// channel, so they never count as unread: not in the room, not in the feed.
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
	if page.Unread != 1 || page.ChannelUnread[VoiceChannel] != 0 {
		t.Fatalf("feed unread = %d, channels = %v; want 1 and no voice count", page.Unread, page.ChannelUnread)
	}
	for _, p := range page.Posts {
		if p.Source == room.SourceVoice && p.Unread {
			t.Fatalf("voice post %s is marked unread", p.ID)
		}
	}
}
