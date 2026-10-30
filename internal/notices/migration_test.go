package notices

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"dyna-agent/internal/database"
)

func TestDismissedNoticeCannotReturnFromRetainedJSON(t *testing.T) {
	s := Store{Dir: t.TempDir()}
	t.Cleanup(func() { database.Close(s.Dir) })
	db, err := database.Open(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db, "notices.schema", 1, func(conn *sql.Conn, _ int) error {
		if _, err := conn.ExecContext(context.Background(), schema); err != nil {
			return err
		}
		_, err := conn.ExecContext(context.Background(), "CREATE TABLE notices_revision (n INTEGER NOT NULL); INSERT INTO notices_revision VALUES (4)")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]Notice{{ID: "dismissed", Key: "disk", Text: "Stale alert", At: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		if err := Migrate(s.Dir); err != nil {
			t.Fatal(err)
		}
		list, err := s.List()
		if err != nil || len(list) != 0 {
			t.Fatalf("pass %d revived dismissed notice: %+v %v", pass, list, err)
		}
		rev, err := s.Revision()
		db, openErr := database.Open(s.Dir)
		if openErr != nil {
			t.Fatal(openErr)
		}
		var exists bool
		if err := db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE name='notices_revision')").Scan(&exists); err != nil || exists {
			t.Fatalf("old table exists=%v err=%v", exists, err)
		}
		if err != nil || rev != 4 {
			t.Fatalf("pass %d revision = %d, %v", pass, rev, err)
		}
		got, err := os.ReadFile(s.Path())
		if err != nil || string(got) != string(raw) {
			t.Fatalf("source changed: %s %v", got, err)
		}
		if err := database.Close(s.Dir); err != nil {
			t.Fatal(err)
		}
	}
}
