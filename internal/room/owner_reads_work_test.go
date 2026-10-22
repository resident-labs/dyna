package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	return s, rec, db, filepath.Join(s.Root, rec.ID, ownerReadsFile)
}

func ownerReadFile(t *testing.T, path string, want ...string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("owner-read copy = %v, want %v", got, want)
	}
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
			// A real new mark establishes the copy after the retained history.
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
			// A new Store must reuse persisted completion, not a process cache.
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
			if loads != 1 || rows != 515 {
				t.Fatalf("new mark loaded %d snapshots/%d rows, want 1/515", loads, rows)
			}
			got, err := read(db, rec.ID)
			if err != nil || !got["msg_new"] || len(got) != 515 {
				t.Fatalf("new canonical mark = %v, %v", got, err)
			}
		})
	}
}

func TestOwnerReadAcknowledgementMirrorsCanonicalForeignUnion(t *testing.T) {
	for _, ids := range [][]string{nil, {"msg_b"}} {
		t.Run(fmt.Sprint(ids), func(t *testing.T) {
			s, rec, _, path := ownerReadFixture(t)
			if err := s.MarkOwnerRead(rec.ID, "msg_b"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".foreign", []byte(`["msg_b","msg_c"]`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".foreign", path); err != nil {
				t.Fatal(err)
			}
			// A read may import and stamp the foreign subset before the ack.
			if got, err := s.OwnerReadIDs(rec.ID); err != nil || len(got) != 3 {
				t.Fatalf("foreign union = %v, %v", got, err)
			}
			if err := (&Store{Root: s.Root}).MarkOwnerReadMany(rec.ID, ids); err != nil {
				t.Fatal(err)
			}
			ownerReadFile(t, path, "msg_a", "msg_b", "msg_c")
		})
	}
}

func TestOwnerReadAcknowledgementRepairsInterruptedMirror(t *testing.T) {
	for _, stage := range []string{"before-pending", "after-rename"} {
		t.Run(stage, func(t *testing.T) {
			s, rec, db, path := ownerReadFixture(t)
			null := "NOT NULL"
			if stage == "after-rename" {
				null = "NULL"
			}
			trigger := fmt.Sprintf(`CREATE TRIGGER fail_owner_read_mirror BEFORE UPDATE OF pending ON state_mirrors
WHEN NEW.key='%s' AND NEW.pending IS %s
BEGIN SELECT RAISE(ABORT,'fixture mirror failure'); END`, ownerReadsKey(rec.ID), null)
			if _, err := db.Exec(trigger); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkOwnerRead(rec.ID, "msg_new"); err != nil {
				t.Fatal(err)
			}
			var pending sql.NullString
			if err := db.QueryRow("SELECT pending FROM state_mirrors WHERE key=?", ownerReadsKey(rec.ID)).Scan(&pending); err != nil {
				t.Fatal(err)
			}
			if (pending.Valid && pending.String != "") != (stage == "after-rename") {
				t.Fatalf("failure at %s left pending=%+v", stage, pending)
			}
			if stage == "before-pending" {
				ownerReadFile(t, path, "msg_a")
			} else {
				ownerReadFile(t, path, "msg_a", "msg_new")
			}
			if _, err := db.Exec("DROP TRIGGER fail_owner_read_mirror"); err != nil {
				t.Fatal(err)
			}
			ids := []string{"msg_new"}
			if stage == "after-rename" {
				ids = nil
			}
			if err := (&Store{Root: s.Root}).MarkOwnerReadMany(rec.ID, ids); err != nil {
				t.Fatal(err)
			}
			ownerReadFile(t, path, "msg_a", "msg_new")
			if err := db.QueryRow("SELECT pending FROM state_mirrors WHERE key=?", ownerReadsKey(rec.ID)).Scan(&pending); err != nil || pending.Valid {
				t.Fatalf("recovery left pending=%+v, error=%v", pending, err)
			}
		})
	}
}
