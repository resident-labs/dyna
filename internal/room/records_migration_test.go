package room

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

func TestRoomRecordsMigrateBeforeAnyRead(t *testing.T) {
	const id = "room_20261022-120000-1234abcd"
	const attached = `{"external":true,"harness":"fixture"}`
	for _, mode := range []string{"separate seat", "embedded seat", "separate detach", "canonical default", "canonical detach", "bad seat"} {
		t.Run(mode, func(t *testing.T) {
			s := recordsStore(t)
			db, err := s.recordsDB()
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(s.Root, id)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"room.json": `{"id":"` + id + `","name":"retained","seat":` + attached + `}`}
			want := Record{ID: id, Name: "retained", Seat: Seat{External: true, Harness: "fixture"}}
			if mode != "embedded seat" {
				files["seat.json"] = attached
			}
			switch mode {
			case "separate detach":
				files["seat.json"] = `{}`
				want.Seat = Seat{}
			case "canonical default", "canonical detach":
				want.Name, want.Seat = "canonical", Seat{}
				if err := state.For(db).Write(t.Context(), hub.Event{}, func(conn *sql.Conn) error {
					if err := putRecord(t.Context(), conn, want); err != nil {
						return err
					}
					if mode == "canonical detach" {
						return putSeat(t.Context(), conn, id, Seat{})
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				files["room.json"], files["seat.json"] = "{stale", "{stale"
			case "bad seat":
				files["seat.json"] = "{broken"
			}
			for name, raw := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = Migrate(filepath.Dir(s.Root))
			if mode == "bad seat" {
				if err == nil || !strings.Contains(err.Error(), filepath.Join(dir, "seat.json")) {
					t.Fatalf("bad seat error = %v; want source path", err)
				}
				var rows, markers, revisions int
				if err := db.QueryRow(`SELECT (SELECT count(*) FROM room_records), (SELECT count(*) FROM meta WHERE key='room.records.import'), (SELECT count(*) FROM meta WHERE key='rooms.revision')`).Scan(&rows, &markers, &revisions); err != nil || rows != 0 || markers != 0 || revisions != 0 {
					t.Fatalf("failed import left rows=%d markers=%d revisions=%d: %v", rows, markers, revisions, err)
				}
				files["seat.json"] = attached
				if err := os.WriteFile(filepath.Join(dir, "seat.json"), []byte(attached), 0600); err != nil {
					t.Fatal(err)
				}
				err = Migrate(filepath.Dir(s.Root))
			}
			if err != nil {
				t.Fatal(err)
			}
			// Query SQL before any room read can run the compatibility importer.
			var body string
			var seat sql.NullString
			if err := db.QueryRow("SELECT body,seat FROM room_records WHERE id=?", id).Scan(&body, &seat); err != nil {
				t.Fatalf("startup did not import room: %v", err)
			}
			got, err := scanRecord(body, seat)
			if err != nil || got != want || seat.Valid != (mode != "canonical default") {
				t.Fatalf("eager room = %+v, seat=%+v, %v; want %+v", got, seat, err, want)
			}
			for name, raw := range files {
				if after, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(after, []byte(raw)) {
					t.Fatalf("source %s changed: %q, %v", name, after, err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), []byte("{stale"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := database.Close(filepath.Dir(s.Root)); err != nil {
				t.Fatal(err)
			}
			if err := Migrate(filepath.Dir(s.Root)); err != nil {
				t.Fatalf("completed import inspected stale sources: %v", err)
			}
			db, err = database.Open(filepath.Dir(s.Root))
			if err != nil {
				t.Fatal(err)
			}
			var afterBody string
			var afterSeat sql.NullString
			if err := db.QueryRow("SELECT body,seat FROM room_records WHERE id=?", id).Scan(&afterBody, &afterSeat); err != nil || afterBody != body || afterSeat != seat {
				t.Fatalf("startup replay changed canonical row: %s, %+v, %v", afterBody, afterSeat, err)
			}
			var version int
			if err := db.QueryRow("SELECT value FROM meta WHERE key='room.records.import'").Scan(&version); err != nil || version != 1 {
				t.Fatalf("import version = %d, %v", version, err)
			}
		})
	}
}
