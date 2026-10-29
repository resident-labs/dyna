package room

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	ownerSession = "ses_20261023-120000-aaaaaaaa"
	otherSession = "ses_20261023-120001-bbbbbbbb"
)

// The writer's own conversation stands as written: the DM composer says where
// the post was written, and the store does not guess from the mentions.
func TestHomeForKeepsTheWriterConversation(t *testing.T) {
	msg := Message{From: "alpha", Human: true, To: []string{"alpha:lead"}, Conversation: DMConversation(ownerSession)}
	if got := HomeFor(msg, "alpha", ""); got != DMConversation(ownerSession) {
		t.Fatalf("home = %q, want the DM the writer named", got)
	}
	msg = Message{From: "alpha:lead", To: []string{"alpha"}, Conversation: ConversationVoice}
	if got := HomeFor(msg, "alpha", ""); got != ConversationVoice {
		t.Fatalf("home = %q, want voice", got)
	}
}

// A post of the owner's voice call is in the voice conversation, whatever it
// mentions.
func TestHomeForVoiceCall(t *testing.T) {
	msg := Message{From: "alpha:voice-call", Source: SourceVoice, To: []string{"alpha:lead"}, Wake: true}
	if got := HomeFor(msg, "alpha", ""); got != ConversationVoice {
		t.Fatalf("home = %q, want voice", got)
	}
	legacy := Message{From: "dyna", Kind: "voice", To: []string{"alpha"}}
	if got := HomeFor(legacy, "alpha", ""); got != ConversationVoice {
		t.Fatalf("legacy voice home = %q, want voice", got)
	}
}

// The mentions never decide a home: an owner post to exactly one session stays
// in the room feed, bound or not, and that session's DM shows a reference row
// for it. Only the writer's client names a DM.
func TestHomeForOwnerPostToASessionStaysInTheFeed(t *testing.T) {
	bound := map[string]string{"alpha:lead": ownerSession}
	for name, msg := range map[string]Message{
		"one session":    {From: "alpha", Human: true, To: []string{"alpha:lead"}, SessionIDs: bound},
		"unbound":        {From: "alpha", Human: true, To: []string{"alpha:lead"}},
		"two recipients": {From: "alpha", Human: true, To: []string{"alpha:lead", "alpha:root"}, SessionIDs: bound},
		"root post":      {From: "alpha", Human: false, To: []string{"alpha:lead"}, SessionIDs: bound},
		"session author": {From: "alpha:lead", To: []string{"alpha:root"}, SessionIDs: bound},
	} {
		if got := HomeFor(msg, "alpha", ""); got != ConversationRoom {
			t.Fatalf("%s: home = %q, want the room feed", name, got)
		}
	}
}

// A typed event whose audience is one session belongs to that session's DM, so
// a parent's check-in does not land in the room feed. An event that also
// reaches anything else, such as the room, stays in the feed.
func TestHomeForOneSessionEvent(t *testing.T) {
	checkin := Message{From: EngineSender, Kind: string(FamilyCheckin), Notice: EventNotice{Kind: string(TypeSummary)},
		To: []string{"alpha:lead"}, SessionIDs: map[string]string{"alpha:lead": ownerSession}}
	if got := HomeFor(checkin, "alpha", ""); got != DMConversation(ownerSession) {
		t.Fatalf("check-in home = %q, want dm:%s", got, ownerSession)
	}
	checkin.To = []string{"alpha:lead", "alpha"}
	if got := HomeFor(checkin, "alpha", ""); got != ConversationRoom {
		t.Fatalf("a check-in to the room too has home %q, want the room feed", got)
	}
	// An event of the room's own audience keeps the feed even when one session
	// receives it.
	roomEvent := Message{From: EngineSender, Kind: string(FamilyPR), Notice: EventNotice{Kind: string(PRReady)},
		To: []string{"alpha:lead"}, SessionIDs: map[string]string{"alpha:lead": ownerSession}}
	if got := HomeFor(roomEvent, "alpha", ""); got != ConversationRoom {
		t.Fatalf("room event home = %q, want the room feed", got)
	}
	// A check-in for the root seat is the seat's DM, not the DM of the session
	// that holds the seat: every root view reads the seat's conversation.
	const roomID = "room_20261023-120000-abcdef12"
	rootCheckin := Message{From: EngineSender, Kind: string(FamilyCheckin), Notice: EventNotice{Kind: string(TypeSummary)}, RoomID: roomID,
		To: []string{"alpha:root"}, SessionIDs: map[string]string{"alpha:root": ownerSession}}
	if got := HomeFor(rootCheckin, "alpha", ""); got != SeatDMConversation(roomID) {
		t.Fatalf("root check-in home = %q, want %q", got, SeatDMConversation(roomID))
	}
}

