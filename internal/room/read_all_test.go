package room

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// A log past ReadFrom's 4 MiB read must still list its newest posts. Cards
// in a large room served 404 because Messages stopped at 4 MiB.
func TestMessagesReadsPastFourMiB(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s := DefaultStore()
	rec, err := s.Create(CreateInput{Name: "alpha", CreatedBy: "test"})
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.messagesPath(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	const n = 500 // about 5 MiB
	pad := strings.Repeat("x", 10*1024)
	for i := range n {
		b, _ := json.Marshal(Message{ID: fmt.Sprintf("msg_%d", i), RoomID: rec.ID, From: "alpha", Text: pad})
		if _, err := f.Write(append(b, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	if info, _ := os.Stat(path); info.Size() <= 4<<20 {
		t.Fatalf("log is %d bytes; the test needs more than 4 MiB", info.Size())
	}

	first, _, err := s.ReadFrom(rec.ID, 0)
	if err != nil || len(first) >= n {
		t.Fatalf("ReadFrom read %d messages, err %v; want a partial read", len(first), err)
	}
	msgs, err := s.Messages(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != n || msgs[n-1].ID != fmt.Sprintf("msg_%d", n-1) {
		t.Fatalf("Messages read %d messages, want %d ending with msg_%d", len(msgs), n, n-1)
	}
	all, next, err := s.ReadAll(rec.ID, 0)
	if info, _ := os.Stat(path); err != nil || len(all) != n || next != info.Size() {
		t.Fatalf("ReadAll = %d messages, next %d, err %v; want %d and the file size", len(all), next, err, n)
	}
}
