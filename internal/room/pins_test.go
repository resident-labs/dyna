package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pinFixture(t *testing.T) (*Store, Record, *time.Time) {
	t.Helper()
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	s := &Store{Root: t.TempDir(), Now: func() time.Time { return now }}
	rec, err := s.Create(CreateInput{Name: "alpha", CreatedBy: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	return s, rec, &now
}

func pinPost(t *testing.T, s *Store, rec Record, text string) Message {
	t.Helper()
	m, err := s.Post(rec.ID, PostInput{From: rec.Name + ":lead", To: []string{rec.Name}, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPinOrderIdempotenceAndUnpin(t *testing.T) {
	s, rec, now := pinFixture(t)
	a, b := pinPost(t, s, rec, "a"), pinPost(t, s, rec, "b")
	first, err := s.Pin(rec.ID, a.ID, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if _, err := s.Pin(rec.ID, b.ID, "alpha:lead"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	again, err := s.Pin(rec.ID, a.ID, "alpha:other")
	if err != nil || again != first {
		t.Fatalf("second pin = %+v, %v; want the first %+v", again, err, first)
	}
	pins, err := s.Pins(rec.ID)
	if err != nil || len(pins) != 2 || pins[0].ID != b.ID || pins[1].ID != a.ID {
		t.Fatalf("pins = %+v, %v; want newest first, one entry each", pins, err)
	}
	if pins[1].By != "alpha" || pins[1].At != first.At {
		t.Fatalf("repin changed the entry: %+v", pins[1])
	}
	if err := s.Unpin(rec.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Unpin(rec.ID, b.ID); err != nil {
		t.Fatalf("unpin of a missing pin: %v", err)
	}
	if pins, _ = s.Pins(rec.ID); len(pins) != 1 || pins[0].ID != a.ID {
		t.Fatalf("pins after unpin = %+v", pins)
	}
}

func TestPinRefusesUnknownPost(t *testing.T) {
	s, rec, _ := pinFixture(t)
	if _, err := s.Pin(rec.ID, "msg_nope", "alpha"); err == nil {
		t.Fatal("pinned an unknown post")
	}
	if pins, _ := s.Pins(rec.ID); len(pins) != 0 {
		t.Fatalf("pins = %+v", pins)
	}
	if _, err := os.Stat(filepath.Join(s.Root, rec.ID, PinsFile)); !os.IsNotExist(err) {
		t.Fatalf("a refused pin wrote a file: %v", err)
	}
}

func TestPinCap(t *testing.T) {
	s, rec, _ := pinFixture(t)
	var ids []string
	for i := 0; i <= MaxPins; i++ {
		ids = append(ids, pinPost(t, s, rec, "p").ID)
	}
	for _, id := range ids[:MaxPins] {
		if _, err := s.Pin(rec.ID, id, "alpha"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.Pin(rec.ID, ids[MaxPins], "alpha")
	if err == nil || !strings.Contains(err.Error(), "50 pinned") {
		t.Fatalf("err = %v, want the cap error", err)
	}
	if _, err := s.Pin(rec.ID, ids[0], "alpha"); err != nil {
		t.Fatalf("repin at the cap must still work: %v", err)
	}
	if err := s.Unpin(rec.ID, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pin(rec.ID, ids[MaxPins], "alpha"); err != nil {
		t.Fatalf("pin after freeing a slot: %v", err)
	}
}

func TestDeleteRoomRemovesPins(t *testing.T) {
	s, rec, _ := pinFixture(t)
	m := pinPost(t, s, rec, "keep")
	if _, err := s.Pin(rec.ID, m.ID, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(rec.Name, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, rec.ID, PinsFile)); !os.IsNotExist(err) {
		t.Fatalf("pins file survived delete: %v", err)
	}
	if _, err := s.Pin(rec.ID, m.ID, "alpha"); err == nil {
		t.Fatal("pinned in a deleted room")
	}
}

func TestEveryPostKindCanBePinnedAndRepliedTo(t *testing.T) {
	s, rec, _ := pinFixture(t)
	for _, kind := range []string{"message", "completed", "failed", "stopped", "pr", "question", "goal", "future-kind"} {
		t.Run(kind, func(t *testing.T) {
			post, err := s.Post(rec.ID, PostInput{From: "alpha:lead", To: []string{"alpha"}, Kind: kind, Text: "Stored report"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Pin(rec.ID, post.ID, "alpha"); err != nil {
				t.Fatal(err)
			}
			reply, err := s.Post(rec.ID, PostInput{From: "alpha", To: []string{"alpha:lead"}, Text: "Reply", ReplyTo: post.ID})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := s.Message(rec.ID, reply.ID)
			if err != nil || stored.ReplyTo != post.ID {
				t.Fatalf("reply = %+v, %v", stored, err)
			}
		})
	}
}

// Pins are rows: they list with pins.json gone, and every pin change moves
// PinsRevision, an unpin included (requirement 19).
func TestPinsAreRows(t *testing.T) {
	s, rec, _ := pinFixture(t)
	a := pinPost(t, s, rec, "a")
	if rev, err := s.PinsRevision(rec.ID); err != nil || rev != 0 {
		t.Fatalf("PinsRevision before any pin = %d, %v", rev, err)
	}
	if _, err := s.Pin(rec.ID, a.ID, "alpha"); err != nil {
		t.Fatal(err)
	}
	pinned, _ := s.PinsRevision(rec.ID)
	if _, err := os.Stat(filepath.Join(s.Root, rec.ID, PinsFile)); !os.IsNotExist(err) {
		t.Fatalf("pin wrote a rollback file: %v", err)
	}
	if pins, err := s.Pins(rec.ID); err != nil || len(pins) != 1 || pins[0].ID != a.ID {
		t.Fatalf("pins without pins.json = %+v, %v", pins, err)
	}
	if err := s.Unpin(rec.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if rev, _ := s.PinsRevision(rec.ID); rev <= pinned {
		t.Fatalf("an unpin left PinsRevision at %d (was %d)", rev, pinned)
	}
}

// Once room.pins is migrated, files cannot overwrite current rows.
func TestPinsIgnoreFilesAfterMigration(t *testing.T) {
	s, rec, _ := pinFixture(t)
	a, b := pinPost(t, s, rec, "a"), pinPost(t, s, rec, "b")
	if _, err := s.Pin(rec.ID, a.ID, "alpha"); err != nil {
		t.Fatal(err)
	}
	before, err := s.PinsRevision(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, rec.ID, PinsFile)
	if err := os.WriteFile(path, []byte(`[{"id":"`+b.ID+`","by":"alpha:old","at":2}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if rev, err := s.PinsRevision(rec.ID); err != nil || rev != before {
		t.Fatalf("old file changed revision = %d, %v; want %d", rev, err, before)
	}
	pins, err := s.Pins(rec.ID)
	if err != nil || len(pins) != 1 || pins[0].ID != a.ID {
		t.Fatalf("pins = %+v, %v; want current row", pins, err)
	}
}

// The owner's reads are rows: they survive owner-read.json's removal, and an
// later changes to an earlier release's file do not alter canonical reads.
func TestOwnerReadsAreRows(t *testing.T) {
	s, rec, _ := pinFixture(t)
	if err := s.MarkOwnerReadMany(rec.ID, []string{"msg_a", "msg_b"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, rec.ID, "owner-read.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if ids, err := s.OwnerReadIDs(rec.ID); err != nil || !ids["msg_a"] || !ids["msg_b"] {
		t.Fatalf("reads without owner-read.json = %v, %v", ids, err)
	}
	if err := os.WriteFile(path, []byte(`["msg_c"]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.OwnerReadIDs(rec.ID); err != nil || len(ids) != 2 || ids["msg_c"] {
		t.Fatalf("reads after an earlier release's file = %v, %v; want the two canonical marks", ids, err)
	}
}
