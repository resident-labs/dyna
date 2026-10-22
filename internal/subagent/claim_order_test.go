package subagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/room"
)

func TestRecoveredClaimsDoNotCopyRoomHistory(t *testing.T) {
	rooms := &room.Store{Root: t.TempDir()}
	rec, err := rooms.Create(room.CreateInput{Name: "claims"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := &Store{Root: t.TempDir()}
	meta := Meta{ID: NewID(), Name: "target", Room: rec.Name, RoomID: rec.ID}
	dir, _ := sessions.Dir(meta.ID)
	if err := os.MkdirAll(filepath.Join(dir, "turns"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "turns", "1.json"), []byte(`{"n":1,"status":"ok"}`), 0600); err != nil {
		t.Fatal(err)
	}
	posts := []room.Message{}
	for i := range 2 {
		post, err := rooms.Post(rec.ID, room.PostInput{From: rec.Name, To: []string{meta.Address()}, Text: fmt.Sprint(i)})
		if err != nil {
			t.Fatal(err)
		}
		posts = append(posts, post)
	}
	// Equal timestamps require append order, rather than time or map order.
	path := filepath.Join(rooms.Root, rec.ID, "messages.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for i := range posts {
		posts[i].TS = 1
		if err := encoder.Encode(posts[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeQueue(dir, queueClaims{Claimed: map[string]int{posts[1].ID: 1, posts[0].ID: 1}}); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Store: sessions, Rooms: rooms}
	measure := func() float64 {
		t.Helper()
		meta.RoomCursor, err = rooms.FileSize(rec.ID)
		if err != nil {
			t.Fatal(err)
		}
		drain := func() {
			got, cursor, err := manager.drainRoomMessages(meta, Turn{N: 2})
			if err != nil || len(got) != 2 || got[0].ID != posts[0].ID || got[1].ID != posts[1].ID || cursor != meta.RoomCursor {
				t.Fatalf("recovered claims: %+v, cursor %d, error %v", got, cursor, err)
			}
		}
		drain() // Warm the room index before measuring recovery.
		return testing.AllocsPerRun(3, drain)
	}
	small := measure()
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	encoder = json.NewEncoder(file)
	for i := range 300 {
		if err := encoder.Encode(room.Message{ID: fmt.Sprintf("history-%d", i), From: rec.Name, To: []string{rec.Name}, SessionIDs: map[string]string{rec.Name: "owner"}, Text: "unrelated history"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	large := measure()
	if large > small+100 {
		t.Fatalf("claim recovery allocations grow with retained history: small %.0f, large %.0f", small, large)
	}
}
