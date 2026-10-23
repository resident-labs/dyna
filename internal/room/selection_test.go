package room

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSelectedMessagesUseOnlyAppendedWork(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "selected-work"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	for i := range 2000 {
		from := "other"
		if i%2 == 0 {
			from = "selected"
		}
		if err := enc.Encode(Message{ID: fmt.Sprintf("msg_%016x", i), TS: base.Add(time.Duration(i) * time.Millisecond).UnixMilli(), From: from, To: []string{rec.Name}}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(s.Root, rec.ID, "messages.jsonl")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	decoded, visited := 0, 0
	s.decodeMessage = func(b []byte, m *Message) error {
		decoded++
		return json.Unmarshal(b, m)
	}
	selection := &MessageSelection{}
	match := func(m Message) bool {
		visited++
		// The matcher may read the same index without deadlocking.
		if visited == 1 {
			if _, err := s.Message(rec.ID, m.ID); err != nil {
				t.Fatal(err)
			}
		}
		return m.From == "selected"
	}
	since := base.Add(time.Second + time.Nanosecond)
	before := base.Add(1006 * time.Millisecond)
	read := func() []Message {
		t.Helper()
		got, err := s.SelectedMessages(rec.ID, selection, match, since, before, 3)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	ids := func(msgs []Message) []string {
		var out []string
		for _, m := range msgs {
			out = append(out, m.ID)
		}
		return out
	}
	want := []string{"msg_00000000000003ea", "msg_00000000000003ec", "msg_00000000000003ee"}
	got := read()
	if !reflect.DeepEqual(ids(got), want) || decoded != 2000 || visited != 2000 {
		t.Fatalf("cold: IDs %v, decoded %d, visited %d", ids(got), decoded, visited)
	}
	got[0].To[0] = "caller mutation"
	decoded, visited = 0, 0
	if again := read(); !reflect.DeepEqual(ids(again), want) || again[0].To[0] != rec.Name || decoded != 0 || visited != 0 {
		t.Fatalf("warm: IDs %v, decoded %d, visited %d", ids(again), decoded, visited)
	}
	if allocs := testing.AllocsPerRun(1, func() { read() }); allocs > 60 {
		t.Fatalf("three selected messages used %.0f allocations", allocs)
	}
	// An out-of-order timestamp must not change append ordering in a page.
	appended := Message{ID: "msg_ffffffffffffffff", TS: base.Add(1003 * time.Millisecond).UnixMilli(), From: "selected", To: []string{rec.Name}}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = json.NewEncoder(f).Encode(appended)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append: %v, close: %v", err, closeErr)
	}
	want = append(want[1:], appended.ID)
	if got := read(); !reflect.DeepEqual(ids(got), want) || decoded != 1 || visited != 1 {
		t.Fatalf("append: IDs %v, decoded %d, visited %d", ids(got), decoded, visited)
	}
	latest, err := s.LatestSelectedMessages(rec.ID, selection, match, 2)
	if err != nil || !reflect.DeepEqual(ids(latest), []string{"msg_00000000000007cc", "msg_00000000000007ce"}) {
		t.Fatalf("timestamp order: %v, %v", ids(latest), err)
	}
	// Replacing the file resets the same selection, including its time index.
	replacement, err := json.Marshal(appended)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".new", append(replacement, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		t.Fatal(err)
	}
	decoded, visited = 0, 0
	if got := read(); !reflect.DeepEqual(ids(got), []string{appended.ID}) || decoded != 1 || visited != 1 {
		t.Fatalf("replacement: IDs %v, decoded %d, visited %d", ids(got), decoded, visited)
	}
}

func TestSelectedMessagesKeepTimestampTiesInAppendOrder(t *testing.T) {
	s := &Store{Root: t.TempDir()}
	rec, err := s.Create(CreateInput{Name: "selected-ties"})
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return time.UnixMilli(1) }
	var want []string
	for range 4 {
		msg, err := s.Post(rec.ID, PostInput{From: "selected", To: []string{rec.Name}, Text: "same time"})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, msg.ID)
	}
	selection := &MessageSelection{}
	all := func(Message) bool { return true }
	for _, limit := range []int{0, 1, 3, 10} {
		messages, err := s.SelectedMessages(rec.ID, selection, all, time.Time{}, time.Time{}, limit)
		if err != nil || len(messages) != min(limit, len(want)) {
			t.Fatalf("limit %d: %v, %v", limit, messages, err)
		}
		for i, message := range messages {
			if message.ID != want[max(0, len(want)-limit)+i] {
				t.Fatalf("limit %d: message %d = %s", limit, i, message.ID)
			}
		}
		latest, err := s.LatestSelectedMessages(rec.ID, selection, all, limit)
		if err != nil || !reflect.DeepEqual(latest, messages) {
			t.Fatalf("timestamp ties, limit %d: %v, %v", limit, latest, err)
		}
	}
}