// The owner's DM with the root seat names the seat, so it keeps one
// conversation when the holder changes.
func TestSeatDMConversation(t *testing.T) {
	const roomID = "room_20261023-120000-abcdef12"
	seat := SeatDMConversation(roomID)
	if got, want := string(seat), "dm:seat:"+roomID; got != want {
		t.Fatalf("seat DM = %q, want %q", got, want)
	}
	if err := seat.Validate(); err != nil {
		t.Fatalf("%q: %v", string(seat), err)
	}
	// The seat is the delivery identity of the same DM.
	if got, want := seat.SessionID(), SeatSessionID(roomID); got != want {
		t.Fatalf("delivery target = %q, want %q", got, want)
	}
	if HomeFor(Message{From: "alpha", Human: true, Conversation: seat}, "alpha", "") != seat {
		t.Fatal("a post the writer placed in the seat's DM was not stored there")
	}
}

// A session that answers a post in its own DM stays in that DM; its other
// posts go to the room feed.
func TestHomeForSessionAnsweringItsOwnDM(t *testing.T) {
	own := map[string]string{"alpha:lead": ownerSession}
	msg := Message{From: "alpha:lead", To: []string{"alpha"}, SessionIDs: own, ReplyTo: "msg_parent"}
	if got := HomeFor(msg, "alpha", DMConversation(ownerSession)); got != DMConversation(ownerSession) {
		t.Fatalf("home = %q, want dm:%s", got, ownerSession)
	}
	if got := HomeFor(msg, "alpha", ConversationRoom); got != ConversationRoom {
		t.Fatalf("home = %q, want the room feed", got)
	}
	// A reply in another session's DM is not this session's home.
	if got := HomeFor(msg, "alpha", DMConversation(otherSession)); got != ConversationRoom {
		t.Fatalf("home = %q, want the room feed", got)
	}
}

// Everything else, including a post from before the field existed, is the
// room feed.
func TestHomeForDefaultsToTheRoom(t *testing.T) {
	if got := HomeFor(Message{From: "dyna", Kind: "notice", To: []string{"alpha"}}, "alpha", ""); got != ConversationRoom {
		t.Fatalf("home = %q, want the room feed", got)
	}
	if got := (Conversation("")).Home(); got != ConversationRoom {
		t.Fatalf("empty home = %q, want the room feed", got)
	}
}

// OtherHomesOf reads the homes of the posts it names, not every non-feed home of
// the room. A check of two owner posts in a room with 2000 DM posts returns the
// two homes and looks each post up by its key.
func TestOtherHomesOfReadsOnlyTheNamedPosts(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	dm := DMConversation(ownerSession)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2000 {
		if _, err := tx.Exec(`INSERT INTO post_homes(room_id, post_id, ts, conversation, author_session_id) VALUES(?, ?, ?, ?, '')`,
			rec.ID, fmt.Sprintf("msg_dm%05d", i), i, dm.String()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO post_homes(room_id, post_id, ts, conversation, author_session_id) VALUES(?, 'msg_feed', 1, 'room', '')`, rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got, err := s.OtherHomesOf(rec.ID, []string{"msg_dm00007", "msg_dm01999", "msg_feed", "msg_missing"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Conversation{"msg_dm00007": dm, "msg_dm01999": dm}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("homes = %v, want %v", got, want)
	}
	if empty, err := s.OtherHomesOf(rec.ID, nil); err != nil || len(empty) != 0 {
		t.Fatalf("no IDs: homes = %v, err = %v", empty, err)
	}

	rows, err := db.Query("EXPLAIN QUERY PLAN "+otherHomesOfQuery(2), rec.ID, ConversationRoom.String(), "msg_a", "msg_b")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "SCAN") {
			t.Fatalf("query plan scans the room: %q", detail)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestConversationValidate(t *testing.T) {
	for _, ok := range []Conversation{"", ConversationRoom, ConversationVoice, DMConversation(ownerSession), SeatDMConversation("room_20261023-120000-abcdef12")} {
		if err := ok.Validate(); err != nil {
			t.Fatalf("%q: %v", string(ok), err)
		}
	}
	for _, bad := range []Conversation{"dm:", "dm:alpha:lead", "feed", "dm:ses_a dm:ses_b", "dm:seat:", "dm:seat:alpha", "dm:seat:room_1"} {
		if err := bad.Validate(); err == nil {
			t.Fatalf("%q was accepted", string(bad))
		}
	}
}
