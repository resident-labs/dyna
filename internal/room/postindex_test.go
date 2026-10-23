package room

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A bound recipient gets one row with the post's mode, an agent address without
// a binding gets a row keyed by the address, and the root seat gets its own row
// with its category.
func TestDeliveriesFor(t *testing.T) {
	bound := Message{
		From: "alpha:lead", Wake: true, RoomID: "room_x",
		To:         []string{"alpha:bea", "alpha:oldname"},
		SessionIDs: map[string]string{"alpha:bea": otherSession},
	}
	got := DeliveriesFor(bound, "alpha")
	if len(got) != 2 || got[0].SessionID != otherSession || got[0].Mode != DeliveryWake || got[0].Category != "" {
		t.Fatalf("deliveries = %+v, want a wake row for %s and one for the unbound address", got, otherSession)
	}
	// A post that names an agent address without binding a session to it still
	// reaches the session that holds that address, so it gets a row of its own.
	// The bare room address names the owner, who is no session, and gets none.
	if got[1].SessionID != "alpha:oldname" || got[1].Mode != DeliveryWake {
		t.Fatalf("unbound row = %+v, want a wake row keyed by alpha:oldname", got[1])
	}

	toRoom := Message{From: "alpha:lead", Wake: true, RoomID: "room_x", To: []string{"alpha"}}
	got = DeliveriesFor(toRoom, "alpha")
	if len(got) != 1 || got[0].SessionID != SeatSessionID("room_x") {
		t.Fatalf("deliveries = %+v, want only the seat row: a room address is no session", got)
	}
	bound.Wake = false
	if got = DeliveriesFor(bound, "alpha"); len(got) != 2 || got[0].Mode != DeliveryQuiet || got[1].Mode != DeliveryQuiet {
		t.Fatalf("deliveries = %+v, want two quiet rows", got)
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
	// The root agent's address gets a row like any other address the post named
	// without a binding; the seat's row is the one that wakes root.
	if len(got) != 2 || got[0].SessionID != "alpha:"+RootAgent || got[0].Mode != DeliveryWake {
		t.Fatalf("deliveries = %+v, want a wake row for the root agent's address", got)
	}
	if seat := got[1]; seat.SessionID != "seat:room_x" || seat.Mode != DeliveryWake || seat.Category != "" {
		t.Fatalf("seat row = %+v, want one waking seat row", seat)
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

// A retry of a post stored before the conversation field existed is accepted:
// it resolves to a home, and the stored post has none to compare with.
func TestPostRetryOfAPostWithoutAConversation(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	bound := map[string]string{"alpha:lead": ownerSession}
	stored := Message{ID: "msg_bbbbbbbbbbbbbbbb", TS: 1, RoomID: rec.ID, From: "alpha", Kind: "message", Human: true, To: []string{"alpha:lead"}, SessionIDs: bound, Text: "hello"}
	appendRawPost(t, s, rec.ID, stored)
	retry, err := s.Post(rec.ID, PostInput{ID: stored.ID, From: stored.From, Human: stored.Human, To: stored.To, SessionIDs: bound, Text: stored.Text})
	if err != nil {
		t.Fatalf("a retry of a post from before the conversation field was refused: %v", err)
	}
	if retry.ID != stored.ID {
		t.Fatalf("retry stored %q, want the stored post %q", retry.ID, stored.ID)
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

// appendRawPost appends one post straight to the room's log, as a crash
// between the log append and the row write would leave it.
func appendRawPost(t *testing.T, s *Store, roomID string, msg Message) {
	t.Helper()
	line, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, roomID, "messages.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
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
	// The owner's DM composer says where it writes; the store never guesses a DM
	// from the mentions.
	dm, err := s.Post(rec.ID, PostInput{From: "alpha", Human: true, Conversation: DMConversation(ownerSession), To: []string{"alpha:lead"}, SessionIDs: bound, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	notice, err := s.PostEvent(rec.ID, SessionEvent{Type: SessionInstructions, Session: "alpha:lead", Detail: "a note"}, "alpha")
	if err != nil {
		t.Fatal(err)
	}

	home, _, deliveries := postIndexRows(t, s, rec.ID, dm.ID)
	if home != string(DMConversation(ownerSession)) {
		t.Fatalf("owner DM home = %q, want dm:%s", home, ownerSession)
	}
	if len(deliveries) != 2 || deliveries[0].state != string(DeliveryQueued) {
		t.Fatalf("owner DM deliveries = %+v, want a queued session row", deliveries)
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
	appendRawPost(t, s, rec.ID, Message{ID: "msg_0123456789abcdef", TS: 1, RoomID: rec.ID, From: "alpha:lead", Kind: "message", To: []string{"alpha:bea"}, SessionIDs: map[string]string{"alpha:bea": otherSession}, Text: "written by a crash"})

	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatalf("the catch-up failed: %v", err)
	}
	home, author, deliveries := postIndexRows(t, s, rec.ID, "msg_0123456789abcdef")
	if home != string(ConversationRoom) || author != "" {
		t.Fatalf("caught-up home = %q, author %q, want the room feed", home, author)
	}
	if len(deliveries) != 1 || deliveries[0].session != otherSession || deliveries[0].state != string(DeliveryQueued) {
		t.Fatalf("caught-up deliveries = %+v, want one queued row for %s: a crash never proves the post arrived", deliveries, otherSession)
	}
	size, err = s.FileSize(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if offset, ok := storedPostCursor(t, s, rec.ID); !ok || offset != size {
		t.Fatalf("caught-up cursor = %d (%v), want %d", offset, ok, size)
	}
}

// A store that imported the index before the seat named its own posts keeps
// those rows as they were. The seat's DM reads the seat as their author, so a
// one-time backfill rewrites them: an old external root answer and an old
// internal root post would otherwise leave the seat's DM, and lose their files.
func TestPostIndexBackfillsTheSeatAuthor(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := s.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, Text: "Root answers"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := s.Post(rec.ID, PostInput{From: rec.Name + ":" + RootAgent, To: []string{rec.Name}, SessionIDs: map[string]string{rec.Name + ":" + RootAgent: ownerSession}, Text: "Root report"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := s.Post(rec.ID, PostInput{From: rec.Name, Human: true, To: []string{rec.Name}, Text: "Owner asks"})
	if err != nil {
		t.Fatal(err)
	}
	// The rows of a store that imported them before the seat named its own
	// posts: no author for the external root, the root session for the other.
	db, err := s.postIndexDB()
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE post_homes SET author_session_id='' WHERE room_id=? AND post_id=?`, []any{rec.ID, external.ID}},
		{`UPDATE post_homes SET author_session_id=? WHERE room_id=? AND post_id=?`, []any{ownerSession, rec.ID, report.ID}},
		{`DELETE FROM meta WHERE key=?`, []any{postIndexAuthorsKey}},
		{`INSERT INTO meta(key, value) VALUES(?, '1') ON CONFLICT(key) DO UPDATE SET value='1'`, []any{postIndexImportKey}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	for _, old := range []struct{ post, author string }{{external.ID, ""}, {report.ID, ownerSession}} {
		if _, author, _ := postIndexRows(t, s, rec.ID, old.post); author != old.author {
			t.Fatalf("previous index author = %q, want %q", author, old.author)
		}
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	seat := SeatSessionID(rec.ID)
	for _, id := range []string{external.ID, report.ID} {
		if _, author, _ := postIndexRows(t, s, rec.ID, id); author != seat {
			t.Fatalf("backfilled author = %q, want the seat %q", author, seat)
		}
	}
	if _, author, _ := postIndexRows(t, s, rec.ID, owner.ID); author != "" {
		t.Fatalf("an owner post names an author: %q", author)
	}
	// The seat's DM finds both, which is what the backfill is for.
	found, err := s.AuthoredPosts(rec.ID, seat, SeatDMConversation(rec.ID), time.Time{}, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 || found[0].Message.ID != external.ID || found[1].Message.ID != report.ID {
		t.Fatalf("seat authored posts = %+v", found)
	}
}
