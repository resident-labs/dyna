package subagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife/recordtest"
	"dyna-agent/internal/store"
)

func turnStore(t *testing.T) (*Store, string) {
	t.Helper()
	s := &Store{Root: filepath.Join(t.TempDir(), "sessions")}
	t.Cleanup(func() { database.Close(filepath.Dir(s.Root)) })
	id := NewID()
	if err := os.MkdirAll(filepath.Join(s.Root, id, "turns"), 0o700); err != nil {
		t.Fatal(err)
	}
	return s, id
}

func turnFile(t *testing.T, s *Store, id string, n int) string {
	t.Helper()
	path, err := s.turnPath(id, n)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func prompt(t *testing.T, s *Store, id string, n int) string {
	t.Helper()
	turn, err := s.ReadTurn(id, n)
	if err != nil {
		t.Fatal(err)
	}
	return turn.Prompt
}

// Current turn writes create only canonical rows.
func TestTurnWritesOnlyCanonicalRecord(t *testing.T) {
	s, id := turnStore(t)
	if err := s.writeTurn(id, Turn{N: 1, Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(turnFile(t, s, id, 1)); !os.IsNotExist(err) {
		t.Fatalf("turn write created a JSON copy: %v", err)
	}
	if got := prompt(t, s, id, 1); got != "first" {
		t.Fatalf("canonical turn reads %q", got)
	}
}

// A read keeps the canonical revision after an update.
func TestOwnTurnCopyIsNotImported(t *testing.T) {
	s, id := turnStore(t)
	if _, err := s.UpdateTurn(id, 1, func(*Turn) {}); err == nil {
		t.Fatal("updated a turn that does not exist")
	}
	if err := s.writeTurn(id, Turn{N: 1, Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.turnVersion(id, 1)
	if _, err := s.UpdateTurn(id, 1, func(turn *Turn) { turn.Prompt = "second" }); err != nil {
		t.Fatal(err)
	}
	written, _ := s.turnVersion(id, 1)
	prompt(t, s, id, 1)
	after, _ := s.turnVersion(id, 1)
	if written == before || after != written {
		t.Fatalf("versions %s, %s, then %s after a read: a read imported this code's own copy", before, written, after)
	}
}

// A retained file cannot replace a canonical row on a read or an update.
func TestTurnFileFromAnOlderBinaryCannotReplaceTheRow(t *testing.T) {
	s, id := turnStore(t)
	if err := s.writeTurn(id, Turn{N: 1, Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.turnVersion(id, 1)
	if err := store.WriteJSON(turnFile(t, s, id, 1), Turn{N: 1, Prompt: "old binary"}, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := prompt(t, s, id, 1); got != "first" {
		t.Fatalf("a read after an old binary's write reads %q; want the row, first", got)
	}
	if _, err := s.UpdateTurn(id, 1, func(*Turn) {}); err != nil {
		t.Fatal(err)
	}
	if got := prompt(t, s, id, 1); got != "first" {
		t.Fatalf("after the next write, the turn reads %q; want the canonical row", got)
	}
	after, _ := s.turnVersion(id, 1)
	if after == before {
		t.Fatal("the canonical update did not move the turn's version")
	}
	if again, _ := s.turnVersion(id, 1); again != after {
		t.Fatalf("a read changed the canonical revision: %s then %s", after, again)
	}
}

// A crash after the row commits and before its copy is written leaves the
// old copy matching the row's mirror, so the row wins.
func TestCrashBeforeTheTurnCopyKeepsTheRow(t *testing.T) {
	s, id := turnStore(t)
	if err := s.writeTurn(id, Turn{N: 1, Prompt: "first"}); err != nil {
		t.Fatal(err)
	}
	db, err := s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(Turn{N: 1, Prompt: "second"})
	if _, err := saveTurnRow(context.Background(), db, id, 1, raw); err != nil {
		t.Fatal(err)
	}
	if got := prompt(t, s, id, 1); got != "second" {
		t.Fatalf("the stale copy won over the row: %q", got)
	}
}

// At the first engine start the engine backs up dyna.db once (requirements
// 21 and 23). Retained turn files are imported before startup readers run.
func TestTurnFilesAreImportedAtStartupAfterOneBackup(t *testing.T) {
	s, id := turnStore(t)
	recordtest.Write(t, s.Root, id, Meta{ID: id, Name: "old", RoomID: "room", Room: "room", Turns: 2})
	for n := 1; n <= 2; n++ {
		if err := store.WriteJSON(turnFile(t, s, id, n), Turn{N: n, Prompt: "turn " + strconv.Itoa(n)}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Root), "backups", database.PreMigrationFolder, "dyna-*.db"))
	if len(backups) != 1 {
		t.Fatalf("backups at the first start: %v", backups)
	}
	backup, err := sql.Open("sqlite", backups[0])
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	var tables int
	if err := backup.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='session_turns'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("backup was taken after creating the turn table: %d, %v", tables, err)
	}
	turns, err := s.Turns(id)
	if err != nil || len(turns) != 2 || turns[0].Prompt != "turn 1" || turns[1].Prompt != "turn 2" {
		t.Fatalf("Turns = %+v, %v; want both old turns", turns, err)
	}
	for n := 1; n <= 2; n++ {
		if err := os.Remove(turnFile(t, s, id, n)); err != nil {
			t.Fatal(err)
		}
	}
	if turns, err := s.Turns(id); err != nil || len(turns) != 2 {
		t.Fatalf("after the files go, Turns = %+v, %v; want the imported rows", turns, err)
	}
	if err := Migrate(filepath.Dir(s.Root)); err != nil {
		t.Fatal(err)
	}
	if again, _ := filepath.Glob(filepath.Join(filepath.Dir(s.Root), "backups", database.PreMigrationFolder, "dyna-*.db")); len(again) != 1 {
		t.Fatalf("a later start backed up again: %v", again)
	}
}

// The largest session today has 1,534 turns. Its first read, which imports
// every file, and a full read stay well inside the test budget.
func TestLargeSessionTurnsImportAndRead(t *testing.T) {
	const turns = 1534
	s, id := turnStore(t)
	recordtest.Write(t, s.Root, id, Meta{ID: id, Name: "large", RoomID: "room", Room: "room", Turns: turns})
	for n := 1; n <= turns; n++ {
		b, _ := json.Marshal(Turn{N: n, Status: TurnOK, Prompt: fmt.Sprintf("turn %d", n)})
		if err := os.WriteFile(turnFile(t, s, id, n), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	all, err := s.Turns(id) // The first read imports every file.
	if err != nil || len(all) != turns || all[turns-1].Prompt != fmt.Sprintf("turn %d", turns) {
		t.Fatalf("Turns = %d turns, %v", len(all), err)
	}
	imported := time.Since(start)
	start = time.Now()
	if all, err := s.Turns(id); err != nil || len(all) != turns {
		t.Fatalf("second Turns = %d turns, %v", len(all), err)
	}
	t.Logf("%d turns: first read with imports %v, full read %v", turns, imported, time.Since(start))
}

func TestTurnRange(t *testing.T) {
	s, id := turnStore(t)
	recordtest.Write(t, s.Root, id, Meta{ID: id, Name: "range", RoomID: "room", Room: "room", Turns: 5})
	for n := 1; n <= 5; n++ {
		if err := s.writeTurn(id, Turn{N: n, Status: TurnOK, Prompt: strconv.Itoa(n)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		first, last int
		want        string
	}{
		{2, 4, "234"},
		{4, 5, "45"},
		{5, 5, "5"},
	} {
		turns, err := s.TurnRange(id, tc.first, tc.last)
		if err != nil {
			t.Fatal(err)
		}
		var got string
		for _, turn := range turns {
			got += turn.Prompt
		}
		if got != tc.want {
			t.Fatalf("TurnRange(%d, %d) prompts = %q, want %q", tc.first, tc.last, got, tc.want)
		}
	}
	for _, bounds := range [][2]int{{0, 1}, {3, 2}} {
		if _, err := s.TurnRange(id, bounds[0], bounds[1]); err == nil {
			t.Fatalf("TurnRange(%d, %d) accepted invalid bounds", bounds[0], bounds[1])
		}
	}
	db, err := s.turnsDB()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM session_turns WHERE session = ? AND n = 3", id); err != nil {
		t.Fatal(err)
	}
	turns, err := s.TurnRange(id, 2, 4)
	if err != nil || len(turns) != 3 || turns[1].Status != TurnUnreadable || turns[1].N != 3 {
		t.Fatalf("range with a missing row = %+v, %v", turns, err)
	}
}

// An update changes only the canonical row, even beside a foreign turn file.
func TestTurnUpdateKeepsTheCanonicalStatus(t *testing.T) {
	s, id := turnStore(t)
	if err := s.writeTurn(id, Turn{N: 1, Prompt: "first", Status: TurnOK}); err != nil {
		t.Fatal(err)
	}
	path := turnFile(t, s, id, 1)
	if err := store.WriteJSON(path, Turn{N: 1, Prompt: "old binary", Status: TurnRunning}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTurn(id, 1, func(turn *Turn) { turn.Prompt = "update" }); err != nil {
		t.Fatal(err)
	}
	turn, err := s.ReadTurn(id, 1)
	if err != nil || turn.Prompt != "update" || turn.Status != TurnOK {
		t.Fatalf("turn after the update = %+v, %v; want the update on the canonical turn", turn, err)
	}
}

// A damaged retained turn file cannot affect a canonical row.
func TestDamagedTurnFileKeepsTheRow(t *testing.T) {
	s, id := turnStore(t)
	if err := s.writeTurn(id, Turn{N: 1, Prompt: "row"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(turnFile(t, s, id, 1), []byte("not JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := prompt(t, s, id, 1); got != "row" {
			t.Fatalf("after a damaged file, the turn reads %q", got)
		}
	}
}
