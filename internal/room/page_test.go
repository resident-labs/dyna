package room

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSelectedWindowUsesTheSharedIndex(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "selected-window"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := range 8 {
		msg, err := s.Post(rec.ID, PostInput{From: "author", To: []string{rec.Name}, Text: fmt.Sprintf("note %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	decoded := 0
	s.decodeMessage = func(data []byte, msg *Message) error { decoded++; return json.Unmarshal(data, msg) }
	unread := PageFilter{Unread: func(msg Message) bool { return msg.ID != ids[0] }}
	page, err := s.ReadPage(rec.ID, "", 3, unread)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 3 || page.Messages[0].ID != ids[5] || page.Earlier != 5 || page.Cursor != ids[5] {
		t.Fatalf("selected window retained %d messages, earlier %d, cursor %q", len(page.Messages), page.Earlier, page.Cursor)
	}
	if page.Unread != 7 || page.FirstUnread != ids[1] {
		t.Fatalf("unread metadata: %+v", page)
	}
	if decoded != 8 {
		t.Fatalf("cold index decoded %d messages, want 8", decoded)
	}
	older, err := s.ReadPage(rec.ID, page.Cursor, 3, unread)
	if err != nil || len(older.Messages) != 3 || older.Messages[0].ID != ids[2] || older.Earlier != 2 {
		t.Fatalf("older page: %+v, %v", older, err)
	}
	if older.Offset != page.Offset || older.Unread != 7 || older.FirstUnread != ids[1] {
		t.Fatalf("older metadata changed: %+v", older)
	}
	if decoded != 8 {
		t.Fatalf("indexed reads decoded old messages: %d", decoded)
	}
	page.Messages[0].To[0] = "changed"
	again, err := s.ReadPage(rec.ID, "", 3, unread)
	if err != nil || again.Messages[0].To[0] != rec.Name {
		t.Fatalf("page changed shared index: %+v, %v", again, err)
	}
	if _, err := s.ReadPage(rec.ID, "missing", 3, unread); err == nil {
		t.Fatal("unknown cursor was accepted")
	}
	if _, err := s.Post(rec.ID, PostInput{From: "author", To: []string{rec.Name}, Text: "appended"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadPage(rec.ID, "", 3, unread); err != nil {
		t.Fatal(err)
	}
	if decoded != 9 {
		t.Fatalf("append decoded %d messages, want 9", decoded)
	}
}

func TestForwardPageKeepsLiveArrivalsContiguous(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "forward-window"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for i := 0; i < 8; i++ {
		msg, err := s.Post(rec.ID, PostInput{From: "author", To: []string{rec.Name}, Text: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	older, err := s.ReadPage(rec.ID, ids[5], 3, PageFilter{})
	if err != nil {
		t.Fatal(err)
	}
	live, err := s.Post(rec.ID, PostInput{From: "author", To: []string{rec.Name}, Text: "live"})
	if err != nil {
		t.Fatal(err)
	}
	forward, err := s.ReadAfter(rec.ID, older.Messages[2].ID, 3, PageFilter{})
	if err != nil || len(forward.Messages) != 3 || forward.Messages[0].ID != ids[5] || forward.Messages[2].ID != ids[7] || forward.Earlier != 5 || forward.Newer != 1 {
		t.Fatalf("forward skipped the dropped page: %+v, %v", forward, err)
	}
	tail, err := s.ReadAfter(rec.ID, forward.Messages[2].ID, 3, PageFilter{})
	if err != nil || len(tail.Messages) != 1 || tail.Messages[0].ID != live.ID || tail.Newer != 0 {
		t.Fatalf("live tail: %+v %v", tail, err)
	}
	if _, err := s.ReadAfter(rec.ID, "missing", 3, PageFilter{}); err == nil {
		t.Fatal("accepted unknown forward cursor")
	}
}

func TestSearchMessagesMatchesEveryWordNewestFirst(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "search"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, text := range []string{"Deploy failed on CI", "lunch", "ci deploy is green", "Deploy notes", "CI DEPLOY retry"} {
		from := "search:lead"
		if i == 1 {
			from = "search:deployer"
		}
		msg, err := s.Post(rec.ID, PostInput{From: from, To: []string{rec.Name}, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, msg.ID)
	}
	got, more, err := s.SearchMessages(rec.ID, "  deploy  CI ", 2, nil)
	if err != nil || more != true || len(got) != 2 || got[0].ID != ids[4] || got[1].ID != ids[2] {
		t.Fatalf("first page: %v more=%v %v", got, more, err)
	}
	all, more, err := s.SearchMessages(rec.ID, "deploy ci", 0, nil)
	if err != nil || more || len(all) != 3 || all[2].ID != ids[0] {
		t.Fatalf("all matches: %v more=%v %v", all, more, err)
	}
	// The sender's address is searched too.
	if by, _, err := s.SearchMessages(rec.ID, "deployer", 0, nil); err != nil || len(by) != 1 || by[0].ID != ids[1] {
		t.Fatalf("sender match: %v %v", by, err)
	}
	got[0].Text = "changed"
	if again, _, _ := s.SearchMessages(rec.ID, "retry", 0, nil); len(again) != 1 || again[0].Text != "CI DEPLOY retry" {
		t.Fatalf("a result changed the shared index: %v", again)
	}
	if _, _, err := s.SearchMessages(rec.ID, " ", 0, nil); err == nil {
		t.Fatal("empty search was accepted")
	}
	// keep drops messages before they count toward the limit.
	kept, more, err := s.SearchMessages(rec.ID, "deploy ci", 1, func(m Message) bool { return m.ID != ids[4] })
	if err != nil || len(kept) != 1 || kept[0].ID != ids[2] || !more {
		t.Fatalf("kept: %v more=%v %v", kept, more, err)
	}
	for _, q := range []string{"a b c d e f g h i", strings.Repeat("x", MaxSearchBytes+1)} {
		if _, _, err := s.SearchMessages(rec.ID, q, 0, nil); err == nil {
			t.Fatalf("over-long search %q was accepted", q[:10])
		}
	}
}
