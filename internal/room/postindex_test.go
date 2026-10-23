package room

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A bound recipient gets one row with the post's mode, an address without a
// binding gets none, and the root seat gets its own row with its category.
func TestDeliveriesFor(t *testing.T) {
	bound := Message{
		From: "alpha:lead", Wake: true,
		To:         []string{"alpha:bea", "alpha:oldname"},
		SessionIDs: map[string]string{"alpha:bea": otherSession},
	}
	got := DeliveriesFor(bound, "alpha")
	if len(got) != 1 || got[0].SessionID != otherSession || got[0].Mode != DeliveryWake || got[0].Category != "" {
		t.Fatalf("deliveries = %+v, want one wake row for %s", got, otherSession)
	}
	bound.Wake = false
	if got = DeliveriesFor(bound, "alpha"); len(got) != 1 || got[0].Mode != DeliveryQuiet {
		t.Fatalf("deliveries = %+v, want one quiet row", got)
	}

	ownerDM := Message{From: "alpha", Human: true, RoomID: "room_x", To: []string{"alpha:lead"}, SessionIDs: map[string]string{"alpha:lead": ownerSession}}
	got = DeliveriesFor(ownerDM, "alpha")
	if len(got) != 2 {
		t.Fatalf("owner DM deliveries = %+v, want the session and the seat", got)
	}
	if got[0].SessionID != ownerSession || got[0].Mode != DeliveryQuiet {
		t.Fatalf("session row = %+v, want a quiet row for %s", got[0], ownerSession)
	}
	if seat := got[1]; seat.SessionID != "seat:room_x" || seat.Mode != DeliveryQuiet || seat.Category != RootOwnerDM {
		t.Fatalf("seat row = %+v, want a quiet %s row", seat, RootOwnerDM)
	}

	needsRoot := Message{From: "alpha", Human: true, Wake: true, NeedsRoot: true, RoomID: "room_x", To: []string{"alpha", "alpha:" + RootAgent}}
	got = DeliveriesFor(needsRoot, "alpha")
	if len(got) != 1 || got[0].SessionID != "seat:room_x" || got[0].Mode != DeliveryWake || got[0].Category != "" {
		t.Fatalf("deliveries = %+v, want one waking seat row", got)
	}
}

