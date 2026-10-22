package roomsvc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/room"
)

func largeOwnerLog(t *testing.T, e *env) room.Record {
	t.Helper()
	rec := e.room("large-owner-index")
	var log bytes.Buffer
	encoder := json.NewEncoder(&log)
	write := func(message room.Message) {
		t.Helper()
		if err := encoder.Encode(message); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 60 {
		write(room.Message{ID: fmt.Sprintf("msg_%016x", i), RoomID: rec.ID, From: rec.Name + ":lead", To: []string{rec.Name}, Text: "Unread before padding"})
	}
	for i := range 2 {
		write(room.Message{ID: fmt.Sprintf("msg_ffffffffffffff%02x", i), RoomID: rec.ID, From: rec.Name, To: []string{rec.Name + ":lead"}, Text: strings.Repeat("x", 3<<20)})
	}
	for i := 60; i < 120; i++ {
		write(room.Message{ID: fmt.Sprintf("msg_%016x", i), RoomID: rec.ID, From: rec.Name + ":lead", To: []string{rec.Name}, Text: "Unread after padding"})
	}
	if err := os.WriteFile(filepath.Join(e.svc.rooms().Root, rec.ID, "messages.jsonl"), log.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	return rec
}

func roomUnreadCount(t *testing.T, s *Service, id string) int {
	t.Helper()
	rooms, err := s.Rooms()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rooms {
		if row.ID == id {
			return row.Unread
		}
	}
	t.Fatal("room missing")
	return 0
}

func TestRoomUnreadCountsEntireLogOnFirstRead(t *testing.T) {
	e := newEnv(t)
	rec := largeOwnerLog(t, e)
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 120 {
		t.Fatalf("large log first unread count is %d; want 120", got)
	}
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 120 {
		t.Fatalf("large log repeat unread count is %d; want 120", got)
	}
	e.say(rec, "lead", "A later unread append")
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 121 {
		t.Fatalf("large log append unread count is %d; want 121", got)
	}
}

func TestRoomUnreadFollowsSameSizeReplacement(t *testing.T) {
	e := newEnv(t)
	rec := e.room("replacement-owner-index")
	first := room.Message{ID: "msg_0000000000000001", RoomID: rec.ID, From: rec.Name + ":lead", To: []string{rec.Name}, Text: "Unread"}
	data, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(e.svc.rooms().Root, rec.ID, "messages.jsonl")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 1 {
		t.Fatalf("initial unread count: %d", got)
	}
	if err := e.svc.MarkRead(rec.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 0 {
		t.Fatalf("marked unread count: %d", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = "msg_0000000000000002"
	replacement, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	replacement = append(replacement, '\n')
	if len(replacement) != len(data) {
		t.Fatal("replacement changed the file size")
	}
	next := path + ".replacement"
	if err := os.WriteFile(next, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(next, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if got := roomUnreadCount(t, e.svc, rec.ID); got != 1 {
		t.Fatalf("same-size replacement unread count is %d; want 1", got)
	}
}
