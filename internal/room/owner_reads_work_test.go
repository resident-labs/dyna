package room

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
	"dyna-agent/internal/store"
)

func ownerReadFixture(t *testing.T) (*Store, Record, *sql.DB, string) {
	t.Helper()
	s, rec, _ := pinFixture(t)
	db, err := s.ownerReadsDB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := database.Close(filepath.Dir(s.Root)); err != nil {
			t.Error(err)
		}
	})
	if err := s.MarkOwnerRead(rec.ID, "msg_a"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.Root, rec.ID, ownerReadsFile)
	if err := os.WriteFile(path, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	return s, rec, db, path
}

func TestOwnerReadUnchangedAcknowledgementsDoNoFullWork(t *testing.T) {
	for _, empty := range []bool{false, true} {
		name := "duplicate"
		if empty {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			s, rec, db, path := ownerReadFixture(t)
			if err := state.For(db).Write(context.Background(), hub.Event{}, func(conn *sql.Conn) error {
				_, err := conn.ExecContext(context.Background(), `WITH RECURSIVE ids(n) AS (
VALUES(1) UNION ALL SELECT n+1 FROM ids WHERE n<512)
INSERT INTO room_owner_reads(room_id,msg_id) SELECT ?,printf('msg_%04d',n) FROM ids`, rec.ID)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			// A new mark must not materialize the retained history.
			if err := s.MarkOwnerRead(rec.ID, "msg_b"); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			read := storedOwnerReads
			loads, rows := 0, 0
			storedOwnerReads = func(db *sql.DB, id string) (map[string]bool, error) {
				got, err := read(db, id)
				loads++
				rows += len(got)
				return got, err
			}
			t.Cleanup(func() { storedOwnerReads = read })
			ids := []string{"msg_a", "msg_a"}
			if empty {
				ids = nil
			}
			// A new Store must not materialize the retained set.
			reopened := &Store{Root: s.Root}
			if err := reopened.MarkOwnerReadMany(rec.ID, ids); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if loads != 0 || rows != 0 || !store.VersionOf(before).Equal(store.VersionOf(after)) {
				t.Fatalf("%s acknowledgement loaded %d snapshots/%d rows or rewrote the copy", name, loads, rows)
			}
			if err := reopened.MarkOwnerRead(rec.ID, "msg_new"); err != nil {
				t.Fatal(err)
			}
			if loads != 0 || rows != 0 {
				t.Fatalf("new mark loaded %d snapshots/%d rows, want 0/0", loads, rows)
			}
			got, err := read(db, rec.ID)
			if err != nil || !got["msg_new"] || len(got) != 515 {
				t.Fatalf("new canonical mark = %v, %v", got, err)
			}
		})
	}
}