// The store keeps the conversation the writer names, puts a session's answer
// to its own DM back in that DM, and refuses a conversation it cannot keep.
func TestPostStoresConversation(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	bound := map[string]string{"alpha:lead": ownerSession}
	first, err := s.Post(rec.ID, PostInput{From: "alpha", Human: true, To: []string{"alpha:lead"}, SessionIDs: bound, Conversation: DMConversation(ownerSession), Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Conversation != DMConversation(ownerSession) {
		t.Fatalf("stored conversation = %q, want dm:%s", first.Conversation, ownerSession)
	}
	reply, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, SessionIDs: bound, ReplyTo: first.ID, Text: "hello back"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.Conversation != DMConversation(ownerSession) {
		t.Fatalf("reply conversation = %q, want dm:%s", reply.Conversation, ownerSession)
	}
	others := map[string]string{"alpha:lead": ownerSession, "alpha:bea": otherSession}
	elsewhere, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha:bea"}, SessionIDs: others, Text: "to bea"})
	if err != nil {
		t.Fatal(err)
	}
	home, author, _ := postIndexRows(t, s, rec.ID, elsewhere.ID)
	if elsewhere.Conversation != ConversationRoom || home != string(ConversationRoom) || author != ownerSession {
		t.Fatalf("post to another session: conversation %q, home %q, author %q", elsewhere.Conversation, home, author)
	}
	if _, err := s.Post(rec.ID, PostInput{From: "alpha", To: []string{"alpha"}, Conversation: "feed", Text: "bad"}); err == nil {
		t.Fatal("a conversation that is not room, voice or a DM was stored")
	}
	if msgs, err := s.Messages(rec.ID); err != nil || len(msgs) != 3 {
		t.Fatalf("posts = %d, %v: want the three good posts only", len(msgs), err)
	}
}

type storedDelivery struct {
	session, mode, category, state string
}

func postIndexRows(t *testing.T, s *Store, roomID, postID string) (conversation, author string, deliveries []storedDelivery) {
	t.Helper()
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT conversation, author_session_id FROM post_homes WHERE room_id=? AND post_id=?", roomID, postID).Scan(&conversation, &author); err != nil {
		t.Fatalf("post %s has no home row: %v", postID, err)
	}
	rows, err := db.Query("SELECT session_id, mode, category, state FROM post_deliveries WHERE post_id=? ORDER BY session_id", postID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var d storedDelivery
		if err := rows.Scan(&d.session, &d.mode, &d.category, &d.state); err != nil {
			t.Fatal(err)
		}
		deliveries = append(deliveries, d)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return conversation, author, deliveries
}

func storedPostCursor(t *testing.T, s *Store, roomID string) (int64, bool) {
	t.Helper()
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	var offset int64
	err = db.QueryRow("SELECT log_offset FROM post_index_cursor WHERE room_id=?", roomID).Scan(&offset)
	if err == sql.ErrNoRows {
		return 0, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return offset, true
}

// Every post gets its rows when it is stored, the one-time import fills the
// rows of posts stored before the index, and the cursor catches up on a post
// that a crash left unindexed.
func TestPostIndexImportAndCursorCatchUp(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	bound := map[string]string{"alpha:lead": ownerSession}
	dm, err := s.Post(rec.ID, PostInput{From: "alpha", Human: true, To: []string{"alpha:lead"}, SessionIDs: bound, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	notice, err := s.Post(rec.ID, PostInput{From: "dyna", Kind: "notice", To: []string{"alpha"}, Text: "a notice"})
	if err != nil {
		t.Fatal(err)
	}

	home, _, deliveries := postIndexRows(t, s, rec.ID, dm.ID)
	if home != string(DMConversation(ownerSession)) {
		t.Fatalf("owner DM home = %q, want dm:%s", home, ownerSession)
	}
	if len(deliveries) != 2 || deliveries[0].state != string(DeliveryQueued) {
		t.Fatalf("owner DM deliveries = %+v, want a queued session row and a seat row", deliveries)
	}
	if home, _, _ := postIndexRows(t, s, rec.ID, notice.ID); home != string(ConversationRoom) {
		t.Fatalf("notice home = %q, want the room feed", home)
	}
	size, err := s.FileSize(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if offset, ok := storedPostCursor(t, s, rec.ID); !ok || offset != size {
		t.Fatalf("cursor = %d (%v), want the log size %d", offset, ok, size)
	}

	// The one-time import fills rows for posts stored before the index.
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"DELETE FROM post_homes", "DELETE FROM post_deliveries", "DELETE FROM post_index_cursor", "DELETE FROM meta WHERE key='" + postIndexImportKey + "'"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	home, _, deliveries = postIndexRows(t, s, rec.ID, dm.ID)
	if home != string(DMConversation(ownerSession)) || deliveries[0].state != string(DeliveryDelivered) {
		t.Fatalf("imported home %q, deliveries %+v: want the DM and delivered rows", home, deliveries)
	}
	if offset, ok := storedPostCursor(t, s, rec.ID); !ok || offset != size {
		t.Fatalf("imported cursor = %d (%v), want %d", offset, ok, size)
	}

	// A post appended before its rows were written is caught up from the
	// cursor, and the catch-up moves the cursor past it.
	lost, err := json.Marshal(Message{ID: "msg_0123456789abcdef", TS: 1, RoomID: rec.ID, From: "alpha:lead", Kind: "message", To: []string{"alpha:bea"}, SessionIDs: map[string]string{"alpha:bea": otherSession}, Text: "written by a crash"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, rec.ID, "messages.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(lost, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatalf("the catch-up failed: %v", err)
	}
	home, author, deliveries := postIndexRows(t, s, rec.ID, "msg_0123456789abcdef")
	if home != string(ConversationRoom) || author != "" {
		t.Fatalf("caught-up home = %q, author %q, want the room feed", home, author)
	}
	if len(deliveries) != 1 || deliveries[0].session != otherSession || deliveries[0].state != string(DeliveryDelivered) {
		t.Fatalf("caught-up deliveries = %+v, want one delivered row for %s", deliveries, otherSession)
	}
	size, err = s.FileSize(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if offset, ok := storedPostCursor(t, s, rec.ID); !ok || offset != size {
		t.Fatalf("caught-up cursor = %d (%v), want %d", offset, ok, size)
	}
}
