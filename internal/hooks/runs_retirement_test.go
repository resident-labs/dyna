package hooks

import (
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dyna-agent/internal/database"
	statewrite "dyna-agent/internal/state"
)

func TestRunReceiptStaysCanonicalAfterImport(t *testing.T) {
	s := runTestService(t)
	db, err := database.Open(s.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "hooks.runs", 1, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(t.Context(), runsSchema)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	run := Run{ID: strings.Repeat("a", 32), Repo: "repo", Name: "hook-test", Result: "pending", StartedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.dir(run.Repo), "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, run.ID+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ownedID := strings.Repeat("b", 32)
	ownedPath := filepath.Join(dir, ownedID+".json")
	owned := run
	owned.ID = ownedID
	ownedRaw, err := json.Marshal(owned)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Base(s.dir(run.Repo))
	if err := statewrite.For(db).Mirror(t.Context(), runMirrorKey(key, ownedID), ownedPath, func(out io.Writer) error { _, err := out.Write(ownedRaw); return err }); err != nil {
		t.Fatal(err)
	}
	var stamp string
	if err := db.QueryRow("SELECT version FROM state_mirrors WHERE key=?", runMirrorKey(key, ownedID)).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if err := MigrateRuns(s.DataDir); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := db.QueryRow("SELECT body FROM hook_runs WHERE repo=? AND id=?", key, run.ID).Scan(&body); err != nil || body != string(raw) {
		t.Fatalf("immediate import = %s, %v", body, err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM hook_runs WHERE id=?", ownedID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("owned absence restored: %d, %v", count, err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(raw) {
		t.Fatalf("source changed: %s, %v", got, err)
	}
	var after string
	if err := db.QueryRow("SELECT version FROM state_mirrors WHERE key=?", runMirrorKey(key, ownedID)).Scan(&after); err != nil || after != stamp {
		t.Fatalf("stamp changed: %s, %v", after, err)
	}
}
