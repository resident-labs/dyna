package subagent

import (
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/sessionlife"
	"dyna-agent/internal/sessionlife/recordtest"
)

func TestMigratePreservesExplicitUnassignedHistory(t *testing.T) {
	data := t.TempDir()
	id := "ses_20261019-120000-aaaaaaaa"
	dir := filepath.Join(data, "sessions", id, "turns")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	recordtest.WriteRaw(t, filepath.Join(data, "sessions"), id, []byte(`{"id":"`+id+`","turns":1,"future":17}`))
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(`{"n":1,"status":"ok","inputCutoff":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := sessionlife.Migrate(data); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	var history bool
	if err := db.QueryRow("SELECT history_only FROM session_records WHERE id=?", id).Scan(&history); err != nil || !history {
		t.Fatalf("history projection = %v, %v", history, err)
	}
	var raw map[string]any
	if err := sessionlife.Load(filepath.Join(data, "sessions"), id, &raw); err != nil || raw["historyOnly"] != true || raw["future"] != float64(17) {
		t.Fatalf("history record = %v, %v", raw, err)
	}
	s := &Store{Root: filepath.Join(data, "sessions")}
	if _, err := s.ReadMeta(id); err != nil {
		t.Fatal(err)
	}
	if turn, err := s.ReadTurn(id, 1); err != nil || turn.Status != TurnOK {
		t.Fatalf("turn = %+v, %v", turn, err)
	}
	if all, err := s.ListMeta(); err != nil || len(all) != 0 {
		t.Fatalf("routing = %v, %v", all, err)
	}
	if err := sessionlife.Check(s.Root, id); err == nil {
		t.Fatal("history can publish")
	}
}
