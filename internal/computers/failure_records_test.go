package computers

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
)

// TestFailureFileMigratesIntoTheStatusRow covers the one-time import: a
// failure.json from the release that kept the reason in a file moves into the
// computer's row, and the file stops being status.
func TestFailureFileMigratesIntoTheStatusRow(t *testing.T) {
	id := recordTestID(t)
	dir := filepath.Dir(Root())
	db, err := database.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	const ddl = `CREATE TABLE computers(session TEXT PRIMARY KEY, meta BLOB, status BLOB); INSERT INTO meta(key,value) VALUES('computers.records','2');`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(Dir(id), "failure.json")
	if err := os.WriteFile(path, []byte(`{"reason":"compositor did not start","at":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dir); err != nil {
		t.Fatal(err)
	}
	if got, err := storedFailure(id); err != nil || got != "compositor did not start" {
		t.Fatalf("imported reason = %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"reason":"file changed later"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := storedFailure(id); err != nil || got != "compositor did not start" {
		t.Fatalf("status read %q, %v; want the imported reason", got, err)
	}
}

// TestFailureReasonLivesInTheStatusRow covers the status read and its clear.
func TestFailureReasonLivesInTheStatusRow(t *testing.T) {
	id := recordTestID(t)
	if err := saveStatus(Status{SessionID: id, State: StateIdleStopped}); err != nil {
		t.Fatal(err)
	}
	RecordFailure(id, errors.New("cleanup failed"))
	if got, err := storedFailure(id); err != nil || got != "cleanup failed" {
		t.Fatalf("stored reason = %q, %v", got, err)
	}
	if got := savedStatus(id); got.State != StateIdleStopped {
		t.Fatalf("failure replaced the status row: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(Dir(id), "failure.json"), []byte(`{"reason":"stale file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := storedFailure(id); err != nil || got != "cleanup failed" {
		t.Fatalf("status read %q, %v; want only the SQLite reason", got, err)
	}
	clearFailedSetup(id)
	if got, err := storedFailure(id); err != nil || got != "" {
		t.Fatalf("cleared reason = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(Dir(id), "failure.json")); !os.IsNotExist(err) {
		t.Fatalf("failure.json survived a successful start: %v", err)
	}
}
