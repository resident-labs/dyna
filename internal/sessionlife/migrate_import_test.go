package sessionlife

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
)

func TestMigrateImportsSessionRecordsAtomicallyOnce(t *testing.T) {
	data := t.TempDir()
	t.Cleanup(func() { _ = database.Close(data) })
	sessions := filepath.Join(data, "sessions")
	write := func(id, body string) {
		t.Helper()
		path := filepath.Join(sessions, id)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "session.json"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("ses_a", `{"id":"ses_a","status":"idle"}`)
	write("ses_b", `{`)
	write("ses_c", `{"id":"ses_c","status":"old"}`)
	if err := Save(sessions, "ses_c", map[string]any{"id": "ses_c", "status": "current"}); err != nil {
		t.Fatal(err)
	}
	db, err := database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	count := func(query string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	err = Migrate(data)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(sessions, "ses_b", "session.json")) {
		t.Errorf("damaged source = %v", err)
	}
	if n := count("SELECT COUNT(*) FROM session_records WHERE id='ses_a'"); n != 0 {
		t.Fatalf("partial import committed %d rows", n)
	}
	if n := count("SELECT COUNT(*) FROM meta WHERE key='sessions.import'"); n != 0 {
		t.Fatalf("failed import committed version")
	}
	write("ses_b", `{"id":"wrong"}`)
	if err := Migrate(data); err == nil {
		t.Error("mismatched source ID accepted")
	}
	write("ses_b", `{"id":"ses_b"}`)
	write("ses_c", `{`)
	unsafe := filepath.Join(sessions, "ses_unsafe")
	if err := os.Symlink(t.TempDir(), unsafe); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(data); err == nil || !strings.Contains(err.Error(), unsafe) {
		t.Errorf("unsafe source = %v", err)
	}
	if err := os.Remove(unsafe); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT COUNT(*) FROM session_records"); n != 3 {
		t.Fatalf("imported rows = %d", n)
	}
	if n := count("SELECT COUNT(*) FROM session_records WHERE id='ses_c' AND status='current'"); n != 1 {
		t.Fatal("canonical record replaced")
	}
	if _, err := db.Exec("DELETE FROM session_records WHERE id='ses_a'"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(data); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(data); err != nil {
		t.Fatal(err)
	}
	db, err = database.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if n := count("SELECT COUNT(*) FROM session_records WHERE id='ses_a'"); n != 0 {
		t.Fatal("completed import restored deleted row")
	}
	raw, err := os.ReadFile(filepath.Join(sessions, "ses_a", "session.json"))
	if err != nil || string(raw) != `{"id":"ses_a","status":"idle"}` {
		t.Fatal("source changed")
	}
}
