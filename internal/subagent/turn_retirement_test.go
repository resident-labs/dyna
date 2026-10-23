package subagent

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

func TestCompletedTurnImportDoesNotResurrectDeletedRow(t *testing.T) {
	s, id := turnStore(t)
	dataDir := filepath.Dir(s.Root)
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "subagent.turns", 3, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(t.Context(), `CREATE TABLE session_turns (
 session TEXT NOT NULL, n INTEGER NOT NULL, revision INTEGER NOT NULL,
 turn BLOB NOT NULL, mirror TEXT NOT NULL DEFAULT '', PRIMARY KEY(session,n));
CREATE UNIQUE INDEX session_turns_revision ON session_turns(revision);`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw := []byte("{\n  \"n\": 1, \"prompt\": \"retained history\"\n}\n")
	if _, err := db.Exec("INSERT INTO session_turns(session,n,revision,turn) VALUES (?,1,1,?)", id, raw); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO meta(key,value) VALUES ('subagent.turns.import','2')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("DELETE FROM session_turns WHERE session=?", id); err != nil {
		t.Fatal(err)
	}
	path := turnFile(t, s, id, 1)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dataDir); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadTurn(id, 1); err == nil {
		t.Fatal("turn read resurrected a deleted canonical row after completed import")
	}
	if err := s.writeTurn(id, Turn{N: 1, Prompt: "current row"}); err != nil {
		t.Fatal(err)
	}
	if saved, err := os.ReadFile(path); err != nil || !bytes.Equal(saved, raw) {
		t.Fatalf("canonical write changed retained history: %s, %v", saved, err)
	}
	if err := s.writeTurn(id, Turn{N: 2, Prompt: "new turn"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(turnFile(t, s, id, 2)); !os.IsNotExist(err) {
		t.Fatalf("new turn created a JSON copy: %v", err)
	}
	for query, want := range map[string]int{
		"SELECT value FROM meta WHERE key='subagent.turns'":                           4,
		"SELECT count(*) FROM pragma_table_info('session_turns') WHERE name='mirror'": 0,
		"SELECT count(*) FROM meta WHERE key='subagent.reports'":                      0,
	} {
		var got int
		if err := db.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("%s = %d, %v; want %d", query, got, err, want)
		}
	}
}
