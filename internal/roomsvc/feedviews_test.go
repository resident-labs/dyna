package roomsvc

import (
	"encoding/json"
	"fmt"
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
			// A stored post carries the home the store decided for it.
			got := slices.Sorted(slices.Values(views.viewsFor(tc.Post, tc.Post.Conversation.Home())))
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
	completed, err := e.svc.rooms().PostEventInput(room.PostEventInput{RoomID: rec.ID, To: []string{rec.Name},
		Event: room.TurnEvent{Session: grandchild.Address(), SessionID: grandchild.ID, Turn: 1, State: room.TurnCompleted, Summary: "finished"}})
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

// A browser tab open across an engine update keeps the bundle it was built
// with, and a reset resyncs data without reloading the page. A new engine must
// still answer the field names that bundle reads, or team posts and voice
// lines appear in the room chat and the sidebar loses its counts.
func TestFeedAnswersTheFieldsOfAnOldBundle(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	e.start(rec, "child", &lead)
	e.say(rec, "child", "to the owner")
	page, err := e.svc.FeedTail(rec.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(page)
	if err != nil {
		t.Fatal(err)
	}
	var old struct {
		Posts []struct {
			Channels []string `json:"channels"`
		} `json:"posts"`
		ChannelUnread map[string]int `json:"channelUnread"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		t.Fatal(err)
	}
	last := page.Posts[len(page.Posts)-1]
	if last.Views[0] != "channel:"+lead.ID {
		t.Fatalf("the newest post shows in %v, want the lead view first", last.Views)
	}
	if got := old.Posts[len(old.Posts)-1].Channels; !reflect.DeepEqual(got, last.Views) {
		t.Fatalf("old bundle channels = %v, want %v", got, last.Views)
	}
	if !reflect.DeepEqual(old.ChannelUnread, page.ViewUnread) || old.ChannelUnread["channel:"+lead.ID] == 0 {
		t.Fatalf("old bundle counts = %v, want %v", old.ChannelUnread, page.ViewUnread)
	}
}

// Room chat lists the room's own posts only. A post written in a DM or in the
// owner's voice call has that conversation as its home, so it is not in the room
// chat and not unread there; its own DM or call lists it.
func TestRoomChatListsOnlyTheRoomsOwnPosts(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	post := func(in room.PostInput) room.Message {
		t.Helper()
		m, err := e.svc.rooms().Post(rec.ID, in)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	dm := post(room.PostInput{From: lead.Address(), To: []string{rec.Name}, Conversation: room.DMConversation(lead.ID), Text: "in the lead's DM"})
	call := post(room.PostInput{From: rec.Name + ":" + room.VoiceAgent, To: []string{rec.Name}, Kind: "voice", Source: room.SourceVoice, Spoken: true, Text: "in the call"})
	here := e.say(rec, "lead", "in the room chat")

	page, err := e.svc.FeedTail(rec.ID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]Post{}
	for _, p := range page.Posts {
		listed[p.ID] = p
	}
	if _, ok := listed[dm.ID]; ok {
		t.Fatalf("Room chat lists the post written in the lead's DM: %+v", listed[dm.ID])
	}
	if views := listed[call.ID].Views; slices.Contains(views, "general") {
		t.Fatalf("Room chat lists the voice call line in %v", views)
	}
	if listed[call.ID].Unread {
		t.Fatal("the voice call line is unread in Room chat")
	}
	if got := listed[here.ID].Views; !slices.Contains(got, "general") || !listed[here.ID].Unread {
		t.Fatalf("the room's own post shows in %v, unread %v; want it unread in Room chat", got, listed[here.ID].Unread)
	}
	want := map[string]int{"general": 1}
	if page.Unread != 1 || !reflect.DeepEqual(page.ViewUnread, want) {
		t.Fatalf("Room chat unread = %d in %v, want 1 in %v", page.Unread, page.ViewUnread, want)
	}
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 1 {
		t.Fatalf("room rail unread = %d, want 1", got)
	}
}

// A full page of DM posts between two room posts must not stall paging or empty
// a page: the feed pages over the room's own posts, whatever the log holds
// between them.
func TestRoomChatPagesPastAFullPageOfDMPosts(t *testing.T) {
	e := newEnv(t)
	rec := e.room("alpha")
	lead := e.start(rec, "lead", nil)
	first := e.say(rec, "lead", "before the DMs")
	var dm room.Message
	for i := range 60 {
		m, err := e.svc.rooms().Post(rec.ID, room.PostInput{From: lead.Address(), To: []string{rec.Name}, Conversation: room.DMConversation(lead.ID), Text: fmt.Sprintf("dm %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		dm = m
	}
	last := e.say(rec, "lead", "after the DMs")
	// A DM post after every room post is a cursor too, and it is not listed.
	afterLast, err := e.svc.rooms().Post(rec.ID, room.PostInput{From: lead.Address(), To: []string{rec.Name}, Conversation: room.DMConversation(lead.ID), Text: "dm after the room"})
	if err != nil {
		t.Fatal(err)
	}

	ids := func(page Feed) []string {
		out := make([]string, 0, len(page.Posts))
		for _, p := range page.Posts {
			out = append(out, p.ID)
		}
		return out
	}
	tail, err := e.svc.FeedTail(rec.ID, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(tail); !reflect.DeepEqual(got, []string{last.ID}) || tail.Earlier != 1 || tail.Newer != 0 {
		t.Fatalf("tail page = %v, earlier %d, newer %d; want the newest room post only", got, tail.Earlier, tail.Newer)
	}
	forward, err := e.svc.FeedAfter(rec.ID, first.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(forward); !reflect.DeepEqual(got, []string{last.ID}) || forward.Newer != 0 {
		t.Fatalf("forward page = %v, newer %d; want the next room post", got, forward.Newer)
	}
	older, err := e.svc.FeedTail(rec.ID, last.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(older); !reflect.DeepEqual(got, []string{first.ID}) || older.Earlier != 0 || older.Newer != 1 {
		t.Fatalf("older page = %v, earlier %d, newer %d; want the older room post", got, older.Earlier, older.Newer)
	}
	// A jump to a post of the DM lands on the room posts around it.
	around, err := e.svc.FeedAround(rec.ID, dm.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(around); !reflect.DeepEqual(got, []string{first.ID, last.ID}) {
		t.Fatalf("page around a DM post = %v, want the room's posts", got)
	}
	// A DM cursor must still find the next room post, and a DM cursor after the
	// last room post ends the page instead of skipping it.
	between, err := e.svc.FeedAfter(rec.ID, dm.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(between); !reflect.DeepEqual(got, []string{last.ID}) || between.Newer != 0 {
		t.Fatalf("page after a DM post = %v, newer %d; want the next room post", got, between.Newer)
	}
	tailAfter, err := e.svc.FeedAfter(rec.ID, afterLast.ID, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(tailAfter); len(got) != 0 || tailAfter.Newer != 0 {
		t.Fatalf("page after the last DM post = %v, newer %d; want none", got, tailAfter.Newer)
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
