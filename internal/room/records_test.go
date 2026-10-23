package room

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func recordsStore(t *testing.T) *Store {
	t.Helper()
	s := &Store{Root: filepath.Join(t.TempDir(), "rooms")}
	t.Cleanup(func() { database.Close(filepath.Dir(s.Root)) })
	return s
}

func roomsRevision(t *testing.T, s *Store) int64 {
	t.Helper()
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.QueryRow("SELECT value FROM meta WHERE key = ?", RevisionKey).Scan(&v); err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A room's record is its row: the room resolves and lists with room.json
// gone, and every record write raises rooms.revision (requirement 19).
func TestRoomRecordIsARow(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	before := roomsRevision(t, s)
	if err := s.Rename("alpha", "Alpha room"); err != nil {
		t.Fatal(err)
	}
	if after := roomsRevision(t, s); after <= before {
		t.Fatalf("a record write left rooms.revision at %d (was %d)", after, before)
	}
	if err := os.Remove(filepath.Join(s.Root, rec.ID, "room.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	got, err := s.Resolve("alpha")
	if err != nil || got.DisplayName != "Alpha room" {
		t.Fatalf("Resolve without room.json = %+v, %v", got, err)
	}
	if list, err := s.List(true); err != nil || len(list) != 1 || list[0].ID != rec.ID {
		t.Fatalf("List without room.json = %+v, %v", list, err)
	}
}

// Posts and archive changes leave retained source files untouched.
func TestPostTouchesTheRowWithoutCopyingRoomFile(t *testing.T) {
	now := time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC)
	s := recordsStore(t)
	s.Now = func() time.Time { return now }
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, rec.ID, "room.json")
	copied := []byte("retained")
	if err := os.WriteFile(path, copied, 0600); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := s.Post(rec.ID, PostInput{From: "alpha:a", To: []string{"alpha"}, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Resolve(rec.ID); !got.LastActivity.Equal(now) {
		t.Fatalf("row last activity = %v, want %v", got.LastActivity, now)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, copied) {
		t.Fatal("a post rewrote room.json")
	}
	if err := s.SetArchived(rec.ID, true); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(after, copied) {
		t.Fatal("archive rewrote room.json")
	}
}

// Deleting a room removes its row, issues, pins, owner reads and its copies'
// stamps in one write, and raises rooms.revision, so a watcher sees the delete.
func TestDeleteRemovesTheRoomsRows(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetSeat(context.Background(), rec.ID, Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FileIssue(rec.ID, "a smell", "", "x.go", 1, "alpha:a"); err != nil {
		t.Fatal(err)
	}
	post, err := s.Post(rec.ID, PostInput{From: "alpha:a", To: []string{"alpha"}, Text: "pinned"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pin(rec.ID, post.ID, "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkOwnerRead(rec.ID, post.ID); err != nil {
		t.Fatal(err)
	}
	before := roomsRevision(t, s)
	if _, err := s.Delete("alpha", nil); err != nil {
		t.Fatal(err)
	}
	if after := roomsRevision(t, s); after <= before {
		t.Fatalf("a delete left rooms.revision at %d (was %d)", after, before)
	}
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"SELECT COUNT(*) FROM room_records WHERE id = ?1",
		"SELECT COUNT(*) FROM room_issues WHERE room_id = ?1",
		"SELECT COUNT(*) FROM room_pins WHERE room_id = ?1",
		"SELECT COUNT(*) FROM room_pin_revisions WHERE room_id = ?1",
		"SELECT COUNT(*) FROM room_owner_reads WHERE room_id = ?1",
		"SELECT COUNT(*) FROM state_mirrors WHERE substr(key, 1, length('rooms/' || ?1)) = 'rooms/' || ?1",
	} {
		var n int
		if err := db.QueryRow(q, rec.ID).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s = %d, %v; want 0", q, n, err)
		}
	}
	if _, err := s.Resolve("alpha"); err == nil {
		t.Fatal("the deleted room still resolves")
	}
}

func TestRoomWritesPreserveRetainedSources(t *testing.T) {
	s := recordsStore(t)
	rec, err := s.Create(CreateInput{Name: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Root, rec.ID)
	for _, name := range []string{"room.json", "seat.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Rename(rec.ID, "Current name"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.SetSeat(t.Context(), rec.ID, Seat{External: true}, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"room.json", "seat.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(raw) != "{}" {
			t.Fatalf("write changed %s: %s, %v", name, raw, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{stale"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Resolve(rec.ID)
	if err != nil || got.DisplayName != "Current name" || !got.Seat.External {
		t.Fatalf("canonical record = %+v, %v", got, err)
	}
	list, err := s.List(true)
	if err != nil || len(list) != 1 || list[0] != got {
		t.Fatalf("canonical listing = %+v, %v", list, err)
	}
	if err := s.SetArchived(rec.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"room.json", "seat.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(raw) != "{stale" {
			t.Fatalf("write changed stale %s: %s, %v", name, raw, err)
		}
	}
}
