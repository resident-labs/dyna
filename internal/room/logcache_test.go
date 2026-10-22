package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLogReadsDecodeOnlyNewMessages(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "cached", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	decoded := 0
	s.decodeMessage = func(b []byte, m *Message) error { decoded++; return json.Unmarshal(b, m) }
	first, err := s.Post(rec.ID, PostInput{From: "cached", To: []string{"cached"}, Text: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Messages(rec.ID); err != nil {
		t.Fatal(err)
	}
	if decoded != 1 {
		t.Fatalf("first read decoded %d messages, want 1", decoded)
	}
	if _, err := s.Message(rec.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReadAll(rec.ID, 0); err != nil {
		t.Fatal(err)
	}
	if decoded != 1 {
		t.Fatalf("unchanged reads decoded %d messages, want 1", decoded)
	}
	if _, err := s.Post(rec.ID, PostInput{From: "cached", To: []string{"cached"}, Text: "second"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages(rec.ID)
	if err != nil || len(msgs) != 2 || msgs[1].Text != "second" {
		t.Fatalf("appended messages: %v, %v", msgs, err)
	}
	if decoded != 2 {
		t.Fatalf("append decoded %d messages, want 2", decoded)
	}
}

// This benchmark uses an isolated COPY of a room store when one is provided.
func BenchmarkRoomPoll(b *testing.B) {
	root := os.Getenv("ROOM_SCAN_PROFILE_ROOMS")
	if root == "" {
		b.Skip("set ROOM_SCAN_PROFILE_ROOMS to a copied room store")
	}
	s := &Store{Root: root}
	records, err := s.List(true)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		for _, rec := range records {
			msgs, err := s.Messages(rec.ID)
			if err != nil {
				b.Fatal(err)
			}
			if len(msgs) > 0 {
				if _, err := s.Message(rec.ID, msgs[len(msgs)-1].ID); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}

func TestLogCacheRefreshAndCopies(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "refresh", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.messagesPath(rec.ID)
	write := func(text string) {
		t.Helper()
		data, _ := json.Marshal(Message{ID: "msg_test", Text: text, To: []string{"refresh"}, SessionIDs: map[string]string{"refresh": "one"}, Forward: &Forward{Text: "original"}})
		if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("first")
	first, err := s.Message(rec.ID, "msg_test")
	if err != nil {
		t.Fatal(err)
	}
	first.To[0] = "changed"
	first.SessionIDs["refresh"] = "changed"
	first.Forward.Text = "changed"
	again, err := s.Message(rec.ID, "msg_test")
	if err != nil || again.To[0] != "refresh" || again.SessionIDs["refresh"] != "one" || again.Forward.Text != "original" {
		t.Fatalf("returned value changed cache: %+v, %v", again, err)
	}
	// A same-size rewrite must be visible without waiting for a clock tick.
	info, _ := os.Stat(path)
	write("other")
	if err := os.Chtimes(path, info.ModTime().Add(1), info.ModTime().Add(1)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Message(rec.ID, "msg_test")
	if err != nil || got.Text != "other" {
		t.Fatalf("edited post: %+v, %v", got, err)
	}
	// Atomic replacement has a new identity even when size and time match.
	data, _ := os.ReadFile(path)
	data = bytes.ReplaceAll(data, []byte("other"), []byte("third"))
	replacement := path + ".new"
	if err := os.WriteFile(replacement, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Message(rec.ID, "msg_test"); err != nil || got.Text != "third" {
		t.Fatalf("replaced log: %+v, %v", got, err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.Messages(rec.ID)
	if err != nil || len(msgs) != 0 {
		t.Fatalf("truncated log: %v, %v", msgs, err)
	}
	if err := s.Rename(rec.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	renamed, err := s.Resolve(rec.ID)
	if err != nil || renamed.DisplayName != "renamed" {
		t.Fatalf("renamed room: %+v, %v", renamed, err)
	}
	if _, err := s.Delete(rec.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Messages(rec.ID); err == nil {
		t.Fatal("deleted room was returned from cache")
	}
}

func TestLogCacheCompletesPartialLine(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "partial", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.messagesPath(rec.ID)
	data, _ := json.Marshal(Message{ID: "msg_partial", Text: "partial"})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	msgs, next, err := s.ReadAll(rec.ID, 0)
	if err != nil || len(msgs) != 0 || next != 0 {
		t.Fatalf("partial line: %v, %d, %v", msgs, next, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.Write([]byte{'\n'})
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	msgs, next, err = s.ReadAll(rec.ID, 0)
	if err != nil || len(msgs) != 1 || next != int64(len(data)+1) {
		t.Fatalf("complete line: %v, %d, %v", msgs, next, err)
	}
}

func TestLogCacheReplacementCanGrowAndDeletedRoomsReleaseIndex(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "replace", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.messagesPath(rec.ID)
	replace := func(messages ...Message) {
		t.Helper()
		var data []byte
		for _, message := range messages {
			line, _ := json.Marshal(message)
			data = append(data, append(line, '\n')...)
		}
		temp := path + ".new"
		if err := os.WriteFile(temp, data, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(temp, path); err != nil {
			t.Fatal(err)
		}
	}
	replace(Message{ID: "old"})
	if _, err := s.Messages(rec.ID); err != nil {
		t.Fatal(err)
	}
	replace(Message{ID: "middle"})
	replace(Message{ID: "new-one"}, Message{ID: "new-two"})
	got, err := s.Messages(rec.ID)
	if err != nil || len(got) != 2 || got[0].ID != "new-one" {
		t.Fatalf("larger replacement: %+v, %v", got, err)
	}
	other := &Store{Root: s.Root}
	if _, err := other.Delete(rec.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(true); err != nil {
		t.Fatal(err)
	}
	if len(s.logs) != 0 {
		t.Fatal("listing retained a deleted room's index")
	}
}

func TestRecoveredClaimOrderDecodesOnlyAppendedMessages(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "claim-order"})
	if err != nil {
		t.Fatal(err)
	}
	path, _ := s.messagesPath(rec.ID)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(f)
	for i := range 300 {
		if err := encoder.Encode(Message{ID: fmt.Sprintf("post-%d", i), TS: 1, Text: "retained history"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	decoded := 0
	s.decodeMessage = func(b []byte, m *Message) error { decoded++; return json.Unmarshal(b, m) }
	// Claim lookup warms the same index used by ordering.
	if _, err := s.Message(rec.ID, "post-20"); err != nil {
		t.Fatal(err)
	}
	if decoded != 300 {
		t.Fatalf("initial index decoded %d messages, want 300", decoded)
	}
	order := func() {
		t.Helper()
		// Caller-owned values must retain their text, even when the index has other text.
		msgs := []Message{{ID: "post-299", Text: "last claim"}, {ID: "post-20", Text: "first claim"}}
		if err := s.OrderMessages(rec.ID, msgs); err != nil {
			t.Fatal(err)
		}
		if msgs[0].ID != "post-20" || msgs[0].Text != "first claim" || msgs[1].ID != "post-299" || msgs[1].Text != "last claim" {
			t.Fatalf("claim order: %+v", msgs)
		}
	}
	order()
	if decoded != 300 {
		t.Fatalf("claim ordering decoded old history: got %d, want 300", decoded)
	}
	if _, err := s.Post(rec.ID, PostInput{From: rec.Name, To: []string{rec.Name}, Text: "new unrelated post"}); err != nil {
		t.Fatal(err)
	}
	order()
	if decoded != 301 {
		t.Fatalf("append decoded %d messages, want 301", decoded)
	}
	order()
	if decoded != 301 {
		t.Fatalf("repeated claim ordering decoded %d messages, want 301", decoded)
	}
}

func TestPagedCursorReadsDecodeOnlyAppendedMessages(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "paged"})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(s.Root, rec.ID, "messages.jsonl"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for i := range 300 {
		if err := encoder.Encode(Message{ID: fmt.Sprintf("msg_%016x", i), RoomID: rec.ID, From: rec.Name, To: []string{rec.Name}, Text: strings.Repeat("x", MaxMessageBytes)}); err != nil {
			file.Close()
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	decoded := 0
	s.decodeMessage = func(b []byte, msg *Message) error { decoded++; return json.Unmarshal(b, msg) }
	page, cursor, err := s.ReadFrom(rec.ID, 0)
	if err != nil || len(page) == 0 || len(page) >= 300 || decoded != 300 {
		t.Fatalf("first page: %d messages, %d decoded, %v", len(page), decoded, err)
	}
	remaining, end, err := s.ReadAll(rec.ID, cursor)
	if err != nil || len(remaining)+len(page) != 300 || decoded != 300 {
		t.Fatalf("remaining pages: %d messages, %d decoded, %v", len(remaining), decoded, err)
	}
	post, err := s.Post(rec.ID, PostInput{Wake: true, From: rec.Name, To: []string{"paged:target"}, Text: "new input"})
	if err != nil {
		t.Fatal(err)
	}
	added, next, err := s.ReadAll(rec.ID, end)
	if err != nil || len(added) != 1 || added[0].ID != post.ID || decoded != 301 {
		t.Fatalf("appended bytes: %+v, %d decoded, %v", added, decoded, err)
	}
	unchanged, _, err := s.ReadAll(rec.ID, next)
	if err != nil || len(unchanged) != 0 || decoded != 301 {
		t.Fatalf("unchanged cursor: %+v, %d decoded, %v", unchanged, decoded, err)
	}
}
