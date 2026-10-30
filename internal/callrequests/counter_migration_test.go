package callrequests

import (
	"context"
	"database/sql"
	"testing"

	"dyna-agent/internal/database"
)

func TestRevisionMigrationPreservesCounter(t *testing.T) {
	s := testStore(t)
	db, err := database.Open(s.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "callrequests.schema", 2, func(conn *sql.Conn, _ int) error {
		_, err := conn.ExecContext(context.Background(), schema+"; CREATE TABLE call_requests_revision(id INTEGER PRIMARY KEY,revision INTEGER NOT NULL); INSERT INTO call_requests_revision VALUES(1,23)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.Revision()
	if err != nil || n != 23 {
		t.Fatalf("revision=%d err=%v", n, err)
	}
	var exists bool
	if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='call_requests_revision')").Scan(&exists); err != nil || exists {
		t.Fatalf("old table exists=%v err=%v", exists, err)
	}
}
