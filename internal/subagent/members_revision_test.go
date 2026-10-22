package subagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
)

func revisionStore(t *testing.T) *Store {
	t.Helper()
	s := &Store{Root: filepath.Join(t.TempDir(), "sessions")}
	t.Cleanup(func() { database.Close(filepath.Dir(s.Root)) })
	return s
}

func newSession(t *testing.T, s *Store, name string) string {
	t.Helper()
	id := NewID()
	if err := os.MkdirAll(filepath.Join(s.Root, id, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	recordtest.Write(t, s.Root, id, Meta{ID: id, Name: name, RoomID: "room", Room: "room", Created: time.Now()})
	return id
}

// Every write the members scan reads moves MembersRevision, and so does a
// delete: deleting the newest session row and writing another one, which
// would take its revision again, still moves it (requirement 16).
func TestMembersRevisionMovesOnEveryMembersWrite(t *testing.T) {
	s := revisionStore(t)
	a := newSession(t, s, "a")
	last := func() string {
		t.Helper()
		rev, err := s.MembersRevision()
		if err != nil {
			t.Fatal(err)
		}
		return rev
	}
	prev := last()
	moved := func(what string) {
		t.Helper()
		if now := last(); now == prev {
			t.Fatalf("%s did not move the members revision (%s)", what, now)
		} else {
			prev = now
		}
	}
	if again := last(); again != prev {
		t.Fatalf("an idle read moved the revision: %s then %s", prev, again)
	}
	if _, err := s.UpdateMeta(a, func(m *Meta) { m.Profile = "changed" }); err != nil {
		t.Fatal(err)
	}
	moved("a session row write")
	if err := s.writeTurn(a, Turn{N: 1, Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	moved("a turn row write")
	if err := writeQueue(filepath.Join(s.Root, a), claims(1)); err != nil {
		t.Fatal(err)
	}
	moved("a queue row write")
	b := newSession(t, s, "b")
	moved("a new session")
	if err := sessionlife.Delete(s.Root, b); err != nil {
		t.Fatal(err)
	}
	newSession(t, s, "c")
	moved("deleting the newest session row, then writing another")
}

// Deleting a session also deletes its turn and queue rows and their copies'
// stamps, in the same transaction, and leaves other sessions' rows alone.
func TestDeleteRemovesTheSessionsTurnsAndQueue(t *testing.T) {
	s := revisionStore(t)
	gone, kept := newSession(t, s, "gone"), newSession(t, s, "kept")
	for _, id := range []string{gone, kept} {
		if err := s.writeTurn(id, Turn{N: 1, Prompt: "hello"}); err != nil {
			t.Fatal(err)
		}
		if err := writeQueue(filepath.Join(s.Root, id), claims(1)); err != nil {
			t.Fatal(err)
		}
	}
	if err := sessionlife.Delete(s.Root, gone); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(filepath.Dir(s.Root))
	if err != nil {
		t.Fatal(err)
	}
	count := func(query, id string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, c := range []struct{ what, query string }{
		{"turn rows", "SELECT COUNT(*) FROM session_turns WHERE session = ?"},
		{"queue rows", "SELECT COUNT(*) FROM session_queues WHERE session = ?"},
		{"copy stamps", "SELECT COUNT(*) FROM state_mirrors WHERE key IN ('queues/' || ?1, 'turns/' || ?1 || '/1')"},
	} {
		if n := count(c.query, gone); n != 0 {
			t.Errorf("the deleted session kept %d %s", n, c.what)
		}
		if n := count(c.query, kept); n == 0 {
			t.Errorf("deleting another session removed this one's %s", c.what)
		}
	}
}

// TurnStatus reads a turn's status from its row, for readers that cannot
// import subagent, and reports a turn with no row yet.
func TestTurnStatusReadsTheRow(t *testing.T) {
	s := revisionStore(t)
	id := newSession(t, s, "a")
	if _, ok, err := sessionlife.TurnStatus(s.Root, id, 1); err != nil || ok {
		t.Fatalf("TurnStatus before any turn = %v, %v; want no row", ok, err)
	}
	if err := s.writeTurn(id, Turn{N: 1, Status: TurnOK}); err != nil {
		t.Fatal(err)
	}
	// The rollback copy says otherwise; the row is the turn.
	path := filepath.Join(s.Root, id, "turns", "1.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if status, ok, err := sessionlife.TurnStatus(s.Root, id, 1); err != nil || !ok || status != TurnOK {
		t.Fatalf("TurnStatus = %q, %v, %v; want ok from the row", status, ok, err)
	}
}
